package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
)

const (
	githubSource     = "github"
	maxDiffHunkLines = 8
)

// ── Types ──────────────────────────────────────────────────────────────

// GitHub reads conversations from GitHub PR and issue comment threads.
// Threads are cached locally so the API cost is paid once; subsequent runs
// only fetch threads updated since the last sync.
type GitHub struct{}

func (g *GitHub) Name() string { return "GitHub" }

// cachedThread stores raw API data for a single GitHub thread.
// Stored upstream of conversation assembly so formatting changes
// don't require re-fetching.
type cachedThread struct {
	Owner     string          `json:"owner"`
	Repo      string          `json:"repo"`
	Number    int             `json:"number"`
	IsPR      bool            `json:"is_pr"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Author    string          `json:"author"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Messages  []cachedMessage `json:"messages"`
}

// cachedMessage stores a single comment from any GitHub comment endpoint.
// Fields are a union across issue comments, PR review comments, and reviews.
type cachedMessage struct {
	Author      string    `json:"author"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	Path        string    `json:"path,omitempty"`         // review comment: file path
	DiffHunk    string    `json:"diff_hunk,omitempty"`    // review comment: diff context
	ReviewState string    `json:"review_state,omitempty"` // review: APPROVED, CHANGES_REQUESTED, etc.
}

type githubSyncState struct {
	LastSync time.Time `json:"last_sync"`
	Username string    `json:"username"`
}

// githubMessage is an intermediate type used during conversation assembly.
type githubMessage struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// ── Provider ───────────────────────────────────────────────────────────

func (g *GitHub) Conversations() ([]Conversation, error) {
	token := resolveGitHubToken()
	if token == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	client := github.NewClient(nil).WithAuthToken(token)

	username, err := resolveGitHubUsername(ctx, client)
	if err != nil {
		return nil, err
	}

	cacheDir, err := githubCacheDir()
	if err != nil {
		return nil, err
	}

	state := loadGitHubSyncState(cacheDir)

	// Username changed → invalidate cache
	if state.Username != "" && state.Username != username {
		os.RemoveAll(filepath.Join(cacheDir, "threads"))
		state = githubSyncState{}
	}

	// Sync: discover and fetch threads from the API
	syncStart := time.Now()
	if err := syncGitHubThreads(ctx, client, username, cacheDir, state); err != nil {
		// Partial sync is fine — cache what we got. Don't advance the sync
		// timestamp so the next run retries.
		fmt.Fprintf(os.Stderr, "github: sync incomplete: %v\n", err)
	} else {
		saveGitHubSyncState(cacheDir, githubSyncState{
			LastSync: syncStart,
			Username: username,
		})
	}

	// Assemble conversations from everything in cache
	threads, err := loadAllCachedThreads(cacheDir)
	if err != nil {
		return nil, err
	}

	var conversations []Conversation
	for _, t := range threads {
		conv := assembleCachedConversation(username, t)
		if conv != nil {
			conversations = append(conversations, *conv)
		}
	}

	sort.Slice(conversations, func(i, j int) bool {
		return conversations[i].UpdatedAt.After(conversations[j].UpdatedAt)
	})

	return conversations, nil
}

func resolveGitHubToken() string {
	if token := os.Getenv("MUSE_GITHUB_TOKEN"); token != "" {
		return token
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func resolveGitHubUsername(ctx context.Context, client *github.Client) (string, error) {
	if username := os.Getenv("MUSE_GITHUB_USERNAME"); username != "" {
		return username, nil
	}
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("failed to resolve GitHub username: %w", err)
	}
	return user.GetLogin(), nil
}

// ── Cache I/O ──────────────────────────────────────────────────────────

func githubCacheDir() (string, error) {
	if dir := os.Getenv("MUSE_GITHUB_CACHE"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	dir := filepath.Join(home, ".muse", "cache", "github")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func loadGitHubSyncState(cacheDir string) githubSyncState {
	data, err := os.ReadFile(filepath.Join(cacheDir, "state.json"))
	if err != nil {
		return githubSyncState{}
	}
	var state githubSyncState
	json.Unmarshal(data, &state)
	return state
}

func saveGitHubSyncState(cacheDir string, state githubSyncState) {
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(cacheDir, "state.json"), data, 0o644)
}

func threadCachePath(cacheDir string, owner, repo string, number int, isPR bool) string {
	kind := "issues"
	if isPR {
		kind = "pull"
	}
	return filepath.Join(cacheDir, "threads", owner, repo, kind, fmt.Sprintf("%d.json", number))
}

func saveCachedThread(cacheDir string, t *cachedThread) error {
	path := threadCachePath(cacheDir, t.Owner, t.Repo, t.Number, t.IsPR)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadCachedThread(path string) (*cachedThread, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t cachedThread
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func loadAllCachedThreads(cacheDir string) ([]cachedThread, error) {
	threadsDir := filepath.Join(cacheDir, "threads")
	if _, err := os.Stat(threadsDir); os.IsNotExist(err) {
		return nil, nil
	}
	var threads []cachedThread
	filepath.Walk(threadsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		t, err := loadCachedThread(path)
		if err != nil {
			return nil // skip corrupt cache files
		}
		threads = append(threads, *t)
		return nil
	})
	return threads, nil
}

// ── Sync ───────────────────────────────────────────────────────────────

func syncGitHubThreads(ctx context.Context, client *github.Client, username, cacheDir string, state githubSyncState) error {
	if state.LastSync.IsZero() {
		return syncGitHubFull(ctx, client, username, cacheDir)
	}
	return syncGitHubIncremental(ctx, client, username, cacheDir, state.LastSync)
}

func syncGitHubFull(ctx context.Context, client *github.Client, username, cacheDir string) error {
	fmt.Fprintf(os.Stderr, "github: initial sync — discovering all threads\n")

	for _, isPR := range []bool{true, false} {
		kind := "pr"
		if !isPR {
			kind = "issue"
		}

		// Check total to decide strategy
		baseQuery := fmt.Sprintf("involves:%s type:%s", username, kind)
		total, err := searchGitHubCount(ctx, client, baseQuery)
		if err != nil {
			return fmt.Errorf("count %ss: %w", kind, err)
		}
		fmt.Fprintf(os.Stderr, "github: %d %ss total\n", total, kind)

		if total <= 1000 {
			issues, err := searchAllGitHubIssues(ctx, client, baseQuery)
			if err != nil {
				return fmt.Errorf("search %ss: %w", kind, err)
			}
			if err := fetchAndCacheIssues(ctx, client, cacheDir, issues, isPR); err != nil {
				return err
			}
		} else {
			if err := dateSegmentedSearch(ctx, client, username, kind, isPR, cacheDir); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncGitHubIncremental(ctx context.Context, client *github.Client, username, cacheDir string, since time.Time) error {
	sinceStr := since.Format("2006-01-02T15:04:05Z")
	fmt.Fprintf(os.Stderr, "github: incremental sync since %s\n", since.Format(time.DateOnly))

	var updated int
	for _, isPR := range []bool{true, false} {
		kind := "pr"
		if !isPR {
			kind = "issue"
		}
		query := fmt.Sprintf("involves:%s type:%s updated:>=%s", username, kind, sinceStr)
		issues, err := searchAllGitHubIssues(ctx, client, query)
		if err != nil {
			return fmt.Errorf("incremental %ss: %w", kind, err)
		}
		if err := fetchAndCacheIssues(ctx, client, cacheDir, issues, isPR); err != nil {
			return err
		}
		updated += len(issues)
	}
	if updated > 0 {
		fmt.Fprintf(os.Stderr, "github: updated %d threads\n", updated)
	}
	return nil
}

// dateSegmentedSearch walks yearly intervals, subdividing into months
// when a year exceeds the 1000-result search API limit.
func dateSegmentedSearch(ctx context.Context, client *github.Client, username, kind string, isPR bool, cacheDir string) error {
	now := time.Now()
	for year := 2008; year <= now.Year(); year++ {
		start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
		if end.After(now) {
			end = now
		}

		yearQuery := fmt.Sprintf("involves:%s type:%s created:%s..%s",
			username, kind,
			start.Format("2006-01-02"), end.Format("2006-01-02"))

		yearTotal, err := searchGitHubCount(ctx, client, yearQuery)
		if err != nil {
			return err
		}
		if yearTotal == 0 {
			continue
		}

		if yearTotal <= 1000 {
			issues, err := searchAllGitHubIssues(ctx, client, yearQuery)
			if err != nil {
				return err
			}
			if err := fetchAndCacheIssues(ctx, client, cacheDir, issues, isPR); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "github: %d %ss from %d\n", len(issues), kind, year)
		} else {
			// Subdivide into months
			for month := time.January; month <= time.December; month++ {
				mStart := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
				mEnd := mStart.AddDate(0, 1, 0)
				if mStart.After(now) {
					break
				}
				if mEnd.After(now) {
					mEnd = now
				}

				mQuery := fmt.Sprintf("involves:%s type:%s created:%s..%s",
					username, kind,
					mStart.Format("2006-01-02"), mEnd.Format("2006-01-02"))

				issues, err := searchAllGitHubIssues(ctx, client, mQuery)
				if err != nil {
					return err
				}
				if len(issues) > 0 {
					if err := fetchAndCacheIssues(ctx, client, cacheDir, issues, isPR); err != nil {
						return err
					}
					fmt.Fprintf(os.Stderr, "github: %d %ss from %s %d\n",
						len(issues), kind, month, year)
				}
			}
		}
	}
	return nil
}

// ── Search helpers ─────────────────────────────────────────────────────

// searchGitHubCount returns the total number of results for a query
// without fetching the results themselves.
func searchGitHubCount(ctx context.Context, client *github.Client, query string) (int, error) {
	result, _, err := client.Search.Issues(ctx, query, &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 1},
	})
	if err != nil {
		return 0, err
	}
	return result.GetTotal(), nil
}

// searchAllGitHubIssues paginates through all search results (up to 1000).
func searchAllGitHubIssues(ctx context.Context, client *github.Client, query string) ([]*github.Issue, error) {
	var all []*github.Issue
	opts := &github.SearchOptions{
		Sort:        "updated",
		Order:       "desc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		result, resp, err := client.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Issues...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// parseRepoURL extracts owner and repo from a GitHub API repository URL.
// e.g. "https://api.github.com/repos/octocat/hello-world" returns ("octocat", "hello-world").
func parseRepoURL(url string) (string, string) {
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

// ── Comment fetching ───────────────────────────────────────────────────

// fetchAndCacheIssues fetches comments for each issue and writes to cache.
// Threads already cached with the same UpdatedAt are skipped.
func fetchAndCacheIssues(ctx context.Context, client *github.Client, cacheDir string, issues []*github.Issue, isPR bool) error {
	for i, issue := range issues {
		owner, repo := parseRepoURL(issue.GetRepositoryURL())
		if owner == "" || repo == "" {
			continue
		}

		// Skip if cache is already up-to-date
		path := threadCachePath(cacheDir, owner, repo, issue.GetNumber(), isPR)
		if existing, err := loadCachedThread(path); err == nil {
			if !issue.GetUpdatedAt().Time.After(existing.UpdatedAt) {
				continue
			}
		}

		messages, err := fetchThreadMessages(ctx, client, owner, repo, issue.GetNumber(), isPR)
		if err != nil {
			continue // skip individual failures
		}

		t := &cachedThread{
			Owner:     owner,
			Repo:      repo,
			Number:    issue.GetNumber(),
			IsPR:      isPR,
			Title:     issue.GetTitle(),
			Body:      issue.GetBody(),
			Author:    issue.GetUser().GetLogin(),
			CreatedAt: issue.GetCreatedAt().Time,
			UpdatedAt: issue.GetUpdatedAt().Time,
			Messages:  messages,
		}
		saveCachedThread(cacheDir, t)

		if (i+1)%50 == 0 {
			fmt.Fprintf(os.Stderr, "github: fetched %d/%d threads\n", i+1, len(issues))
		}
	}
	return nil
}

// fetchThreadMessages fetches all comments from a thread as raw cached messages.
func fetchThreadMessages(ctx context.Context, client *github.Client, owner, repo string, number int, isPR bool) ([]cachedMessage, error) {
	var messages []cachedMessage

	// Issue comments (both PRs and issues have these)
	icOpts := &github.IssueListCommentsOptions{
		Sort:        github.String("created"),
		Direction:   github.String("asc"),
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, number, icOpts)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			if c.GetBody() == "" {
				continue
			}
			messages = append(messages, cachedMessage{
				Author:    c.GetUser().GetLogin(),
				Body:      c.GetBody(),
				CreatedAt: c.GetCreatedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		icOpts.Page = resp.NextPage
	}

	if !isPR {
		return messages, nil
	}

	// PR review comments (line-level)
	rcOpts := &github.PullRequestListCommentsOptions{
		Sort:        "created",
		Direction:   "asc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := client.PullRequests.ListComments(ctx, owner, repo, number, rcOpts)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			if c.GetBody() == "" {
				continue
			}
			messages = append(messages, cachedMessage{
				Author:    c.GetUser().GetLogin(),
				Body:      c.GetBody(),
				CreatedAt: c.GetCreatedAt().Time,
				Path:      c.GetPath(),
				DiffHunk:  c.GetDiffHunk(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		rcOpts.Page = resp.NextPage
	}

	// PR reviews (approval/changes-requested bodies)
	rOpts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, number, rOpts)
		if err != nil {
			return nil, err
		}
		for _, r := range reviews {
			if r.GetBody() == "" {
				continue
			}
			messages = append(messages, cachedMessage{
				Author:      r.GetUser().GetLogin(),
				Body:        r.GetBody(),
				CreatedAt:   r.GetSubmittedAt().Time,
				ReviewState: r.GetState(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		rOpts.Page = resp.NextPage
	}

	return messages, nil
}

// ── Assembly ───────────────────────────────────────────────────────────

// assembleCachedConversation builds a Conversation from a cached thread.
// Returns nil if the owner has fewer than 2 messages (insufficient signal
// for the observation pipeline).
func assembleCachedConversation(username string, t cachedThread) *Conversation {
	var messages []githubMessage

	if t.Body != "" {
		messages = append(messages, githubMessage{
			Author:    t.Author,
			Body:      t.Body,
			CreatedAt: t.CreatedAt,
		})
	}

	for _, m := range t.Messages {
		if isGitHubBot(m.Author) || isGitHubNoise(m.Body) {
			continue
		}
		body := m.Body
		if m.Path != "" || m.DiffHunk != "" {
			body = formatGitHubReviewComment(m.Path, m.DiffHunk, body)
		}
		if m.ReviewState != "" {
			body = fmt.Sprintf("[review: %s]\n%s", strings.ToLower(m.ReviewState), body)
		}
		messages = append(messages, githubMessage{
			Author:    m.Author,
			Body:      body,
			CreatedAt: m.CreatedAt,
		})
	}

	return assembleGitHubConversation(username, t.Owner, t.Repo, t.Number, t.IsPR, t.Title, t.CreatedAt, t.UpdatedAt, messages)
}

// assembleGitHubConversation builds a Conversation from pre-formatted messages.
// Returns nil if the owner has fewer than 2 messages.
func assembleGitHubConversation(username, owner, repo string, number int, isPR bool, title string, createdAt, updatedAt time.Time, messages []githubMessage) *Conversation {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})

	ownerCount := 0
	for _, m := range messages {
		if strings.EqualFold(m.Author, username) {
			ownerCount++
		}
	}
	if ownerCount < 2 {
		return nil
	}

	var convMessages []Message
	for _, m := range messages {
		role := "assistant"
		body := m.Body
		if strings.EqualFold(m.Author, username) {
			role = "user"
		} else {
			// Attribution prefix so the extract prompt recognizes peer review,
			// not AI output. Prevents the refine prompt from discarding
			// observations about the owner's response to human feedback.
			body = fmt.Sprintf("[GitHub comment by @%s]\n%s", m.Author, body)
		}
		convMessages = append(convMessages, Message{
			Role:      role,
			Content:   body,
			Timestamp: m.CreatedAt,
		})
	}

	kind := "issues"
	if isPR {
		kind = "pull"
	}

	return &Conversation{
		SchemaVersion:  1,
		Source:         githubSource,
		ConversationID: fmt.Sprintf("%s/%s/%s/%d", owner, repo, kind, number),
		Project:        fmt.Sprintf("%s/%s", owner, repo),
		Title:          title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Messages:       convMessages,
	}
}

// ── Filtering ──────────────────────────────────────────────────────────

// isGitHubBot returns true if the author is a known bot account.
func isGitHubBot(author string) bool {
	// GitHub App bots have a [bot] suffix
	if strings.HasSuffix(author, "[bot]") {
		return true
	}
	lower := strings.ToLower(author)
	for _, bot := range knownBots {
		if lower == bot {
			return true
		}
	}
	return false
}

// knownBots is the set of CI/automation accounts whose messages are noise
// for the observation pipeline.
var knownBots = []string{
	"k8s-ci-robot",
	"googlebot",
	"codecov",
	"coveralls",
	"netlify",
	"sonarcloud",
}

// isGitHubNoise returns true if a message body is a prow command or other
// zero-signal content that shouldn't enter the observation pipeline.
func isGitHubNoise(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	// Prow commands: single-line messages starting with /
	if !strings.Contains(trimmed, "\n") && strings.HasPrefix(trimmed, "/") {
		cmd := strings.Fields(trimmed)[0]
		for _, pc := range prowCommands {
			if cmd == pc {
				return true
			}
		}
	}
	return false
}

// prowCommands is the set of prow slash commands that carry no muse signal.
var prowCommands = []string{
	"/retest",
	"/test",
	"/lgtm",
	"/approve",
	"/hold",
	"/unhold",
	"/assign",
	"/unassign",
	"/kind",
	"/area",
	"/priority",
	"/remove-kind",
	"/remove-area",
	"/remove-priority",
	"/cc",
	"/uncc",
	"/close",
	"/reopen",
	"/lifecycle",
	"/remove-lifecycle",
	"/milestone",
	"/retitle",
	"/cherry-pick",
	"/ok-to-test",
}

// ── Formatting ─────────────────────────────────────────────────────────

// formatGitHubReviewComment formats a code review comment with file path
// and diff context so the observation pipeline can see what code prompted it.
func formatGitHubReviewComment(path, diffHunk, body string) string {
	if path == "" && diffHunk == "" {
		return body
	}
	var b strings.Builder
	if path != "" {
		fmt.Fprintf(&b, "[%s]\n", path)
	}
	if diffHunk != "" {
		lines := strings.Split(diffHunk, "\n")
		if len(lines) > maxDiffHunkLines {
			lines = lines[len(lines)-maxDiffHunkLines:]
		}
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	return b.String()
}
