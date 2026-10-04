package github

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// ETagCacheTransport conditionally revalidates successful GET responses. It
// never serves stale data: an entry is used only after GitHub replies 304.
type ETagCacheTransport struct {
	base                        http.RoundTripper
	mu                          sync.Mutex //nolint:forbidigo // Guards entries only; it is a leaf lock with no lock ordering.
	entries                     map[string]etagCacheEntry
	bytes                       int
	hits, revalidations, stores atomic.Int64
}

type etagCacheEntry struct {
	body   []byte
	header http.Header
	etag   string
}

func NewETagCacheTransport(base http.RoundTripper) *ETagCacheTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &ETagCacheTransport{base: base, entries: make(map[string]etagCacheEntry)}
}

func (t *ETagCacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()
	t.mu.Lock()
	entry, cached := t.entries[key]
	t.mu.Unlock()
	if cached {
		t.revalidations.Add(1)
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.StatusCode == http.StatusNotModified && cached {
		t.hits.Add(1)
		log.WithField("host", req.URL.Host).Debug("GitHub ETag cache hit")
		_ = resp.Body.Close()
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: entry.header.Clone(), Body: io.NopCloser(bytes.NewReader(entry.body)), Request: req}, nil
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
		return resp, nil
	}
	originalBody := resp.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, (1<<20)+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if len(body) > 1<<20 {
		resp.Body = &cachedPrefixBody{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		return resp, nil
	}
	_ = originalBody.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.mu.Lock()
	if len(t.entries) >= 128 || t.bytes+len(body) > 16<<20 {
		t.entries = make(map[string]etagCacheEntry)
		t.bytes = 0
	}
	t.bytes -= len(t.entries[key].body)
	t.bytes += len(body)
	t.entries[key] = etagCacheEntry{body: body, header: resp.Header.Clone(), etag: resp.Header.Get("ETag")}
	t.stores.Add(1)
	t.mu.Unlock()
	return resp, nil
}

type ETagCacheStats struct{ Hits, Revalidations, Stores int64 }

func (t *ETagCacheTransport) Stats() ETagCacheStats {
	return ETagCacheStats{Hits: t.hits.Load(), Revalidations: t.revalidations.Load(), Stores: t.stores.Load()}
}

// cachedPrefixBody preserves streaming and closure for responses too large to cache.
type cachedPrefixBody struct {
	io.Reader
	io.Closer
}
