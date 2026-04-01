package conversation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		url       string
		wantOwner string
		wantRepo  string
	}{
		{"https://api.github.com/repos/octocat/hello-world", "octocat", "hello-world"},
		{"https://api.github.com/repos/org/repo-name", "org", "repo-name"},
		{"short", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		owner, repo := parseRepoURL(tt.url)
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("parseRepoURL(%q) = (%q, %q), want (%q, %q)",
				tt.url, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestFormatGitHubReviewComment(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		diffHunk string
		body     string
		want     string
	}{
		{
			name: "body only",
			body: "looks good",
			want: "looks good",
		},
		{
			name: "with path",
			path: "cmd/main.go",
			body: "needs error handling",
			want: "[cmd/main.go]\nneeds error handling",
		},
		{
			name:     "with path and short hunk",
			path:     "pkg/server.go",
			diffHunk: "@@ -10,3 +10,4 @@\n func Start() {\n+    log.Println(\"starting\")\n }",
			body:     "add context to the log",
			want:     "[pkg/server.go]\n@@ -10,3 +10,4 @@\n func Start() {\n+    log.Println(\"starting\")\n }\n\nadd context to the log",
		},
		{
			name:     "long hunk truncated",
			path:     "file.go",
			diffHunk: "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10",
			body:     "comment",
			want:     "[file.go]\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n\ncomment",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatGitHubReviewComment(tt.path, tt.diffHunk, tt.body)
			if got != tt.want {
				t.Errorf("got:\n%s\n\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestAssembleGitHubConversation(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("filters threads with less than 2 owner messages", func(t *testing.T) {
		messages := []githubMessage{
			{Author: "other", Body: "please review", CreatedAt: base},
			{Author: "owner", Body: "lgtm", CreatedAt: base.Add(time.Minute)},
			{Author: "other", Body: "thanks", CreatedAt: base.Add(2 * time.Minute)},
		}
		conv := assembleGitHubConversation("owner", "github-prs", "org", "repo", 1, true, "Fix bug",
			base, base.Add(time.Hour), messages)
		if conv != nil {
			t.Error("expected nil for thread with <2 owner messages")
		}
	})

	t.Run("builds conversation with sufficient participation", func(t *testing.T) {
		messages := []githubMessage{
			{Author: "owner", Body: "here's my PR", CreatedAt: base},
			{Author: "reviewer", Body: "needs tests", CreatedAt: base.Add(time.Hour)},
			{Author: "owner", Body: "added tests", CreatedAt: base.Add(2 * time.Hour)},
			{Author: "reviewer", Body: "lgtm", CreatedAt: base.Add(3 * time.Hour)},
		}
		conv := assembleGitHubConversation("owner", "github-prs", "org", "repo", 42, true, "Add feature",
			base, base.Add(3*time.Hour), messages)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		if conv.Source != "github-prs" {
			t.Errorf("source = %q, want %q", conv.Source, "github-prs")
		}
		if conv.ConversationID != "org/repo/pull/42" {
			t.Errorf("conversationID = %q, want %q", conv.ConversationID, "org/repo/pull/42")
		}
		if conv.Title != "Add feature" {
			t.Errorf("title = %q, want %q", conv.Title, "Add feature")
		}
		if conv.Project != "org/repo" {
			t.Errorf("project = %q, want %q", conv.Project, "org/repo")
		}
		if len(conv.Messages) != 4 {
			t.Fatalf("expected 4 messages, got %d", len(conv.Messages))
		}
		if conv.Messages[0].Role != "user" {
			t.Errorf("owner message role = %q, want %q", conv.Messages[0].Role, "user")
		}
		if conv.Messages[0].Content != "here's my PR" {
			t.Errorf("owner message should not have prefix, got %q", conv.Messages[0].Content)
		}
		if conv.Messages[1].Role != "assistant" {
			t.Errorf("reviewer message role = %q, want %q", conv.Messages[1].Role, "assistant")
		}
		if !strings.HasPrefix(conv.Messages[1].Content, "[GitHub comment by @reviewer]") {
			t.Errorf("reviewer message should have prefix, got %q", conv.Messages[1].Content)
		}
	})

	t.Run("issues use issues path not pull", func(t *testing.T) {
		messages := []githubMessage{
			{Author: "owner", Body: "found a bug", CreatedAt: base},
			{Author: "other", Body: "can reproduce", CreatedAt: base.Add(time.Hour)},
			{Author: "owner", Body: "here's a fix", CreatedAt: base.Add(2 * time.Hour)},
		}
		conv := assembleGitHubConversation("owner", "github-issues", "org", "repo", 10, false, "Bug report",
			base, base.Add(2*time.Hour), messages)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		if conv.ConversationID != "org/repo/issues/10" {
			t.Errorf("conversationID = %q, want %q", conv.ConversationID, "org/repo/issues/10")
		}
	})

	t.Run("messages sorted chronologically", func(t *testing.T) {
		messages := []githubMessage{
			{Author: "owner", Body: "third", CreatedAt: base.Add(3 * time.Minute)},
			{Author: "owner", Body: "first", CreatedAt: base},
			{Author: "other", Body: "second", CreatedAt: base.Add(time.Minute)},
		}
		conv := assembleGitHubConversation("owner", "github-issues", "org", "repo", 1, false, "Discussion",
			base, base.Add(3*time.Minute), messages)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		if conv.Messages[0].Content != "first" {
			t.Errorf("first message = %q, want %q", conv.Messages[0].Content, "first")
		}
	})

	t.Run("case insensitive username matching", func(t *testing.T) {
		messages := []githubMessage{
			{Author: "Owner", Body: "msg1", CreatedAt: base},
			{Author: "OWNER", Body: "msg2", CreatedAt: base.Add(time.Minute)},
			{Author: "other", Body: "msg3", CreatedAt: base.Add(2 * time.Minute)},
		}
		conv := assembleGitHubConversation("owner", "github-issues", "org", "repo", 1, false, "Test",
			base, base.Add(2*time.Minute), messages)
		if conv == nil {
			t.Fatal("expected non-nil conversation (case-insensitive match)")
		}
		for i, m := range conv.Messages {
			if i < 2 && m.Role != "user" {
				t.Errorf("message %d role = %q, want %q (case-insensitive)", i, m.Role, "user")
			}
		}
	})
}

func TestAssembleCachedConversation(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("formats review comments from cache", func(t *testing.T) {
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 5, IsPR: true,
			Title: "PR with review", Author: "owner", Body: "my pr",
			CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour),
			Messages: []cachedMessage{
				{Author: "reviewer", Body: "fix this", CreatedAt: base.Add(time.Hour),
					Path: "main.go", DiffHunk: "+ bad code"},
				{Author: "owner", Body: "fixed", CreatedAt: base.Add(2 * time.Hour)},
				{Author: "owner", Body: "anything else?", CreatedAt: base.Add(3 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-prs", thread)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		// Review comment should have path formatting
		found := false
		for _, m := range conv.Messages {
			if strings.Contains(m.Content, "[main.go]") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected review comment to include file path formatting")
		}
	})

	t.Run("formats review state from cache", func(t *testing.T) {
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 6, IsPR: true,
			Title: "PR with approval", Author: "owner", Body: "my pr",
			CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour),
			Messages: []cachedMessage{
				{Author: "reviewer", Body: "looks good", CreatedAt: base.Add(time.Hour),
					ReviewState: "APPROVED"},
				{Author: "owner", Body: "thanks", CreatedAt: base.Add(2 * time.Hour)},
				{Author: "owner", Body: "merging", CreatedAt: base.Add(3 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-prs", thread)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		found := false
		for _, m := range conv.Messages {
			if strings.Contains(m.Content, "[review: approved]") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected review message to include state")
		}
	})

	t.Run("PR body annotated as auto-generated", func(t *testing.T) {
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 7, IsPR: true,
			Title: "PR with body", Author: "owner", Body: "## Summary\nAdded feature X",
			CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour),
			Messages: []cachedMessage{
				{Author: "owner", Body: "ready for review", CreatedAt: base.Add(time.Hour)},
				{Author: "reviewer", Body: "lgtm", CreatedAt: base.Add(2 * time.Hour)},
				{Author: "owner", Body: "thanks", CreatedAt: base.Add(3 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-prs", thread)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		// First message should be annotated PR body
		first := conv.Messages[0]
		if !strings.HasPrefix(first.Content, "[Auto-generated PR description") {
			t.Errorf("PR body should be annotated, got %q", first.Content[:min(80, len(first.Content))])
		}
		if !strings.Contains(first.Content, "## Summary\nAdded feature X") {
			t.Error("PR body content should be preserved after annotation")
		}
		// Body should have user role (owner authored the PR)
		if first.Role != "user" {
			t.Errorf("owner-authored PR body role = %q, want %q", first.Role, "user")
		}
	})

	t.Run("PR body excluded from owner threshold", func(t *testing.T) {
		// Owner wrote the PR body + 1 comment = only 1 real owner message
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 8, IsPR: true,
			Title: "Thin PR", Author: "owner", Body: "auto-generated description",
			CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour),
			Messages: []cachedMessage{
				{Author: "reviewer", Body: "looks fine", CreatedAt: base.Add(time.Hour)},
				{Author: "owner", Body: "thanks", CreatedAt: base.Add(2 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-prs", thread)
		if conv != nil {
			t.Error("expected nil: PR body should not count toward 2+ owner threshold")
		}
	})

	t.Run("issue body unchanged and counts toward threshold", func(t *testing.T) {
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 9, IsPR: false,
			Title: "Bug report", Author: "owner", Body: "Found a bug in X",
			CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour),
			Messages: []cachedMessage{
				{Author: "reviewer", Body: "can reproduce", CreatedAt: base.Add(time.Hour)},
				{Author: "owner", Body: "here's a fix", CreatedAt: base.Add(2 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-issues", thread)
		if conv == nil {
			t.Fatal("expected non-nil: issue body should count toward threshold")
		}
		// Issue body should NOT be annotated
		first := conv.Messages[0]
		if strings.Contains(first.Content, "Auto-generated") {
			t.Error("issue body should not have auto-generated annotation")
		}
		if first.Content != "Found a bug in X" {
			t.Errorf("issue body = %q, want %q", first.Content, "Found a bug in X")
		}
	})

	t.Run("PR body by non-owner gets attribution prefix", func(t *testing.T) {
		thread := cachedThread{
			Owner: "org", Repo: "repo", Number: 10, IsPR: true,
			Title: "Someone else's PR", Author: "other-dev", Body: "their description",
			CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour),
			Messages: []cachedMessage{
				{Author: "owner", Body: "review comment 1", CreatedAt: base.Add(time.Hour)},
				{Author: "other-dev", Body: "updated", CreatedAt: base.Add(2 * time.Hour)},
				{Author: "owner", Body: "review comment 2", CreatedAt: base.Add(3 * time.Hour)},
			},
		}
		conv := assembleCachedConversation("owner", "github-prs", thread)
		if conv == nil {
			t.Fatal("expected non-nil conversation")
		}
		first := conv.Messages[0]
		if first.Role != "assistant" {
			t.Errorf("non-owner PR body role = %q, want %q", first.Role, "assistant")
		}
		if !strings.Contains(first.Content, "[GitHub comment by @other-dev]") {
			t.Error("non-owner PR body should have attribution prefix")
		}
		if !strings.Contains(first.Content, "[Auto-generated PR description") {
			t.Error("non-owner PR body should still have auto-generated annotation")
		}
	})
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	thread := &cachedThread{
		Owner: "org", Repo: "repo", Number: 42, IsPR: true,
		Title:     "Test PR",
		Body:      "description",
		Author:    "testuser",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		Messages: []cachedMessage{
			{Author: "other", Body: "comment", CreatedAt: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)},
		},
	}

	if err := saveCachedThread(dir, thread); err != nil {
		t.Fatalf("save: %v", err)
	}

	path := threadCachePath(dir, "org", "repo", 42, true)
	loaded, err := loadCachedThread(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.Owner != thread.Owner || loaded.Number != thread.Number {
		t.Errorf("owner/number mismatch: got %s/%d, want %s/%d",
			loaded.Owner, loaded.Number, thread.Owner, thread.Number)
	}
	if len(loaded.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded.Messages))
	}
}

func TestLoadAllCachedThreads(t *testing.T) {
	dir := t.TempDir()

	threads := []*cachedThread{
		{Owner: "a", Repo: "b", Number: 1, IsPR: true, Title: "PR1",
			CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{Owner: "a", Repo: "b", Number: 2, IsPR: false, Title: "Issue2",
			CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{Owner: "c", Repo: "d", Number: 3, IsPR: true, Title: "PR3",
			CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for _, th := range threads {
		saveCachedThread(dir, th)
	}

	loaded, err := loadAllCachedThreads(dir)
	if err != nil {
		t.Fatalf("loadAll: %v", err)
	}
	if len(loaded) != 3 {
		t.Errorf("expected 3 threads, got %d", len(loaded))
	}
}

func TestSyncStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := githubSyncState{
		LastSync: time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
		Username: "testuser",
	}
	saveGitHubSyncState(dir, "pr", state)

	loaded := loadGitHubSyncState(dir, "pr")
	if loaded.Username != "testuser" {
		t.Errorf("username = %q, want %q", loaded.Username, "testuser")
	}
	if !loaded.LastSync.Equal(state.LastSync) {
		t.Errorf("lastSync = %v, want %v", loaded.LastSync, state.LastSync)
	}
}

func TestSyncStateEmpty(t *testing.T) {
	dir := t.TempDir()
	state := loadGitHubSyncState(dir, "issue")
	if !state.LastSync.IsZero() {
		t.Errorf("expected zero LastSync, got %v", state.LastSync)
	}
}

func TestCacheSkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a valid thread
	saveCachedThread(dir, &cachedThread{
		Owner: "a", Repo: "b", Number: 1, IsPR: true, Title: "valid",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	// Write a corrupt file
	corruptDir := filepath.Join(dir, "threads", "x", "y", "pull")
	os.MkdirAll(corruptDir, 0o755)
	os.WriteFile(filepath.Join(corruptDir, "99.json"), []byte("not json"), 0o644)

	loaded, err := loadAllCachedThreads(dir)
	if err != nil {
		t.Fatalf("loadAll: %v", err)
	}
	if len(loaded) != 1 {
		t.Errorf("expected 1 valid thread (corrupt skipped), got %d", len(loaded))
	}
}

func TestGitHub_NoTokenReturnsNil(t *testing.T) {
	t.Setenv("MUSE_GITHUB_TOKEN", "")
	t.Setenv("PATH", "")

	g := &GitHub{kind: "issue", source: "github-issues", displayName: "GitHub Issues"}
	convs, err := g.Conversations(context.Background(), func(SyncProgress) {})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if convs != nil {
		t.Errorf("expected nil conversations, got %d", len(convs))
	}
}

func TestIsGitHubBot(t *testing.T) {
	bots := []string{"dependabot[bot]", "stale[bot]", "k8s-ci-robot", "K8s-CI-Robot", "googlebot", "codecov"}
	for _, b := range bots {
		if !isGitHubBot(b) {
			t.Errorf("expected %q to be detected as bot", b)
		}
	}
	humans := []string{"ellistarn", "reviewer", "some-person"}
	for _, h := range humans {
		if isGitHubBot(h) {
			t.Errorf("expected %q to NOT be detected as bot", h)
		}
	}
}

func TestIsGitHubNoise(t *testing.T) {
	noise := []string{"/retest", "/lgtm", "/approve", "/test all", "/hold", "  /retest  ", ""}
	for _, n := range noise {
		if !isGitHubNoise(n) {
			t.Errorf("expected %q to be detected as noise", n)
		}
	}
	signal := []string{
		"This looks good, but needs tests",
		"/me thinks this is great", // not a prow command
		"I ran /retest locally and it passed",
		"line1\n/retest", // multi-line, not a pure command
	}
	for _, s := range signal {
		if isGitHubNoise(s) {
			t.Errorf("expected %q to NOT be detected as noise", s)
		}
	}
}

func TestAssembleCachedConversation_FiltersBots(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	thread := cachedThread{
		Owner: "org", Repo: "repo", Number: 1, IsPR: true,
		Title: "PR", Author: "owner", Body: "my pr",
		CreatedAt: base, UpdatedAt: base.Add(5 * time.Hour),
		Messages: []cachedMessage{
			{Author: "reviewer", Body: "needs work", CreatedAt: base.Add(time.Hour)},
			{Author: "k8s-ci-robot", Body: "CI passed", CreatedAt: base.Add(2 * time.Hour)},
			{Author: "owner", Body: "fixed", CreatedAt: base.Add(3 * time.Hour)},
			{Author: "dependabot[bot]", Body: "bump deps", CreatedAt: base.Add(4 * time.Hour)},
			{Author: "owner", Body: "done", CreatedAt: base.Add(5 * time.Hour)},
		},
	}
	conv := assembleCachedConversation("owner", "github-prs", thread)
	if conv == nil {
		t.Fatal("expected non-nil conversation")
	}
	// Should have 4 messages: annotated body + reviewer + 2 owner replies (bots filtered)
	if len(conv.Messages) != 4 {
		t.Errorf("expected 4 messages (bots filtered), got %d", len(conv.Messages))
		for i, m := range conv.Messages {
			t.Logf("  msg %d: role=%s content=%q", i, m.Role, m.Content[:min(50, len(m.Content))])
		}
	}
}

func TestAssembleCachedConversation_FiltersProwCommands(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	thread := cachedThread{
		Owner: "org", Repo: "repo", Number: 1, IsPR: true,
		Title: "PR", Author: "owner", Body: "my pr",
		CreatedAt: base, UpdatedAt: base.Add(5 * time.Hour),
		Messages: []cachedMessage{
			{Author: "reviewer", Body: "needs tests", CreatedAt: base.Add(time.Hour)},
			{Author: "reviewer", Body: "/lgtm", CreatedAt: base.Add(2 * time.Hour)},
			{Author: "owner", Body: "/retest", CreatedAt: base.Add(3 * time.Hour)},
			{Author: "owner", Body: "added tests", CreatedAt: base.Add(4 * time.Hour)},
			{Author: "owner", Body: "ready for review", CreatedAt: base.Add(5 * time.Hour)},
		},
	}
	conv := assembleCachedConversation("owner", "github-prs", thread)
	if conv == nil {
		t.Fatal("expected non-nil conversation")
	}
	// Should have 4 messages: annotated body + "needs tests" + "added tests" + "ready for review" (prow filtered)
	if len(conv.Messages) != 4 {
		t.Errorf("expected 4 messages (prow filtered), got %d", len(conv.Messages))
		for i, m := range conv.Messages {
			t.Logf("  msg %d: role=%s content=%q", i, m.Role, m.Content[:min(50, len(m.Content))])
		}
	}
}
