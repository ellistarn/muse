package conversation

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHTTPCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "https://api.github.com/repos/octocat/hello/issues/1/comments", nil)
	req.Header.Set("Accept", "application/json")

	// Miss — no cached entry
	if entry := cache.get(req); entry != nil {
		t.Fatal("expected cache miss on first request")
	}

	// Store an entry
	cache.put(req, &httpCacheEntry{
		ETag:   `"abc123"`,
		Status: 200,
		Headers: http.Header{
			"Link":         []string{`<https://api.github.com/repos/octocat/hello/issues/1/comments?page=2>; rel="next"`},
			"Content-Type": []string{"application/json"},
		},
		Body: []byte(`[{"id": 1}]`),
	})

	// Hit — entry exists
	entry := cache.get(req)
	if entry == nil {
		t.Fatal("expected cache hit")
	}
	if entry.ETag != `"abc123"` {
		t.Errorf("expected etag %q, got %q", `"abc123"`, entry.ETag)
	}
	if string(entry.Body) != `[{"id": 1}]` {
		t.Errorf("unexpected body: %s", entry.Body)
	}

	// Headers preserved
	if link := entry.Headers.Get("Link"); !strings.Contains(link, "page=2") {
		t.Errorf("Link header not preserved: %s", link)
	}
}

func TestHTTPCache_WrapRequest(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "https://api.github.com/repos/octocat/hello/pulls/5/comments", nil)

	// No cache — request unchanged
	wrapped := cache.wrapRequest(req)
	if wrapped.Header.Get("If-None-Match") != "" {
		t.Error("expected no If-None-Match on uncached request")
	}

	// Cache an entry
	cache.put(req, &httpCacheEntry{
		ETag:   `W/"etag456"`,
		Status: 200,
		Body:   []byte(`{}`),
	})

	// Cached — If-None-Match added
	wrapped = cache.wrapRequest(req)
	if got := wrapped.Header.Get("If-None-Match"); got != `W/"etag456"` {
		t.Errorf("expected If-None-Match %q, got %q", `W/"etag456"`, got)
	}

	// Original request not mutated
	if req.Header.Get("If-None-Match") != "" {
		t.Error("original request was mutated")
	}
}

func TestHTTPCache_HandleResponse304(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "https://api.github.com/repos/owner/repo/issues", nil)
	cache.put(req, &httpCacheEntry{
		ETag:    `"cached"`,
		Status:  200,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`[{"number":1}]`),
	})

	// Simulate 304 response
	resp304 := &http.Response{
		StatusCode: http.StatusNotModified,
		Body:       io.NopCloser(strings.NewReader("")),
	}

	result := cache.handleResponse(req, resp304)
	if result.StatusCode != 200 {
		t.Errorf("expected status 200 from cache, got %d", result.StatusCode)
	}
	body, _ := io.ReadAll(result.Body)
	if string(body) != `[{"number":1}]` {
		t.Errorf("unexpected body from cache: %s", body)
	}
}

func TestHTTPCache_HandleResponse200(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "https://api.github.com/repos/owner/repo/pulls", nil)

	// 200 response with ETag gets cached
	resp200 := &http.Response{
		StatusCode: 200,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`[{"number":2}]`)),
	}
	resp200.Header.Set("ETag", `"new-etag"`)
	resp200.Header.Set("Link", `<url>; rel="next"`)
	resp200.Header.Set("Content-Type", "application/json")

	result := cache.handleResponse(req, resp200)
	body, _ := io.ReadAll(result.Body)
	if string(body) != `[{"number":2}]` {
		t.Errorf("body altered: %s", body)
	}

	// Verify it was cached
	entry := cache.get(req)
	if entry == nil {
		t.Fatal("expected entry to be cached after 200")
	}
	if entry.ETag != `"new-etag"` {
		t.Errorf("expected etag %q, got %q", `"new-etag"`, entry.ETag)
	}
}

func TestHTTPCache_DifferentAcceptHeaders(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	url := "https://api.github.com/repos/owner/repo/issues/1"

	req1, _ := http.NewRequest("GET", url, nil)
	req1.Header.Set("Accept", "application/json")

	req2, _ := http.NewRequest("GET", url, nil)
	req2.Header.Set("Accept", "application/vnd.github.v3.raw")

	cache.put(req1, &httpCacheEntry{ETag: `"json"`, Status: 200, Body: []byte("json-body")})

	// Same URL, different Accept → different cache entry
	if entry := cache.get(req2); entry != nil {
		t.Error("expected cache miss for different Accept header")
	}

	// Same URL, same Accept → hit
	if entry := cache.get(req1); entry == nil {
		t.Error("expected cache hit for matching Accept header")
	}
}

func TestHTTPCache_Prune(t *testing.T) {
	dir := t.TempDir()
	cache, err := newHTTPCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create 5 cache entries
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", "https://api.github.com/test/"+string(rune('a'+i)), nil)
		cache.put(req, &httpCacheEntry{
			ETag:   `"etag"`,
			Status: 200,
			Body:   []byte("body"),
		})
	}

	// Prune to 3
	cache.prune(3)

	// Count remaining files
	var remaining int
	httpDir := cache.dir
	entries, _ := os.ReadDir(httpDir)
	for _, d := range entries {
		if d.IsDir() {
			subEntries, _ := os.ReadDir(httpDir + "/" + d.Name())
			remaining += len(subEntries)
		}
	}
	if remaining != 3 {
		t.Errorf("expected 3 entries after prune, got %d", remaining)
	}
}
