package conversation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	slackAPIBase = "https://slack.com/api"

	// Rate limiting — stay well under Slack's tier limits.
	searchDelay  = 3 * time.Second // Tier 2: ~20/min
	repliesDelay = time.Second     // Tier 3: ~50/min
	searchPages  = 5               // max pages to fetch (500 messages)
	searchCount  = 100             // results per page
	maxWindows   = 50              // max channel windows to fetch
)

// Slack fetches conversations from the Slack Web API using a user token
// (MUSE_SLACK_TOKEN). API results are cached locally at ~/.muse/cache/slack/
// so the API cost is paid once; subsequent runs only fetch conversations
// updated since the last sync.
type Slack struct{}

func (s *Slack) Name() string { return "Slack" }

// cachedSlackConv stores raw API data for a single Slack conversation (thread
// or channel window). Stored upstream of conversation assembly so formatting
// changes don't require re-fetching.
type cachedSlackConv struct {
	TeamID      string         `json:"team_id"`
	TeamName    string         `json:"team_name"`
	ChannelID   string         `json:"channel_id"`
	ChannelName string         `json:"channel_name"`
	ThreadTS    string         `json:"thread_ts,omitempty"`    // empty for channel windows
	WindowStart string         `json:"window_start,omitempty"` // empty for threads
	OwnerID     string         `json:"owner_id"`
	Messages    []slackMessage `json:"messages"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type slackSyncState struct {
	LastSync time.Time `json:"last_sync"`
	UserID   string    `json:"user_id"`
}

func (s *Slack) Conversations() ([]Conversation, error) {
	token := os.Getenv("MUSE_SLACK_TOKEN")
	if token == "" {
		return nil, nil
	}

	cacheDir, err := slackCacheDir()
	if err != nil {
		return nil, fmt.Errorf("slack: cache dir: %w", err)
	}

	client := &slackClient{
		token:  token,
		cookie: os.Getenv("MUSE_SLACK_COOKIE"), // required for xoxc- tokens, optional for xoxp-
		http:   &http.Client{Timeout: 30 * time.Second},
	}

	// Identify the authenticated user and workspace.
	userID, teamID, teamName, err := client.authTest()
	if err != nil {
		return nil, fmt.Errorf("slack: auth.test: %w", err)
	}

	state := loadSlackSyncState(cacheDir, teamID)

	// User changed → invalidate cache for this workspace.
	if state.UserID != "" && state.UserID != userID {
		os.RemoveAll(filepath.Join(cacheDir, teamID, "conversations"))
		state = slackSyncState{}
	}

	syncStart := time.Now()
	ws := slackWorkspace{teamID: teamID, name: teamName}
	if err := syncSlackConversations(client, cacheDir, ws, userID, state); err != nil {
		fmt.Fprintf(os.Stderr, "slack: %s: sync incomplete: %v\n", teamName, err)
	} else {
		saveSlackSyncState(cacheDir, teamID, slackSyncState{
			LastSync: syncStart,
			UserID:   userID,
		})
	}

	// Assemble conversations from cache.
	cached, err := loadAllCachedSlackConvs(cacheDir)
	if err != nil {
		return nil, err
	}

	var conversations []Conversation
	for _, c := range cached {
		conv := assembleSlackConversation(c)
		if conv != nil {
			conversations = append(conversations, *conv)
		}
	}
	return conversations, nil
}

type slackWorkspace struct {
	teamID string
	name   string
}

// ── Cache I/O ──────────────────────────────────────────────────────────

func slackCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".muse", "cache", "slack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func loadSlackSyncState(cacheDir, teamID string) slackSyncState {
	data, err := os.ReadFile(filepath.Join(cacheDir, teamID, "state.json"))
	if err != nil {
		return slackSyncState{}
	}
	var state slackSyncState
	json.Unmarshal(data, &state)
	return state
}

func saveSlackSyncState(cacheDir, teamID string, state slackSyncState) {
	dir := filepath.Join(cacheDir, teamID)
	os.MkdirAll(dir, 0o755)
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644)
}

func slackConvCachePath(cacheDir string, c *cachedSlackConv) string {
	id := c.ThreadTS
	if id == "" {
		id = c.WindowStart
	}
	id = strings.ReplaceAll(id, ".", "_")
	return filepath.Join(cacheDir, c.TeamID, "conversations", c.ChannelID, id+".json")
}

func saveCachedSlackConv(cacheDir string, c *cachedSlackConv) error {
	path := slackConvCachePath(cacheDir, c)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func loadAllCachedSlackConvs(cacheDir string) ([]cachedSlackConv, error) {
	var convs []cachedSlackConv
	filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		if filepath.Base(path) == "state.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var c cachedSlackConv
		if err := json.Unmarshal(data, &c); err != nil {
			return nil
		}
		convs = append(convs, c)
		return nil
	})
	return convs, nil
}

// ── Sync ───────────────────────────────────────────────────────────────

func syncSlackConversations(client *slackClient, cacheDir string, ws slackWorkspace, userID string, state slackSyncState) error {
	threads, windows, err := client.searchUserMessages(userID, state.LastSync)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	if !state.LastSync.IsZero() {
		fmt.Fprintf(os.Stderr, "slack: %s: incremental sync since %s\n", ws.name, state.LastSync.Format(time.DateOnly))
	} else {
		fmt.Fprintf(os.Stderr, "slack: %s: initial sync — %d threads, %d windows\n", ws.name, len(threads), len(windows))
	}

	var synced int

	for _, t := range threads {
		time.Sleep(repliesDelay)
		msgs, err := client.conversationsReplies(t.channelID, t.threadTS)
		if err != nil {
			continue
		}
		cached := &cachedSlackConv{
			TeamID:      ws.teamID,
			TeamName:    ws.name,
			ChannelID:   t.channelID,
			ChannelName: t.channelName,
			ThreadTS:    t.threadTS,
			OwnerID:     userID,
			Messages:    msgs,
			UpdatedAt:   slackTSToTime(msgs[len(msgs)-1].TS),
		}
		saveCachedSlackConv(cacheDir, cached)
		synced++
	}

	if len(windows) > maxWindows {
		windows = windows[:maxWindows]
	}
	for _, w := range windows {
		time.Sleep(repliesDelay)
		msgs, err := client.conversationsHistory(w.channelID, w.oldest, w.latest)
		if err != nil {
			continue
		}
		if len(msgs) < 2 {
			continue
		}
		cached := &cachedSlackConv{
			TeamID:      ws.teamID,
			TeamName:    ws.name,
			ChannelID:   w.channelID,
			ChannelName: w.channelName,
			WindowStart: w.oldest,
			OwnerID:     userID,
			Messages:    msgs,
			UpdatedAt:   slackTSToTime(msgs[len(msgs)-1].TS),
		}
		saveCachedSlackConv(cacheDir, cached)
		synced++
	}

	if synced > 0 {
		fmt.Fprintf(os.Stderr, "slack: %s: cached %d conversations\n", ws.name, synced)
	}
	return nil
}

// ── Assembly ───────────────────────────────────────────────────────────

func assembleSlackConversation(c cachedSlackConv) *Conversation {
	if len(c.Messages) < 2 {
		return nil
	}

	var messages []Message
	var ownerMsgCount int
	for _, m := range c.Messages {
		if isSlackNoise(m) {
			continue
		}
		role := "assistant"
		if m.User == c.OwnerID {
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
		if m.Content != "" {
			title = truncate(m.Content, 100)
			break
		}
	}

	var createdAt, updatedAt time.Time
	if len(messages) > 0 {
		createdAt = messages[0].Timestamp
		updatedAt = messages[len(messages)-1].Timestamp
	}

	project := c.TeamName
	if c.ChannelName != "" {
		project = c.TeamName + "/#" + c.ChannelName
	}

	convKey := c.ThreadTS
	if convKey == "" {
		convKey = c.WindowStart
	}

	return &Conversation{
		SchemaVersion:  1,
		Source:         "slack",
		ConversationID: fmt.Sprintf("%s:%s:%s", c.TeamID, c.ChannelID, convKey),
		Project:        project,
		Title:          title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Messages:       messages,
	}
}

// ── Slack API client ───────────────────────────────────────────────────

type slackClient struct {
	token  string
	cookie string // optional, required for xoxc- tokens
	http   *http.Client
}

func (c *slackClient) do(method string, params url.Values) (json.RawMessage, error) {
	u := fmt.Sprintf("%s/%s?%s", slackAPIBase, method, params.Encode())
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.cookie != "" {
		req.Header.Set("Cookie", "d="+c.cookie)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
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
		OK    bool   `json:"ok"`
		Error string `json:"error"`
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

func (c *slackClient) authTest() (userID, teamID, teamName string, err error) {
	body, err := c.do("auth.test", url.Values{})
	if err != nil {
		return "", "", "", err
	}
	var result struct {
		UserID string `json:"user_id"`
		TeamID string `json:"team_id"`
		Team   string `json:"team"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", "", err
	}
	return result.UserID, result.TeamID, result.Team, nil
}

// ── Search ─────────────────────────────────────────────────────────────

type slackThread struct {
	channelID   string
	channelName string
	threadTS    string
}

type channelWindow struct {
	channelID   string
	channelName string
	oldest      string
	latest      string
}

func (c *slackClient) searchUserMessages(userID string, since time.Time) ([]slackThread, []channelWindow, error) {
	seenThreads := map[string]bool{}
	var threads []slackThread
	var standaloneMsgs []chanMsg

	for page := 1; page <= searchPages; page++ {
		if page > 1 {
			time.Sleep(searchDelay)
		}

		query := fmt.Sprintf("from:<@%s>", userID)
		if !since.IsZero() {
			query += fmt.Sprintf(" after:%s", since.Format("2006-01-02"))
		}
		params := url.Values{
			"query": {query},
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
				key := m.Channel.ID + ":" + m.ThreadTS
				if !seenThreads[key] {
					seenThreads[key] = true
					threads = append(threads, slackThread{
						channelID:   m.Channel.ID,
						channelName: m.Channel.Name,
						threadTS:    m.ThreadTS,
					})
				}
			} else {
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

	windows := groupByTimeWindow(standaloneMsgs)
	return threads, windows, nil
}

type chanMsg struct {
	channelID   string
	channelName string
	ts          string
}

func groupByTimeWindow(msgs []chanMsg) []channelWindow {
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

	const windowGap = 3600.0 // 1 hour

	var windows []channelWindow
	for chID, ci := range byChannel {
		if len(ci.timestamps) == 0 {
			continue
		}
		windowStart := ci.timestamps[0]
		prevTS := ci.timestamps[0]
		for i := 1; i < len(ci.timestamps); i++ {
			ts := ci.timestamps[i]
			gap := slackTSToTime(prevTS).Sub(slackTSToTime(ts)).Abs().Seconds()
			if gap > windowGap {
				windows = append(windows, channelWindow{
					channelID: chID, channelName: ci.name,
					oldest: windowStart, latest: prevTS,
				})
				windowStart = ts
			}
			prevTS = ts
		}
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
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
	User     string `json:"user"`
	Channel  struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
}

// ── Conversation fetching ──────────────────────────────────────────────

type slackMessage struct {
	User    string `json:"user"`
	Text    string `json:"text"`
	TS      string `json:"ts"`
	Subtype string `json:"subtype"`
	BotID   string `json:"bot_id"`
}

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

func (c *slackClient) conversationsHistory(channelID, oldest, latest string) ([]slackMessage, error) {
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

	// Reverse to chronological order.
	msgs := result.Messages
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// ── Filtering ──────────────────────────────────────────────────────────

func isSlackNoise(m slackMessage) bool {
	if m.BotID != "" || m.Subtype == "bot_message" {
		return true
	}
	switch m.Subtype {
	case "channel_join", "channel_leave", "channel_topic", "channel_purpose",
		"channel_name", "channel_archive", "channel_unarchive",
		"group_join", "group_leave", "group_topic", "group_purpose":
		return true
	}
	if isURLOnly(m.Text) {
		return true
	}
	return false
}

var urlOnlyRE = regexp.MustCompile(`^\s*<https?://[^>]+>\s*$`)

func isURLOnly(text string) bool {
	return urlOnlyRE.MatchString(text)
}

// ── Utilities ──────────────────────────────────────────────────────────

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
