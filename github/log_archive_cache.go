package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	maxArchiveBytes      = 64 * 1024 * 1024
	maxArchiveCacheBytes = 128 * 1024 * 1024
)

type archiveCacheKey struct {
	client     *Client
	runID      int64
	attempt    int
	artifactID int64
}
type archiveCacheValue struct {
	data    []byte
	touched time.Time
}

var runArchiveCache = struct {
	sync.Mutex //nolint:forbidigo // Guards archive entries and total bytes only; no nested locks.
	entries    map[archiveCacheKey]archiveCacheValue
	bytes      int
}{entries: make(map[archiveCacheKey]archiveCacheValue)}

// cachedRunArchive revalidates run identity before using immutable completed-run
// evidence. A rerun changes the attempt key; active runs are never cached.
func (c *Client) cachedRunArchive(ctx context.Context, runID int64) (*zip.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, _, runErr := c.gh.Actions.GetWorkflowRunByID(ctx, c.owner, c.repo, runID)
	key := archiveCacheKey{client: c, runID: runID}
	cacheable := runErr == nil && run.GetStatus() == "completed" && run.GetRunAttempt() > 0
	if cacheable {
		key.attempt = run.GetRunAttempt()
		if data, ok := lookupArchiveData(key); ok {
			return zip.NewReader(bytes.NewReader(data), int64(len(data)))
		}
	}
	attempt := 0
	if runErr == nil {
		attempt = run.GetRunAttempt()
	}
	downloadURL, err := c.runArchiveURL(ctx, runID, attempt)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch ZIP: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("log archive exceeds %d-byte download limit", maxArchiveBytes)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if cacheable {
		storeRunArchive(key, data)
	}

	return archive, nil
}

func storeRunArchive(key archiveCacheKey, data []byte) {
	runArchiveCache.Lock()
	defer runArchiveCache.Unlock()
	if old, ok := runArchiveCache.entries[key]; ok {
		runArchiveCache.bytes -= len(old.data)
		delete(runArchiveCache.entries, key)
	}
	for runArchiveCache.bytes+len(data) > maxArchiveCacheBytes || len(runArchiveCache.entries) >= 8 {
		evictOldestArchive()
	}
	runArchiveCache.entries[key] = archiveCacheValue{data: data, touched: time.Now()}
	runArchiveCache.bytes += len(data)
}

func evictOldestArchive() {
	var oldest archiveCacheKey
	var when time.Time
	for k, v := range runArchiveCache.entries {
		if when.IsZero() || v.touched.Before(when) {
			oldest = k
			when = v.touched
		}
	}
	runArchiveCache.bytes -= len(runArchiveCache.entries[oldest].data)
	delete(runArchiveCache.entries, oldest)
}

// lookupArchiveData only returns data for the exact Client instance. A client
// owns an immutable transport/credential scope; archives never cross it.
func lookupArchiveData(key archiveCacheKey) ([]byte, bool) {
	runArchiveCache.Lock()
	defer runArchiveCache.Unlock()
	value, ok := runArchiveCache.entries[key]
	if ok {
		value.touched = time.Now()
		runArchiveCache.entries[key] = value
	}
	return value.data, ok
}

// Pin the archive to the same attempt used for its cache key. A rerun may begin
// between metadata inspection and fetching logs from the download endpoint.
func (c *Client) runArchiveURL(ctx context.Context, runID int64, attempt int) (*url.URL, error) {
	if attempt > 0 {
		location, _, err := c.gh.Actions.GetWorkflowRunAttemptLogs(ctx, c.owner, c.repo, runID, attempt, maxRedirects)
		return location, err
	}
	location, _, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	return location, err
}
