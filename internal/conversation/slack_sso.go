package conversation

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// slackSSO holds credentials obtained via SAML SSO.
type slackSSO struct {
	token  string         // xoxc-... token
	cookie string         // d cookie value
	jar    http.CookieJar // full session cookie jar
}

// slackSAMLAuth obtains Slack credentials by following the workspace's SAML
// SSO redirect chain. It loads cookies from cookiePath (Netscape format),
// follows redirects through whatever IDP the workspace uses, submits SAML
// forms, and extracts the xoxc token from the final response.
func slackSAMLAuth(cookiePath, workspace string) (*slackSSO, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	n, err := loadNetscapeCookies(jar, cookiePath)
	if err != nil {
		return nil, fmt.Errorf("load cookies from %s: %w", cookiePath, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("no cookies found in %s", cookiePath)
	}

	ssoURL := fmt.Sprintf("https://%s/sso/saml/start?redir=%%2Fssb%%2Fsignin_redirect&action=login", workspace)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Follow the SAML redirect chain. Each iteration handles one hop:
	// redirects are followed, HTML forms are parsed and submitted.
	currentURL := ssoURL
	var lastBody string
	for i := 0; i < 20; i++ {
		resp, err := client.Get(currentURL)
		if err != nil {
			return nil, fmt.Errorf("step %d GET %s: %w", i, truncateURL(currentURL), err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastBody = string(body)

		switch {
		case resp.StatusCode == 302 || resp.StatusCode == 307:
			loc := resp.Header.Get("Location")
			if loc == "" {
				return nil, fmt.Errorf("step %d: redirect with no Location header", i)
			}
			currentURL = resolveURL(currentURL, loc)

		case resp.StatusCode == 200 && containsForm(lastBody):
			// HTML form (e.g. SAML assertion) — parse and submit.
			currentURL, lastBody, err = submitFormChain(client, currentURL, lastBody)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", i, err)
			}
			if currentURL == "" {
				// No more redirects — lastBody is the final page.
				goto extractToken
			}

		case resp.StatusCode == 200:
			goto extractToken

		default:
			return nil, fmt.Errorf("step %d: unexpected status %d from %s", i, resp.StatusCode, truncateURL(currentURL))
		}
	}
	return nil, fmt.Errorf("SAML chain exceeded 20 redirects")

extractToken:
	token := extractXoxcToken(lastBody)
	if token == "" {
		return nil, fmt.Errorf("SAML flow completed but no xoxc token found in response")
	}

	// Extract d cookie from the jar.
	var dCookie string
	for _, host := range []string{workspace, "slack.com", "enterprise.slack.com"} {
		u := &url.URL{Scheme: "https", Host: host, Path: "/"}
		for _, c := range jar.Cookies(u) {
			if c.Name == "d" {
				dCookie = c.Value
				break
			}
		}
		if dCookie != "" {
			break
		}
	}
	if decoded, err := url.QueryUnescape(dCookie); err == nil {
		dCookie = decoded
	}

	return &slackSSO{token: token, cookie: dCookie, jar: jar}, nil
}

// submitFormChain parses and submits HTML forms, following any redirects.
// Returns the next URL to GET (if redirect), or ("", body, nil) if the
// chain ends at a 200 page.
func submitFormChain(client *http.Client, baseURL, html string) (nextURL, finalBody string, err error) {
	for range 5 { // max 5 nested form submissions
		action, formData := parseForm(html)
		if action == "" {
			return "", html, nil
		}
		postURL := resolveURL(baseURL, action)
		resp, err := client.PostForm(postURL, formData)
		if err != nil {
			return "", "", fmt.Errorf("POST %s: %w", truncateURL(postURL), err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 302 || resp.StatusCode == 307 {
			return resolveURL(postURL, resp.Header.Get("Location")), "", nil
		}
		if resp.StatusCode == 200 && containsForm(string(body)) {
			// Another form — continue submitting.
			html = string(body)
			baseURL = postURL
			continue
		}
		return "", string(body), nil
	}
	return "", html, nil
}

// ── Cookie loading ─────────────────────────────────────────────────────

// loadNetscapeCookies reads a Netscape-format cookie file into an http.CookieJar.
func loadNetscapeCookies(jar http.CookieJar, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	cookieRE := regexp.MustCompile(
		`^(?:#HttpOnly_)?\.?(\S+)\s+(?:TRUE|FALSE)\s+(\S+)\s+(?:TRUE|FALSE)\s+\d+\s+(\S+)(?:\s+(.+))?\s*$`,
	)

	var count int
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		m := cookieRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: m[1], Path: m[2]}, []*http.Cookie{{
			Name: m[3], Value: strings.TrimSpace(m[4]), Path: m[2], Domain: m[1],
		}})
		count++
	}
	return count, nil
}

// ── HTML form parsing ──────────────────────────────────────────────────

var (
	formActionRE = regexp.MustCompile(`<form[^>]*action="([^"]*)"`)
	inputRE      = regexp.MustCompile(`<input[^>]*>`)
	nameRE       = regexp.MustCompile(`name="([^"]*)"`)
	valueRE      = regexp.MustCompile(`value="([^"]*)"`)
)

func containsForm(html string) bool {
	return formActionRE.MatchString(html) && strings.Contains(html, `type="hidden"`)
}

func parseForm(html string) (action string, data url.Values) {
	data = url.Values{}
	m := formActionRE.FindStringSubmatch(html)
	if m == nil {
		return "", nil
	}
	action = htmlUnescape(m[1])
	for _, input := range inputRE.FindAllString(html, -1) {
		nm := nameRE.FindStringSubmatch(input)
		if nm == nil {
			continue
		}
		val := ""
		if vm := valueRE.FindStringSubmatch(input); vm != nil {
			val = htmlUnescape(vm[1])
		}
		data.Set(nm[1], val)
	}
	return action, data
}

func htmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&apos;", "'")
	return s
}

// ── Token extraction ───────────────────────────────────────────────────

var xoxcRE = regexp.MustCompile(`"api_token":"(xoxc-[^"]+)"`)

func extractXoxcToken(html string) string {
	if m := xoxcRE.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return ""
}

// ── URL helpers ────────────────────────────────────────────────────────

func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	u, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return u.ResolveReference(r).String()
}

func truncateURL(u string) string {
	if len(u) > 80 {
		return u[:80] + "..."
	}
	return u
}
