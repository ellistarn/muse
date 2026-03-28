package conversation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsSlackNoise(t *testing.T) {
	tests := []struct {
		name string
		msg  slackMessage
		want bool
	}{
		{"normal message", slackMessage{User: "U123", Text: "I think we should refactor the auth module"}, false},
		{"bot message", slackMessage{BotID: "B123", Text: "Deployment complete"}, true},
		{"bot subtype", slackMessage{Subtype: "bot_message", Text: "Alert"}, true},
		{"channel join", slackMessage{Subtype: "channel_join", User: "U123"}, true},
		{"channel leave", slackMessage{Subtype: "channel_leave", User: "U123"}, true},
		{"url only", slackMessage{User: "U123", Text: "<https://example.com/some/path>"}, true},
		{"url with commentary", slackMessage{User: "U123", Text: "Check this out: <https://example.com/some/path>"}, false},
		{"group topic", slackMessage{Subtype: "group_topic"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSlackNoise(tt.msg); got != tt.want {
				t.Errorf("isSlackNoise() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlackTSToTime(t *testing.T) {
	tests := []struct {
		ts   string
		want time.Time
	}{
		{"1508284197.000015", time.Unix(1508284197, 15000)},
		{"1508284197", time.Unix(1508284197, 0)},
		{"", time.Time{}},
		{"notanumber", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.ts, func(t *testing.T) {
			if got := slackTSToTime(tt.ts); !got.Equal(tt.want) {
				t.Errorf("slackTSToTime(%q) = %v, want %v", tt.ts, got, tt.want)
			}
		})
	}
}

func TestChunkChannel(t *testing.T) {
	t.Run("basic chunking with attribution", func(t *testing.T) {
		ch := cachedChannel{
			TeamID: "T123", TeamName: "TestWS",
			ChannelID: "C456", ChannelName: "general",
			OwnerID: "OWNER",
			Users:   map[string]string{"OWNER": "Ellis", "U999": "Alice"},
			Messages: []slackMessage{
				{User: "U999", Text: "Anyone have thoughts on the new design?", TS: "1000.000"},
				{User: "OWNER", Text: "I think we should use a tree structure instead of flat lists", TS: "1001.000"},
				{User: "U999", Text: "Interesting, why?", TS: "1002.000"},
				{User: "OWNER", Text: "Because the hierarchy is load-bearing", TS: "1003.000"},
			},
		}
		convs := chunkChannel(ch)
		if len(convs) != 1 {
			t.Fatalf("got %d conversations, want 1", len(convs))
		}
		conv := convs[0]
		if conv.Source != "slack" {
			t.Errorf("Source = %q, want %q", conv.Source, "slack")
		}
		if conv.ConversationID != "T123:C456:0" {
			t.Errorf("ConversationID = %q, want %q", conv.ConversationID, "T123:C456:0")
		}
		if conv.Project != "TestWS/#general" {
			t.Errorf("Project = %q, want %q", conv.Project, "TestWS/#general")
		}
		if len(conv.Messages) != 4 {
			t.Fatalf("got %d messages, want 4", len(conv.Messages))
		}
		// Verify roles
		if conv.Messages[0].Role != "assistant" {
			t.Errorf("peer role = %q, want assistant", conv.Messages[0].Role)
		}
		if conv.Messages[1].Role != "user" {
			t.Errorf("owner role = %q, want user", conv.Messages[1].Role)
		}
		// Verify attribution
		if got := conv.Messages[0].Content; got != "@Alice: Anyone have thoughts on the new design?" {
			t.Errorf("peer content = %q", got)
		}
		if got := conv.Messages[1].Content; got != "@Ellis: I think we should use a tree structure instead of flat lists" {
			t.Errorf("owner content = %q", got)
		}
	})

	t.Run("empty messages", func(t *testing.T) {
		ch := cachedChannel{OwnerID: "OWNER"}
		if convs := chunkChannel(ch); len(convs) != 0 {
			t.Errorf("expected 0 conversations for empty channel, got %d", len(convs))
		}
	})

	t.Run("owner not participating", func(t *testing.T) {
		ch := cachedChannel{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C456",
			OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "U999", Text: "Some discussion", TS: "1000.000"},
				{User: "U888", Text: "I agree", TS: "1001.000"},
			},
		}
		if convs := chunkChannel(ch); len(convs) != 0 {
			t.Errorf("expected 0 conversations when owner didn't participate, got %d", len(convs))
		}
	})

	t.Run("filters noise", func(t *testing.T) {
		ch := cachedChannel{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C456",
			OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "OWNER", Text: "Here's my take", TS: "1000.000"},
				{BotID: "B123", Subtype: "bot_message", Text: "Deploy notification", TS: "1001.000"},
				{Subtype: "channel_join", User: "U999", TS: "1002.000"},
				{User: "U999", Text: "Good point", TS: "1003.000"},
			},
		}
		convs := chunkChannel(ch)
		if len(convs) != 1 {
			t.Fatalf("expected 1 conversation, got %d", len(convs))
		}
		if len(convs[0].Messages) != 2 {
			t.Errorf("got %d messages after filtering, want 2", len(convs[0].Messages))
		}
	})

	t.Run("splits large channels into chunks", func(t *testing.T) {
		ch := cachedChannel{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C456",
			ChannelName: "design", OwnerID: "OWNER",
		}
		// Generate enough messages to exceed chunkSize.
		for i := 0; i < 500; i++ {
			user := "U999"
			if i%3 == 0 {
				user = "OWNER"
			}
			ch.Messages = append(ch.Messages, slackMessage{
				User: user,
				Text: fmt.Sprintf("Message %d with enough text to take up space in the chunk budget for testing purposes", i),
				TS:   fmt.Sprintf("%d.000", 1000+i),
			})
		}
		convs := chunkChannel(ch)
		if len(convs) < 2 {
			t.Errorf("expected multiple chunks for 500 messages, got %d", len(convs))
		}
		// Second chunk should have [part N] prefix in title.
		if len(convs) > 1 && !contains(convs[1].Title, "[part") {
			t.Errorf("chunk 2 title = %q, expected [part N] prefix", convs[1].Title)
		}
		// Verify chunk IDs are sequential.
		for i, conv := range convs {
			wantID := fmt.Sprintf("T123:C456:%d", i)
			if conv.ConversationID != wantID {
				t.Errorf("chunk %d ID = %q, want %q", i, conv.ConversationID, wantID)
			}
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSearchResultParsing(t *testing.T) {
	raw := []byte(`{
		"ok": true,
		"messages": {
			"matches": [
				{"ts": "1000.001", "thread_ts": "1000.000", "text": "reply", "user": "U123", "channel": {"id": "C456", "name": "general"}},
				{"ts": "2000.001", "text": "standalone", "user": "U123", "channel": {"id": "C789", "name": "random"}}
			],
			"pagination": {"page_count": 1}
		}
	}`)
	var result searchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages.Matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(result.Messages.Matches))
	}
	if result.Messages.Matches[0].ThreadTS != "1000.000" {
		t.Errorf("first match ThreadTS = %q", result.Messages.Matches[0].ThreadTS)
	}
	if result.Messages.Matches[1].ThreadTS != "" {
		t.Errorf("second match ThreadTS = %q, want empty", result.Messages.Matches[1].ThreadTS)
	}
}

func TestSlackAPIIntegration(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"user_id":"UOWNER","team_id":"T123","team":"TestCorp"}`)
	})
	mux.HandleFunc("/search.messages", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":{"matches":[
			{"ts":"100.001","thread_ts":"100.000","text":"reply","user":"UOWNER","channel":{"id":"C001","name":"arch"}},
			{"ts":"200.001","text":"standalone","user":"UOWNER","channel":{"id":"C001","name":"arch"}}
		],"pagination":{"page_count":1}}}`)
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":[
			{"user":"UOWNER","text":"standalone msg","ts":"200.001"},
			{"user":"U999","text":"What about the API?","ts":"100.000","reply_count":2}
		],"has_more":false}`)
	})
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":[
			{"user":"U999","text":"What about the API?","ts":"100.000"},
			{"user":"UOWNER","text":"Resource-oriented design.","ts":"100.001"},
			{"user":"U999","text":"Elaborate?","ts":"100.002"},
			{"user":"UOWNER","text":"Uniform interface. Composes better.","ts":"100.003"}
		],"has_more":false}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	t.Run("flat channel assembly", func(t *testing.T) {
		client := &slackClient{
			token:   "xoxp-test",
			apiBase: server.URL,
			http:    &http.Client{Timeout: 5 * time.Second},
		}

		activity, err := client.searchUserActivity("UOWNER", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if len(activity) != 1 {
			t.Fatalf("got %d channels, want 1", len(activity))
		}
		ch := activity[0]
		if ch.channelID != "C001" {
			t.Errorf("channelID = %q", ch.channelID)
		}
		if len(ch.threads) != 1 || !ch.threads["100.000"] {
			t.Errorf("threads = %v, want {100.000}", ch.threads)
		}

		msgs, err := client.fetchChannelFlat(ch.channelID, ch.oldest, ch.latest, ch.threads)
		if err != nil {
			t.Fatal(err)
		}
		// Should have: 100.000 (thread parent), 100.001 (reply), 100.002 (reply),
		// 100.003 (reply), 200.001 (standalone) = 5 messages, sorted.
		if len(msgs) != 5 {
			t.Fatalf("got %d messages, want 5", len(msgs))
		}
		if msgs[0].TS != "100.000" || msgs[4].TS != "200.001" {
			t.Errorf("messages not sorted: first=%s last=%s", msgs[0].TS, msgs[4].TS)
		}

		// Chunk into conversations.
		cached := cachedChannel{
			TeamID: "T123", TeamName: "TestCorp",
			ChannelID: "C001", ChannelName: "arch",
			OwnerID: "UOWNER", Messages: msgs,
		}
		convs := chunkChannel(cached)
		if len(convs) == 0 {
			t.Fatal("expected at least 1 conversation")
		}
		if convs[0].Source != "slack" {
			t.Errorf("Source = %q", convs[0].Source)
		}
		if err := convs[0].Validate(); err != nil {
			t.Errorf("Validate() failed: %v", err)
		}
	})
}
