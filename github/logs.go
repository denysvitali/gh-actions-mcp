package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ansiCSI matches ECMA-48 SGR / CSI sequences used by GitHub Actions to
// colour bash -x dumps (`\x1b[36;1m…\x1b[0m`). Stripped on ingest so every
// consumer (search, diagnose, sections) sees the same plain text.
var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// logSizeThreshold is the archive size above which the download is spooled to a
// temporary file instead of being buffered in memory. An unknown content length
// also takes the temp-file path, since it could be arbitrarily large.
const logSizeThreshold = 10 * 1024 * 1024 // 10MB

// maxLogFileSize caps a single log entry. Larger entries inside a run archive are
// skipped entirely; a job log payload is truncated to this length.
const maxLogFileSize = 50 * 1024 * 1024 // 50MB per file

// LogViewOptions selects which part of a log to return.
//
// The three line counts are applied in this order: Offset skips leading lines,
// then Tail wins over Head if both are set. Line numbers count the
// "=== file ===" headers too, unless NoHeaders suppresses them. Filter, when
// active, is applied before any of the line windowing.
//
// A zero value returns the whole log with headers.
type LogViewOptions struct {
	// Head returns at most N lines from the start (after Offset).
	Head int
	// Tail returns the last N lines and takes precedence over Head and Offset.
	Tail int
	// Offset skips the first N lines (0-based).
	Offset int
	// NoHeaders omits the "=== filename ===" separator between archive entries.
	NoHeaders bool
	// Filter narrows the lines before windowing. Nil means no filtering.
	Filter *LogFilterOptions
}

// LogFileInfo describes one entry in a workflow run's log archive.
type LogFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// logFile is one archive entry's name and decoded content.
type logFile struct {
	name string
	data string
}

// readZipArchive downloads the ZIP at zipURL and returns its entries sorted by
// name, along with the number of bytes downloaded.
//
// Archives above logSizeThreshold (or of unknown length) are spooled through a
// temporary file so a large run archive never has to fit in memory. Entries that
// cannot be opened or read are logged and skipped rather than failing the call.
func readZipArchive(zipURL string, httpClient *http.Client) ([]logFile, int64, error) {
	//nolint:noctx // The presigned client has a hard timeout and this compatibility helper has no context parameter.
	zipResp, err := httpClient.Get(zipURL)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: HTTP %d", zipResp.StatusCode)
	}

	zipReader, size, cleanup, err := openZipFromResponse(zipResp)
	if err != nil {
		return nil, 0, err
	}
	defer cleanup()

	return logFilesFromZip(zipReader), size, nil
}

// openZipFromResponse opens the response body as a ZIP archive, buffering small
// payloads in memory and spooling large or unbounded ones to disk. The returned
// cleanup must be called once the reader is no longer used; it is never nil on
// success.
func openZipFromResponse(resp *http.Response) (*zip.Reader, int64, func(), error) {
	if resp.ContentLength >= 0 && resp.ContentLength <= logSizeThreshold {
		return openZipInMemory(resp.Body)
	}
	return openZipViaTempFile(resp.Body)
}

// openZipInMemory buffers the whole body and opens it as a ZIP.
func openZipInMemory(body io.Reader) (*zip.Reader, int64, func(), error) {
	zipData, err := io.ReadAll(body)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to read ZIP: %w", err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to open ZIP: %w", err)
	}
	return zipReader, int64(len(zipData)), func() {}, nil
}

// openZipViaTempFile spools the body to a temporary file and opens that as a ZIP.
// The temporary file is removed by the returned cleanup, or immediately on error.
func openZipViaTempFile(body io.Reader) (*zip.Reader, int64, func(), error) {
	tempFile, err := os.CreateTemp("", "logs-*.zip")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	cleanup := func() {
		tempFile.Close()
		os.Remove(tempFile.Name())
	}

	written, err := io.Copy(tempFile, body)
	if err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("failed to write to temp file: %w", err)
	}
	if _, err := tempFile.Seek(0, 0); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("failed to seek temp file: %w", err)
	}
	zipReader, err := zip.NewReader(tempFile, written)
	if err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("failed to open ZIP: %w", err)
	}
	return zipReader, written, cleanup, nil
}

// logFilesFromZip decodes every non-directory entry, sorted by name. Entries
// larger than maxLogFileSize, or that fail to open or read, are logged and
// omitted.
func logFilesFromZip(zipReader *zip.Reader) []logFile {
	var logFiles []logFile
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > uint64(maxLogFileSize) {
			log.Debugf("Skipping large log file %s (%d bytes)", file.Name, file.UncompressedSize64)
			continue
		}

		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in ZIP: %v", file.Name, err)
			continue
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxLogFileSize))
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in ZIP: %v", file.Name, err)
			continue
		}

		logFiles = append(logFiles, logFile{name: file.Name, data: stripANSI(string(content))})
	}

	sortLogFiles(logFiles)
	return dropCombinedJobLogs(logFiles)
}

// stripANSI removes CSI/SGR sequences. Safe on text that has none.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	return ansiCSI.ReplaceAllString(s, "")
}

// dropCombinedJobLogs removes the run-archive's combined `N_Job Name.txt`
// files when per-step files for that job already exist. GitHub ships both
// and searching the archive otherwise returns every hit twice.
func dropCombinedJobLogs(files []logFile) []logFile {
	hasPerStep := make(map[string]bool, len(files))
	for _, lf := range files {
		job, _, ok := strings.Cut(lf.name, "/")
		if !ok || job == "" {
			continue
		}
		hasPerStep[job] = true
	}
	if len(hasPerStep) == 0 {
		return files
	}
	kept := make([]logFile, 0, len(files))
	for _, lf := range files {
		if strings.Contains(lf.name, "/") {
			kept = append(kept, lf)
			continue
		}
		if job := combinedJobName(lf.name); job != "" && hasPerStep[job] {
			continue
		}
		kept = append(kept, lf)
	}
	return kept
}

// combinedJobName parses "0_Flash & Soak Test (wifi).txt" into the job
// name GitHub uses as the per-step folder. Empty means "not a combined
// job file".
func combinedJobName(path string) string {
	if strings.Contains(path, "/") {
		return ""
	}
	base := strings.TrimSuffix(path, ".txt")
	underscore := strings.IndexByte(base, '_')
	if underscore < 1 || underscore == len(base)-1 {
		return ""
	}
	for _, r := range base[:underscore] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return base[underscore+1:]
}

// sortLogFiles orders entries by name so output is stable across calls.
func sortLogFiles(logFiles []logFile) {
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})
}

// formatLogFiles renders the entries into one string according to opts. It sorts
// logFiles in place.
//
// The result ends with a newline unless it is empty, and it is empty when the
// filter matched nothing or Offset ran past the end.
func formatLogFiles(logFiles []logFile, opts LogViewOptions) (string, error) {
	sortLogFiles(logFiles)

	logStr, matched, err := applyLogFilter(joinLogFiles(logFiles, opts.NoHeaders), opts.Filter)
	if err != nil {
		return "", err
	}
	if !matched {
		return "", nil
	}
	return applyLineWindow(logStr, opts), nil
}

// joinLogFiles concatenates the entries, optionally prefixing each with a
// "=== name ===" header, and trims the trailing newlines so line counting is not
// thrown off by them.
func joinLogFiles(logFiles []logFile, noHeaders bool) string {
	var allLogs strings.Builder
	for _, lf := range logFiles {
		if !noHeaders {
			fmt.Fprintf(&allLogs, "=== %s ===\n", lf.name)
		}
		allLogs.WriteString(lf.data)
		if !strings.HasSuffix(lf.data, "\n") {
			allLogs.WriteString("\n")
		}
	}
	return strings.TrimRight(allLogs.String(), "\n")
}

// applyLogFilter narrows logStr with opts. matched is false only when an active
// filter found nothing, which callers render as an empty result rather than as the
// unfiltered log.
func applyLogFilter(logStr string, opts *LogFilterOptions) (filtered string, matched bool, err error) {
	if !opts.isActive() {
		return logStr, true, nil
	}
	lines, err := filterLogLines(parseLogLines(logStr), opts)
	if err != nil {
		return "", false, err
	}
	if lines == nil {
		return "", false, nil
	}
	return linesToString(lines), true, nil
}

// applyLineWindow slices logStr to the requested Offset/Tail/Head window and
// re-appends the trailing newline.
func applyLineWindow(logStr string, opts LogViewOptions) string {
	lines := strings.Split(logStr, "\n")

	if opts.Offset > 0 {
		if opts.Offset >= len(lines) {
			lines = nil
		} else {
			lines = lines[opts.Offset:]
		}
	}

	switch {
	case opts.Tail > 0:
		if len(lines) > opts.Tail {
			lines = lines[len(lines)-opts.Tail:]
		}
	case opts.Head > 0 && len(lines) > opts.Head:
		lines = lines[:opts.Head]
	}

	result := strings.Join(lines, "\n")
	if result != "" {
		result += "\n"
	}
	return result
}

// runLogArchive downloads and decodes the log archive of a workflow run.
//
// The archive itself is fetched with presignedHTTPClient: GitHub answers the API
// call with a redirect to pre-signed storage, and those URLs reject requests that
// also carry an Authorization header.
func (c *Client) runLogArchive(ctx context.Context, runID int64) ([]logFile, error) {
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}
	return logFiles, nil
}

// GetWorkflowLogFiles lists the entries in a workflow run's log archive with their
// decoded sizes, without returning any content.
func (c *Client) GetWorkflowLogFiles(ctx context.Context, runID int64) ([]*LogFileInfo, error) {
	logFiles, err := c.runLogArchive(ctx, runID)
	if err != nil {
		return nil, err
	}

	result := make([]*LogFileInfo, 0, len(logFiles))
	for _, lf := range logFiles {
		result = append(result, &LogFileInfo{Path: lf.name, Size: int64(len(lf.data))})
	}
	return result, nil
}

// GetWorkflowLogsWithPattern returns a workflow run's logs, keeping only archive
// entries whose name matches filePattern (a filepath.Match glob; empty keeps all).
//
// See LogViewOptions for how head, tail, offset, noHeaders and filterOpts
// interact. A pattern that matches nothing yields an empty string, not an error;
// an invalid pattern is an error.
func (c *Client) GetWorkflowLogsWithPattern(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filePattern string, filterOpts *LogFilterOptions) (string, error) {
	return c.workflowLogs(ctx, runID, filePattern, LogViewOptions{
		Head: head, Tail: tail, Offset: offset, NoHeaders: noHeaders, Filter: filterOpts,
	})
}

// GetWorkflowLogs returns all of a workflow run's logs.
//
// See LogViewOptions for how head, tail, offset, noHeaders and filterOpts
// interact.
func (c *Client) GetWorkflowLogs(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	return c.workflowLogs(ctx, runID, "", LogViewOptions{
		Head: head, Tail: tail, Offset: offset, NoHeaders: noHeaders, Filter: filterOpts,
	})
}

// workflowLogs is the single implementation behind GetWorkflowLogs and
// GetWorkflowLogsWithPattern.
func (c *Client) workflowLogs(ctx context.Context, runID int64, filePattern string, opts LogViewOptions) (string, error) {
	logFiles, err := c.runLogArchive(ctx, runID)
	if err != nil {
		return "", err
	}

	logFiles, err = matchLogFiles(logFiles, filePattern)
	if err != nil {
		return "", err
	}
	return formatLogFiles(logFiles, opts)
}

// matchLogFiles keeps the entries matching pattern. An empty pattern keeps
// everything; an invalid pattern is an error.
func matchLogFiles(logFiles []logFile, pattern string) ([]logFile, error) {
	if pattern == "" {
		return logFiles, nil
	}
	filtered := make([]logFile, 0, len(logFiles))
	for _, lf := range logFiles {
		matched, err := filepath.Match(pattern, lf.name)
		if err != nil {
			return nil, fmt.Errorf("invalid file pattern %q: %w", pattern, err)
		}
		if matched {
			filtered = append(filtered, lf)
		}
	}
	return filtered, nil
}

// GetWorkflowJobLogs returns the logs of a single job.
//
// See LogViewOptions for how head, tail, offset, noHeaders and filterOpts
// interact. GitHub may answer with either a ZIP or plain text; both are handled.
// When GitHub returns 404 for a job that does exist, try
// GetWorkflowJobLogsFromRunArchive instead.
func (c *Client) GetWorkflowJobLogs(ctx context.Context, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	return c.jobLogs(ctx, jobID, LogViewOptions{
		Head: head, Tail: tail, Offset: offset, NoHeaders: noHeaders, Filter: filterOpts,
	})
}

// jobLogs is the single implementation behind GetWorkflowJobLogs.
func (c *Client) jobLogs(ctx context.Context, jobID int64, opts LogViewOptions) (string, error) {
	payload, err := c.jobLogPayload(ctx, jobID)
	if err != nil {
		return "", err
	}
	return formatLogFiles(logFilesFromJobPayload(payload, jobID), opts)
}

// jobLogPayload downloads a job's raw log payload, bounded to maxLogFileSize.
//
// The payload is fetched with presignedHTTPClient because GitHub redirects to
// pre-signed storage, which rejects an Authorization header.
func (c *Client) jobLogPayload(ctx context.Context, jobID int64) ([]byte, error) {
	url, resp, err := c.gh.Actions.GetWorkflowJobLogs(ctx, c.owner, c.repo, jobID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get job log URL for job %d: %w", jobID, err)
	}
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, newHTTPErrorFromGitHub(resp, "failed to get job logs")
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build job log request for job %d: %w", jobID, err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch job logs for job %d: %w", jobID, err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: zipResp.StatusCode,
			Message:    fmt.Sprintf("failed to fetch job logs: HTTP %d", zipResp.StatusCode),
		}
	}

	payload, err := io.ReadAll(io.LimitReader(zipResp.Body, maxLogFileSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read job logs for job %d: %w", jobID, err)
	}
	return payload, nil
}

// logFilesFromJobPayload interprets a job log payload as either a ZIP archive or,
// failing that, a single plain-text log named "job-<id>.log".
func logFilesFromJobPayload(payload []byte, jobID int64) []logFile {
	zipReader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return []logFile{{name: fmt.Sprintf("job-%d.log", jobID), data: string(payload)}}
	}

	var logFiles []logFile
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in log archive: %v", file.Name, err)
			continue
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in log archive: %v", file.Name, err)
			continue
		}
		logFiles = append(logFiles, logFile{name: file.Name, data: stripANSI(string(content))})
	}
	return dropCombinedJobLogs(logFiles)
}

// GetWorkflowJobLogsFromRunArchive returns a job's logs by extracting the job's
// folder from the run-level log archive.
//
// This is the fallback for jobs whose dedicated log endpoint returns 404 while the
// run archive still holds their system logs. It errors when the job is not part of
// the run, or when the archive contains no entry under the job's name.
//
// See LogViewOptions for how head, tail, offset, noHeaders and filterOpts interact.
func (c *Client) GetWorkflowJobLogsFromRunArchive(ctx context.Context, runID, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	return c.jobLogsFromRunArchive(ctx, runID, jobID, LogViewOptions{
		Head: head, Tail: tail, Offset: offset, NoHeaders: noHeaders, Filter: filterOpts,
	})
}

// jobLogsFromRunArchive is the single implementation behind
// GetWorkflowJobLogsFromRunArchive.
func (c *Client) jobLogsFromRunArchive(ctx context.Context, runID, jobID int64, opts LogViewOptions) (string, error) {
	jobName, err := c.jobNameInRun(ctx, runID, jobID)
	if err != nil {
		return "", err
	}

	logFiles, err := c.runLogArchive(ctx, runID)
	if err != nil {
		return "", err
	}

	prefix := jobName + "/"
	filtered := make([]logFile, 0, len(logFiles))
	for _, lf := range logFiles {
		if strings.HasPrefix(lf.name, prefix) {
			filtered = append(filtered, lf)
		}
	}
	if len(filtered) == 0 {
		return "", fmt.Errorf("no logs for job %q (%d) found in run %d archive", jobName, jobID, runID)
	}

	return formatLogFiles(filtered, opts)
}

// jobNameInRun resolves a job ID to its name within a run. The name is what
// identifies the job's folder inside the run log archive.
func (c *Client) jobNameInRun(ctx context.Context, runID, jobID int64) (string, error) {
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
	if err != nil {
		return "", err
	}
	for _, job := range jobs {
		if job.ID == jobID {
			return job.Name, nil
		}
	}
	return "", fmt.Errorf("job %d not found in run %d", jobID, runID)
}
