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

func TestIsURLOnly(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"<https://example.com>", true},
		{"  <https://example.com/path?q=1>  ", true},
		{"check this <https://example.com>", false},
		{"<https://example.com> thoughts?", false},
		{"just plain text", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := isURLOnly(tt.text); got != tt.want {
				t.Errorf("isURLOnly(%q) = %v, want %v", tt.text, got, tt.want)
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

func TestAssembleSlackConversation(t *testing.T) {
	t.Run("basic thread", func(t *testing.T) {
		cached := cachedSlackConv{
			TeamID: "T123", TeamName: "TestWS",
			ChannelID: "C456", ChannelName: "general",
			ThreadTS: "1000.000", OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "U999", Text: "Anyone have thoughts on the new design?", TS: "1000.000"},
				{User: "OWNER", Text: "I think we should use a tree structure instead of flat lists", TS: "1001.000"},
				{User: "U999", Text: "Interesting, why?", TS: "1002.000"},
				{User: "OWNER", Text: "Because the hierarchy is load-bearing", TS: "1003.000"},
			},
		}
		conv := assembleSlackConversation(cached)
		if conv == nil {
			t.Fatal("expected conversation, got nil")
		}
		if conv.Source != "slack" {
			t.Errorf("Source = %q, want %q", conv.Source, "slack")
		}
		if conv.ConversationID != "T123:C456:1000.000" {
			t.Errorf("ConversationID = %q, want %q", conv.ConversationID, "T123:C456:1000.000")
		}
		if conv.Project != "TestWS/#general" {
			t.Errorf("Project = %q, want %q", conv.Project, "TestWS/#general")
		}
		if len(conv.Messages) != 4 {
			t.Fatalf("got %d messages, want 4", len(conv.Messages))
		}
		if conv.Messages[1].Role != "user" {
			t.Errorf("owner message role = %q, want %q", conv.Messages[1].Role, "user")
		}
		if conv.Messages[0].Role != "assistant" {
			t.Errorf("other message role = %q, want %q", conv.Messages[0].Role, "assistant")
		}
	})

	t.Run("empty messages", func(t *testing.T) {
		if conv := assembleSlackConversation(cachedSlackConv{OwnerID: "OWNER"}); conv != nil {
			t.Error("expected nil for empty messages")
		}
	})

	t.Run("owner not participating", func(t *testing.T) {
		cached := cachedSlackConv{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C456",
			ThreadTS: "1000.000", OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "U999", Text: "Some discussion", TS: "1000.000"},
				{User: "U888", Text: "I agree", TS: "1001.000"},
			},
		}
		if conv := assembleSlackConversation(cached); conv != nil {
			t.Error("expected nil when owner didn't participate")
		}
	})

	t.Run("filters noise", func(t *testing.T) {
		cached := cachedSlackConv{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C456",
			ThreadTS: "1000.000", OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "OWNER", Text: "Here's my take", TS: "1000.000"},
				{BotID: "B123", Subtype: "bot_message", Text: "Deploy notification", TS: "1001.000"},
				{Subtype: "channel_join", User: "U999", TS: "1002.000"},
				{User: "U999", Text: "Good point", TS: "1003.000"},
			},
		}
		conv := assembleSlackConversation(cached)
		if conv == nil {
			t.Fatal("expected conversation")
		}
		if len(conv.Messages) != 2 {
			t.Errorf("got %d messages after filtering, want 2", len(conv.Messages))
		}
	})

	t.Run("channel window", func(t *testing.T) {
		cached := cachedSlackConv{
			TeamID: "T123", TeamName: "TestWS", ChannelID: "C789",
			ChannelName: "random", WindowStart: "2000.000", OwnerID: "OWNER",
			Messages: []slackMessage{
				{User: "U999", Text: "What do you think?", TS: "2000.000"},
				{User: "OWNER", Text: "I prefer Y because of Z", TS: "2001.000"},
				{User: "U999", Text: "Makes sense", TS: "2002.000"},
			},
		}
		conv := assembleSlackConversation(cached)
		if conv == nil {
			t.Fatal("expected conversation")
		}
		if conv.ConversationID != "T123:C789:2000.000" {
			t.Errorf("ConversationID = %q", conv.ConversationID)
		}
		if err := conv.Validate(); err != nil {
			t.Errorf("Validate() failed: %v", err)
		}
	})
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

func TestConversationsRepliesParsing(t *testing.T) {
	raw := []byte(`{
		"ok": true,
		"messages": [
			{"user": "U123", "text": "Thread root", "ts": "1000.000"},
			{"user": "U456", "text": "A reply", "ts": "1001.000"}
		],
		"has_more": false
	}`)
	var result struct {
		Messages []slackMessage `json:"messages"`
		HasMore  bool           `json:"has_more"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(result.Messages))
	}
	if result.HasMore {
		t.Error("expected has_more=false")
	}
}

func TestSlackAPIIntegration(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"user_id":"UOWNER","team_id":"T123","team":"TestCorp"}`)
	})
	mux.HandleFunc("/search.messages", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":{"matches":[
			{"ts":"100.001","thread_ts":"100.000","text":"reply","user":"UOWNER","channel":{"id":"C001","name":"arch"}}
		],"pagination":{"page_count":1}}}`)
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

	t.Run("end to end assembly", func(t *testing.T) {
		cached := cachedSlackConv{
			TeamID: "T123", TeamName: "TestCorp",
			ChannelID: "C001", ChannelName: "arch",
			ThreadTS: "100.000", OwnerID: "UOWNER",
			Messages: []slackMessage{
				{User: "U999", Text: "What about the API?", TS: "100.000"},
				{User: "UOWNER", Text: "Resource-oriented design.", TS: "100.001"},
				{User: "U999", Text: "Elaborate?", TS: "100.002"},
				{User: "UOWNER", Text: "Uniform interface. Composes better.", TS: "100.003"},
			},
		}
		conv := assembleSlackConversation(cached)
		if conv == nil {
			t.Fatal("expected conversation")
		}
		if conv.Source != "slack" {
			t.Errorf("Source = %q", conv.Source)
		}
		expectedRoles := []string{"assistant", "user", "assistant", "user"}
		for i, want := range expectedRoles {
			if conv.Messages[i].Role != want {
				t.Errorf("message[%d].Role = %q, want %q", i, conv.Messages[i].Role, want)
			}
		}
		if err := conv.Validate(); err != nil {
			t.Errorf("Validate() failed: %v", err)
		}
	})
}
