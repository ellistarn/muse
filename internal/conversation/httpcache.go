package conversation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// httpCache is a filesystem-backed HTTP response cache keyed by (URL, Accept header).
// It stores ETags and response bodies so subsequent requests can use If-None-Match.
// GitHub API responses with 304 Not Modified don't count against rate limits,
// making this a significant optimization for incremental syncs.
type httpCache struct {
	dir string
}

// httpCacheEntry stores the cached response for a single request.
type httpCacheEntry struct {
	ETag    string      `json:"etag"`
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

func newHTTPCache(dir string) (*httpCache, error) {
	cacheDir := filepath.Join(dir, "http")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("http cache dir: %w", err)
	}
	return &httpCache{dir: cacheDir}, nil
}

// cacheKey returns a stable hash for a request. Keyed on URL and Accept header
// since GitHub returns different response shapes for different media types.
// Key does not include auth token — assumes single token per process lifetime.
// If multi-token support is added, include a token hash in the key.
func (c *httpCache) cacheKey(req *http.Request) string {
	h := sha256.New()
	h.Write([]byte(req.URL.String()))
	h.Write([]byte("\x00"))
	h.Write([]byte(req.Header.Get("Accept")))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func (c *httpCache) path(key string) string {
	// Two-level directory to avoid too many files in one dir
	return filepath.Join(c.dir, key[:2], key+".json")
}

// get retrieves a cached entry for the request, or nil if not cached.
func (c *httpCache) get(req *http.Request) *httpCacheEntry {
	key := c.cacheKey(req)
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil
	}
	var entry httpCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil
	}
	return &entry
}

// put stores a response in the cache.
func (c *httpCache) put(req *http.Request, entry *httpCacheEntry) {
	key := c.cacheKey(req)
	path := c.path(key)
	os.MkdirAll(filepath.Dir(path), 0o755)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0o644)
}

// wrapRequest adds If-None-Match to the request if we have a cached ETag.
func (c *httpCache) wrapRequest(req *http.Request) *http.Request {
	entry := c.get(req)
	if entry == nil || entry.ETag == "" {
		return req
	}
	// Clone the request to avoid mutating the original
	clone := req.Clone(req.Context())
	clone.Header.Set("If-None-Match", entry.ETag)
	return clone
}

// handleResponse processes a response. On 304, reconstructs the cached response.
// On 200 with ETag, caches the response. Returns the response to use.
func (c *httpCache) handleResponse(req *http.Request, resp *http.Response) *http.Response {
	if resp.StatusCode == http.StatusNotModified {
		entry := c.get(req)
		if entry != nil {
			resp.Body.Close()
			return c.reconstruct(entry)
		}
		// 304 but no cache — shouldn't happen, return as-is
		return resp
	}

	// Cache responses with ETags
	etag := resp.Header.Get("ETag")
	if etag == "" || resp.StatusCode != http.StatusOK {
		return resp
	}

	// Read the body, cache it, and replace with a new reader
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp
	}

	// Cache headers that matter for pagination and API behavior
	cachedHeaders := make(http.Header)
	for _, key := range []string{"Link", "Content-Type"} {
		if v := resp.Header.Get(key); v != "" {
			cachedHeaders.Set(key, v)
		}
	}

	c.put(req, &httpCacheEntry{
		ETag:    etag,
		Status:  resp.StatusCode,
		Headers: cachedHeaders,
		Body:    body,
	})

	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

// reconstruct builds an http.Response from a cached entry.
func (c *httpCache) reconstruct(entry *httpCacheEntry) *http.Response {
	resp := &http.Response{
		StatusCode: entry.Status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(entry.Body)),
	}
	for key, vals := range entry.Headers {
		for _, v := range vals {
			resp.Header.Add(key, v)
		}
	}
	return resp
}

// prune removes cache entries exceeding maxEntries, deleting oldest first.
func (c *httpCache) prune(maxEntries int) {
	type fileInfo struct {
		path    string
		modTime int64
	}
	var files []fileInfo
	filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		files = append(files, fileInfo{path: path, modTime: info.ModTime().UnixNano()})
		return nil
	})

	if len(files) <= maxEntries {
		return
	}

	// Sort oldest first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	// Remove oldest until we're at maxEntries
	for i := 0; i < len(files)-maxEntries; i++ {
		os.Remove(files[i].path)
	}
}
