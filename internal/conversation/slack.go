package conversation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const (
	slackAppDir      = "Library/Application Support/Slack"
	slackLevelDBPath = "Local Storage/leveldb"
	slackCookiesDB   = "Cookies"
	slackAPIBase     = "https://slack.com/api"

	// Chromium cookie decryption constants (macOS).
	chromiumSalt       = "saltysalt"
	chromiumIterations = 1003
	chromiumKeyLen     = 16

	// Rate limiting — stay well under Slack's tier limits.
	searchDelay  = 3 * time.Second // Tier 2: ~20/min
	repliesDelay = time.Second     // Tier 3: ~50/min
	searchPages  = 5               // max pages to fetch (500 messages)
	searchCount  = 100             // results per page
	maxWindows   = 50              // max channel windows to fetch
)

// Slack reads conversations from the Slack desktop app's local storage (macOS
// only). It discovers xoxc tokens from LevelDB and decrypts the d cookie from
// the Cookies SQLite database using the macOS Keychain, then uses the Slack
// Web API to fetch threads the user participated in.
type Slack struct{}

func (s *Slack) Name() string { return "Slack" }

func (s *Slack) Conversations() ([]Conversation, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("slack: home directory: %w", err)
	}
	appDir := filepath.Join(home, slackAppDir)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return nil, nil // Slack not installed
	}

	creds, err := discoverSlackCredentials(appDir)
	if err != nil {
		return nil, fmt.Errorf("slack: credentials: %w", err)
	}
	if len(creds.tokens) == 0 {
		return nil, nil
	}

	var conversations []Conversation
	for _, ws := range creds.tokens {
		convs, err := fetchWorkspaceConversations(creds.cookie, ws)
		if err != nil {
			// Treat per-workspace errors as warnings — continue with others.
			fmt.Fprintf(os.Stderr, "slack: workspace %s: %v\n", ws.name, err)
			continue
		}
		conversations = append(conversations, convs...)
	}
	return conversations, nil
}

// slackCredentials holds all discovered credentials from the local Slack app.
type slackCredentials struct {
	cookie string           // decrypted d cookie (xoxd-...)
	tokens []slackWorkspace // one per workspace
}

type slackWorkspace struct {
	teamID string
	name   string
	token  string // xoxc-...
}

// discoverSlackCredentials reads tokens from LevelDB and decrypts the d cookie.
func discoverSlackCredentials(appDir string) (slackCredentials, error) {
	tokens, err := readSlackTokens(filepath.Join(appDir, slackLevelDBPath))
	if err != nil {
		return slackCredentials{}, fmt.Errorf("tokens: %w", err)
	}
	if len(tokens) == 0 {
		return slackCredentials{}, nil
	}

	cookie, err := readSlackCookie(filepath.Join(appDir, slackCookiesDB))
	if err != nil {
		return slackCredentials{}, fmt.Errorf("cookie: %w", err)
	}

	return slackCredentials{cookie: cookie, tokens: tokens}, nil
}

// readSlackTokens reads xoxc tokens from the Slack LevelDB local storage.
func readSlackTokens(dbPath string) ([]slackWorkspace, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Copy LevelDB to temp dir to avoid lock conflicts with running Slack app.
	tmpDir, err := os.MkdirTemp("", "muse-slack-leveldb-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := copyDir(dbPath, tmpDir); err != nil {
		return nil, fmt.Errorf("copy leveldb: %w", err)
	}

	db, err := leveldb.OpenFile(tmpDir, &opt.Options{ReadOnly: true})
	if err != nil {
		// LevelDB may need recovery after unclean copy.
		db, err = leveldb.RecoverFile(tmpDir, nil)
		if err != nil {
			return nil, fmt.Errorf("open leveldb: %w", err)
		}
	}
	defer db.Close()

	// Find the localConfig_v2 key. Chromium prefixes keys with the origin URL.
	iter := db.NewIterator(nil, nil)
	defer iter.Release()

	var keyCount int
	var workspaces []slackWorkspace
	for iter.Next() {
		keyCount++
		key := string(iter.Key())
		if !strings.Contains(key, "localConfig") {
			continue
		}
		ws, err := parseLocalConfig(iter.Value())
		if err != nil {
			continue
		}
		workspaces = append(workspaces, ws...)
	}

	// If structured parsing found tokens, use them.
	if len(workspaces) > 0 {
		return workspaces, nil
	}

	// Fallback: scan raw LevelDB files for xoxc tokens. This catches tokens
	// that exist in compacted/old SST files but aren't in the current DB state
	// (e.g., after Slack updates the localConfig structure without tokens).
	iter.Release()
	seen := map[string]bool{}
	tokenRE := regexp.MustCompile(`xoxc-[0-9]+-[0-9]+-[0-9]+-[0-9a-z]{64}`)
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tmpDir, e.Name()))
		if err != nil {
			continue
		}
		for _, tok := range tokenRE.FindAllString(string(data), -1) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			workspaces = append(workspaces, slackWorkspace{
				token: tok,
				name:  fmt.Sprintf("workspace-%d", len(workspaces)+1),
			})
		}
	}
	return workspaces, nil
}

// localConfig is the JSON structure stored in localConfig_v2.
type localConfig struct {
	Teams     map[string]localConfigTeam `json:"teams"`
	PrevTeams map[string]localConfigTeam `json:"prevTeams"`
}

type localConfigTeam struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

// parseLocalConfig parses the localConfig_v2 value from LevelDB.
func parseLocalConfig(raw []byte) ([]slackWorkspace, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	// Strip 1-byte prefix that Chromium adds.
	data := raw[1:]

	// Detect UTF-16LE encoding (high NUL ratio) and decode if needed.
	data = decodeIfUTF16(data)

	var cfg localConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse localConfig_v2: %w", err)
	}

	var workspaces []slackWorkspace
	// Check both teams and prevTeams — Slack stores active workspaces in one
	// and recently-used workspaces in the other, varying by version.
	for teamID, team := range cfg.Teams {
		if team.Token == "" || !strings.HasPrefix(team.Token, "xoxc-") {
			continue
		}
		workspaces = append(workspaces, slackWorkspace{
			teamID: teamID,
			name:   team.Name,
			token:  team.Token,
		})
	}
	for teamID, team := range cfg.PrevTeams {
		if team.Token == "" || !strings.HasPrefix(team.Token, "xoxc-") {
			continue
		}
		// Skip if already found in teams.
		found := false
		for _, w := range workspaces {
			if w.teamID == teamID {
				found = true
				break
			}
		}
		if !found {
			workspaces = append(workspaces, slackWorkspace{
				teamID: teamID,
				name:   team.Name,
				token:  team.Token,
			})
		}
	}
	return workspaces, nil
}

// decodeIfUTF16 detects and decodes UTF-16LE encoded bytes to UTF-8.
func decodeIfUTF16(data []byte) []byte {
	if len(data) < 4 {
		return data
	}
	// Heuristic: if every other byte is NUL, it's UTF-16LE.
	nuls := 0
	for i := 1; i < len(data) && i < 100; i += 2 {
		if data[i] == 0 {
			nuls++
		}
	}
	if nuls < 20 {
		return data // not UTF-16LE
	}

	u16 := make([]uint16, len(data)/2)
	for i := range u16 {
		u16[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	runes := utf16.Decode(u16)
	return []byte(string(runes))
}

// readSlackCookie reads and decrypts the d cookie from the Slack Cookies SQLite DB.
func readSlackCookie(dbPath string) (string, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return "", fmt.Errorf("cookies database not found")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("open cookies db: %w", err)
	}
	defer db.Close()

	var encrypted []byte
	err = db.QueryRow(`SELECT encrypted_value FROM cookies WHERE host_key = '.slack.com' AND name = 'd'`).Scan(&encrypted)
	if err != nil {
		return "", fmt.Errorf("read cookie: %w", err)
	}

	return decryptChromiumCookie(encrypted)
}

// decryptChromiumCookie decrypts a Chromium v10 encrypted cookie on macOS.
func decryptChromiumCookie(encrypted []byte) (string, error) {
	if len(encrypted) < 4 {
		return "", fmt.Errorf("encrypted value too short")
	}
	// Check for v10 prefix.
	if string(encrypted[:3]) != "v10" {
		return "", fmt.Errorf("unsupported encryption version: %q", encrypted[:3])
	}

	password, err := keychainPassword()
	if err != nil {
		return "", fmt.Errorf("keychain: %w", err)
	}

	key, err := pbkdf2.Key(sha1.New, password, []byte(chromiumSalt), chromiumIterations, chromiumKeyLen)
	if err != nil {
		return "", fmt.Errorf("pbkdf2: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}

	ciphertext := encrypted[3:]
	if len(ciphertext) < 2*aes.BlockSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext not aligned to block size")
	}

	// Chromium's v10 format on macOS embeds the IV in the ciphertext:
	// bytes 0-15 are used internally, bytes 16-31 are the CBC IV, and the
	// actual encrypted data starts at byte 32.
	iv := ciphertext[aes.BlockSize : 2*aes.BlockSize]
	ciphertext = ciphertext[2*aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpad.
	if len(plaintext) == 0 {
		return "", fmt.Errorf("empty plaintext")
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen > aes.BlockSize || padLen == 0 {
		return "", fmt.Errorf("invalid PKCS7 padding")
	}
	plaintext = plaintext[:len(plaintext)-padLen]

	return string(plaintext), nil
}

// keychainPassword retrieves the Slack Safe Storage password from macOS Keychain.
func keychainPassword() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", "Slack Safe Storage", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("security command: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// fetchWorkspaceConversations fetches threads from a single Slack workspace.
func fetchWorkspaceConversations(cookie string, ws slackWorkspace) ([]Conversation, error) {
	client := &slackClient{
		token:  ws.token,
		cookie: cookie,
		http:   &http.Client{Timeout: 30 * time.Second},
	}

	// Identify the authenticated user.
	userID, err := client.authTest()
	if err != nil {
		return nil, fmt.Errorf("auth.test: %w", err)
	}

	// Search for messages the user sent across all channels.
	threads, windows, err := client.searchUserMessages(userID)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	// Fetch full thread replies and map to conversations.
	var conversations []Conversation
	for _, t := range threads {
		time.Sleep(repliesDelay)

		msgs, err := client.conversationsReplies(t.channelID, t.threadTS)
		if err != nil {
			continue // skip individual thread failures
		}

		conv := mapSlackThread(ws, t, msgs, userID)
		if conv == nil {
			continue
		}
		conversations = append(conversations, *conv)
	}

	// Fetch channel history for time-windowed conversations.
	if len(windows) > maxWindows {
		windows = windows[:maxWindows]
	}
	for _, w := range windows {
		time.Sleep(repliesDelay)

		msgs, err := client.conversationsHistory(w.channelID, w.oldest, w.latest)
		if err != nil {
			continue
		}

		conv := mapSlackWindow(ws, w, msgs, userID)
		if conv == nil {
			continue
		}
		conversations = append(conversations, *conv)
	}
	return conversations, nil
}

// slackClient wraps Slack Web API calls with xoxc token + d cookie auth.
type slackClient struct {
	token  string
	cookie string
	http   *http.Client
}

func (c *slackClient) do(method string, params url.Values) (json.RawMessage, error) {
	u := fmt.Sprintf("%s/%s?%s", slackAPIBase, method, params.Encode())
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Cookie", "d="+c.cookie)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		// Rate limited — wait and retry once.
		retryAfter := 5 * time.Second
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		time.Sleep(retryAfter)

		resp2, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		return readSlackResponse(resp2.Body)
	}

	return readSlackResponse(resp.Body)
}

func readSlackResponse(r io.Reader) (json.RawMessage, error) {
	var envelope struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Raw   json.RawMessage `json:"-"`
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if !envelope.OK {
		return nil, fmt.Errorf("slack API error: %s", envelope.Error)
	}
	return body, nil
}

// authTest returns the authenticated user's ID.
func (c *slackClient) authTest() (string, error) {
	body, err := c.do("auth.test", url.Values{})
	if err != nil {
		return "", err
	}
	var result struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.UserID, nil
}

// slackThread represents a thread discovered via search.
type slackThread struct {
	channelID   string
	channelName string
	threadTS    string
	permalink   string
}

// channelWindow represents a group of non-threaded messages in a channel,
// bounded by time. Used to create conversation units from channel flow.
type channelWindow struct {
	channelID   string
	channelName string
	oldest      string // earliest message ts
	latest      string // latest message ts
}

// searchUserMessages finds threads and channel windows the user participated in.
func (c *slackClient) searchUserMessages(userID string) ([]slackThread, []channelWindow, error) {
	seenThreads := map[string]bool{}
	var threads []slackThread

	var standaloneMsgs []chanMsg

	for page := 1; page <= searchPages; page++ {
		if page > 1 {
			time.Sleep(searchDelay)
		}

		params := url.Values{
			"query": {fmt.Sprintf("from:<@%s>", userID)},
			"sort":  {"timestamp"},
			"count": {strconv.Itoa(searchCount)},
			"page":  {strconv.Itoa(page)},
		}
		body, err := c.do("search.messages", params)
		if err != nil {
			return threads, nil, fmt.Errorf("page %d: %w", page, err)
		}

		var result searchResult
		if err := json.Unmarshal(body, &result); err != nil {
			return threads, nil, fmt.Errorf("parse page %d: %w", page, err)
		}

		for _, m := range result.Messages.Matches {
			if m.ThreadTS != "" {
				// Message is in a thread.
				key := m.Channel.ID + ":" + m.ThreadTS
				if !seenThreads[key] {
					seenThreads[key] = true
					threads = append(threads, slackThread{
						channelID:   m.Channel.ID,
						channelName: m.Channel.Name,
						threadTS:    m.ThreadTS,
						permalink:   m.Permalink,
					})
				}
			} else {
				// Standalone message — collect for time-windowed grouping.
				standaloneMsgs = append(standaloneMsgs, chanMsg{
					channelID:   m.Channel.ID,
					channelName: m.Channel.Name,
					ts:          m.TS,
				})
			}
		}

		if page >= result.Messages.Pagination.PageCount {
			break
		}
	}

	// Group standalone messages by channel + time proximity (1 hour gap = new window).
	windows := groupByTimeWindow(standaloneMsgs)

	return threads, windows, nil
}

// chanMsg is a standalone message found via search, used for time-windowed grouping.
type chanMsg struct {
	channelID   string
	channelName string
	ts          string
}

// groupByTimeWindow groups messages by channel, then splits into windows
// where gaps between consecutive messages exceed 1 hour.
func groupByTimeWindow(msgs []chanMsg) []channelWindow {
	// Group by channel.
	type chanInfo struct {
		name       string
		timestamps []string
	}
	byChannel := map[string]*chanInfo{}
	for _, m := range msgs {
		ci, ok := byChannel[m.channelID]
		if !ok {
			ci = &chanInfo{name: m.channelName}
			byChannel[m.channelID] = ci
		}
		ci.timestamps = append(ci.timestamps, m.ts)
	}

	const windowGap = 3600.0 // 1 hour in seconds

	var windows []channelWindow
	for chID, ci := range byChannel {
		if len(ci.timestamps) == 0 {
			continue
		}
		// Messages are already sorted by timestamp (from search results sorted by timestamp).
		windowStart := ci.timestamps[0]
		prevTS := ci.timestamps[0]
		for i := 1; i < len(ci.timestamps); i++ {
			ts := ci.timestamps[i]
			prev := slackTSToTime(prevTS)
			cur := slackTSToTime(ts)
			gap := prev.Sub(cur).Abs().Seconds()
			if gap > windowGap {
				// Close current window and start new one.
				windows = append(windows, channelWindow{
					channelID: chID, channelName: ci.name,
					oldest: windowStart, latest: prevTS,
				})
				windowStart = ts
			}
			prevTS = ts
		}
		// Close final window.
		windows = append(windows, channelWindow{
			channelID: chID, channelName: ci.name,
			oldest: windowStart, latest: prevTS,
		})
	}
	return windows
}

type searchResult struct {
	Messages struct {
		Matches    []searchMatch `json:"matches"`
		Pagination struct {
			PageCount int `json:"page_count"`
		} `json:"pagination"`
	} `json:"messages"`
}

type searchMatch struct {
	TS        string `json:"ts"`
	ThreadTS  string `json:"thread_ts"`
	Text      string `json:"text"`
	User      string `json:"user"`
	Permalink string `json:"permalink"`
	Channel   struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
}

// slackMessage represents a message from conversations.replies.
type slackMessage struct {
	User    string `json:"user"`
	Text    string `json:"text"`
	TS      string `json:"ts"`
	Subtype string `json:"subtype"`
	BotID   string `json:"bot_id"`
}

// conversationsReplies fetches all messages in a thread.
func (c *slackClient) conversationsReplies(channelID, threadTS string) ([]slackMessage, error) {
	var all []slackMessage
	cursor := ""

	for {
		params := url.Values{
			"channel": {channelID},
			"ts":      {threadTS},
			"limit":   {"200"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		body, err := c.do("conversations.replies", params)
		if err != nil {
			return nil, err
		}

		var result struct {
			Messages         []slackMessage `json:"messages"`
			HasMore          bool           `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		all = append(all, result.Messages...)
		if !result.HasMore || result.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = result.ResponseMetadata.NextCursor
		time.Sleep(repliesDelay)
	}
	return all, nil
}

// conversationsHistory fetches messages from a channel within a time window.
// It adds a small buffer around the window to capture context.
func (c *slackClient) conversationsHistory(channelID, oldest, latest string) ([]slackMessage, error) {
	// Add buffer: 5 minutes before oldest, use current time if latest is very recent.
	oldestTime := slackTSToTime(oldest).Add(-5 * time.Minute)
	latestTime := slackTSToTime(latest).Add(5 * time.Minute)

	params := url.Values{
		"channel": {channelID},
		"oldest":  {fmt.Sprintf("%d.000000", oldestTime.Unix())},
		"latest":  {fmt.Sprintf("%d.000000", latestTime.Unix())},
		"limit":   {"200"},
	}

	body, err := c.do("conversations.history", params)
	if err != nil {
		return nil, err
	}

	var result struct {
		Messages []slackMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	// conversations.history returns messages in reverse chronological order.
	// Reverse to chronological.
	msgs := result.Messages
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// mapSlackWindow converts a time-windowed channel conversation to a Conversation.
func mapSlackWindow(ws slackWorkspace, w channelWindow, msgs []slackMessage, ownerID string) *Conversation {
	if len(msgs) < 2 {
		return nil
	}

	var messages []Message
	var ownerMsgCount int
	for _, m := range msgs {
		if isSlackNoise(m) {
			continue
		}
		role := "assistant"
		if m.User == ownerID {
			role = "user"
			ownerMsgCount++
		}
		messages = append(messages, Message{
			Role:      role,
			Content:   m.Text,
			Timestamp: slackTSToTime(m.TS),
		})
	}

	if ownerMsgCount == 0 {
		return nil
	}

	title := ""
	for _, m := range messages {
		if m.Role == "user" && m.Content != "" {
			title = truncate(m.Content, 100)
			break
		}
	}

	var createdAt, updatedAt time.Time
	if len(messages) > 0 {
		createdAt = messages[0].Timestamp
		updatedAt = messages[len(messages)-1].Timestamp
	}

	project := ws.name
	if w.channelName != "" {
		project = ws.name + "/#" + w.channelName
	}

	return &Conversation{
		SchemaVersion:  1,
		Source:         "slack",
		ConversationID: fmt.Sprintf("%s:%s:%s", ws.teamID, w.channelID, w.oldest),
		Project:        project,
		Title:          title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Messages:       messages,
	}
}

// mapSlackThread converts a Slack thread to a Conversation.
func mapSlackThread(ws slackWorkspace, thread slackThread, msgs []slackMessage, ownerID string) *Conversation {
	if len(msgs) == 0 {
		return nil
	}

	var messages []Message
	var ownerMsgCount int
	for _, m := range msgs {
		if isSlackNoise(m) {
			continue
		}
		role := "assistant" // other participants
		if m.User == ownerID {
			role = "user"
			ownerMsgCount++
		}
		messages = append(messages, Message{
			Role:      role,
			Content:   m.Text,
			Timestamp: slackTSToTime(m.TS),
		})
	}

	// Skip threads where the owner didn't contribute meaningfully.
	if ownerMsgCount == 0 {
		return nil
	}

	title := ""
	if len(msgs) > 0 {
		title = truncate(msgs[0].Text, 100)
	}

	var createdAt, updatedAt time.Time
	if len(messages) > 0 {
		createdAt = messages[0].Timestamp
		updatedAt = messages[len(messages)-1].Timestamp
	}

	project := ws.name
	if thread.channelName != "" {
		project = ws.name + "/#" + thread.channelName
	}

	return &Conversation{
		SchemaVersion:  1,
		Source:         "slack",
		ConversationID: fmt.Sprintf("%s:%s:%s", ws.teamID, thread.channelID, thread.threadTS),
		Project:        project,
		Title:          title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Messages:       messages,
	}
}

// isSlackNoise returns true for messages that should be filtered out.
func isSlackNoise(m slackMessage) bool {
	// Bot messages.
	if m.BotID != "" || m.Subtype == "bot_message" {
		return true
	}
	// System messages.
	switch m.Subtype {
	case "channel_join", "channel_leave", "channel_topic", "channel_purpose",
		"channel_name", "channel_archive", "channel_unarchive",
		"group_join", "group_leave", "group_topic", "group_purpose":
		return true
	}
	// URL-only messages (just a link with no commentary).
	if isURLOnly(m.Text) {
		return true
	}
	return false
}

var urlOnlyRE = regexp.MustCompile(`^\s*<https?://[^>]+>\s*$`)

// isURLOnly returns true if the text is just a URL (Slack's <url> format).
func isURLOnly(text string) bool {
	return urlOnlyRE.MatchString(text)
}

// slackTSToTime converts a Slack timestamp ("1234567890.123456") to time.Time.
func slackTSToTime(ts string) time.Time {
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	var micros int64
	if len(parts) == 2 {
		micros, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	return time.Unix(secs, micros*1000)
}

// copyDir copies all files from src to dst (non-recursive, files only).
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
