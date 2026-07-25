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
	"sort"
	"strings"
)

// logSizeThreshold is the size at which we switch to temp file processing
const logSizeThreshold = 10 * 1024 * 1024 // 10MB

// maxLogFileSize is the maximum size for individual log files we'll read
const maxLogFileSize = 50 * 1024 * 1024 // 50MB per file

// LogFileInfo represents information about a single log file in the archive
type LogFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// logFile represents a log file's name and content
type logFile struct {
	name string
	data string
}

// readZipArchive reads a ZIP archive from a URL, using temp files for large downloads
// to avoid loading everything into memory. Returns a slice of log files.
func readZipArchive(zipURL string, httpClient *http.Client) ([]logFile, int64, error) {
	// Fetch the ZIP file
	zipResp, err := httpClient.Get(zipURL)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: %w", err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("failed to fetch ZIP: HTTP %d", zipResp.StatusCode)
	}

	// Check content length to decide approach
	contentLength := zipResp.ContentLength
	useTempFile := contentLength > logSizeThreshold || contentLength < 0

	var zipReader *zip.Reader
	cleanup := func() {}
	defer func() { cleanup() }()

	if useTempFile {
		// For large archives or unknown size, use a temp file
		tempFile, err := os.CreateTemp("", "logs-*.zip")
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create temp file: %w", err)
		}

		// Copy to temp file
		written, err := io.Copy(tempFile, zipResp.Body)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to write to temp file: %w", err)
		}

		// Re-open for reading
		_, err = tempFile.Seek(0, 0)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to seek temp file: %w", err)
		}

		zipReader, err = zip.NewReader(tempFile, written)
		if err != nil {
			tempFile.Close()
			os.Remove(tempFile.Name())
			return nil, 0, fmt.Errorf("failed to open ZIP: %w", err)
		}

		// Set up cleanup function
		cleanup = func() {
			tempFile.Close()
			os.Remove(tempFile.Name())
		}

		contentLength = written
	} else {
		// For small archives, read into memory
		zipData, err := io.ReadAll(zipResp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read ZIP: %w", err)
		}

		zipReader, err = zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to open ZIP: %w", err)
		}

		contentLength = int64(len(zipData))
		cleanup = func() {}
	}

	// Process files
	var logFiles []logFile
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// Skip excessively large individual files
		if file.UncompressedSize64 > uint64(maxLogFileSize) {
			log.Debugf("Skipping large log file %s (%d bytes)", file.Name, file.UncompressedSize64)
			continue
		}

		rc, err := file.Open()
		if err != nil {
			log.Debugf("Warning: could not open %s in ZIP: %v", file.Name, err)
			continue
		}

		// Use limited reader to prevent excessive memory usage
		content, err := io.ReadAll(io.LimitReader(rc, maxLogFileSize))
		rc.Close()
		if err != nil {
			log.Debugf("Warning: could not read %s in ZIP: %v", file.Name, err)
			continue
		}

		logFiles = append(logFiles, logFile{
			name: file.Name,
			data: string(content),
		})
	}

	// Sort by filename for consistent output
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	return logFiles, contentLength, nil
}

func formatLogFiles(logFiles []logFile, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	sort.Slice(logFiles, func(i, j int) bool {
		return logFiles[i].name < logFiles[j].name
	})

	var allLogs strings.Builder
	for _, lf := range logFiles {
		if !noHeaders {
			allLogs.WriteString(fmt.Sprintf("=== %s ===\n", lf.name))
		}
		allLogs.WriteString(lf.data)
		if !strings.HasSuffix(lf.data, "\n") {
			allLogs.WriteString("\n")
		}
	}

	logStr := strings.TrimRight(allLogs.String(), "\n")

	if filterOpts != nil && (filterOpts.Filter != "" || filterOpts.FilterRegex != "") {
		parsedLines := parseLogLines(logStr)
		filteredLines, err := filterLogLines(parsedLines, filterOpts)
		if err != nil {
			return "", err
		}
		if filteredLines == nil {
			return "", nil
		}
		logStr = linesToString(filteredLines)
	}

	lines := strings.Split(logStr, "\n")
	if offset > 0 {
		if offset >= len(lines) {
			lines = nil
		} else {
			lines = lines[offset:]
		}
	}

	if tail > 0 {
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
	} else if head > 0 && len(lines) > head {
		lines = lines[:head]
	}

	logStr = strings.Join(lines, "\n")
	if logStr != "" {
		logStr += "\n"
	}

	return logStr, nil
}

// GetWorkflowLogFiles returns a list of log files available in the workflow run archive
func (c *Client) GetWorkflowLogFiles(ctx context.Context, runID int64) ([]*LogFileInfo, error) {
	// Get the log archive URL
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return nil, newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Fetch ZIP archive (use unauthenticated client for pre-signed storage URLs)
	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
	if err != nil {
		return nil, fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}

	// Convert to LogFileInfo
	result := make([]*LogFileInfo, 0, len(logFiles))
	for _, lf := range logFiles {
		result = append(result, &LogFileInfo{
			Path: lf.name,
			Size: int64(len(lf.data)),
		})
	}

	return result, nil
}

// GetWorkflowLogsWithPattern retrieves logs for a workflow run with optional file pattern filtering
func (c *Client) GetWorkflowLogsWithPattern(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filePattern string, filterOpts *LogFilterOptions) (string, error) {
	// Get the log archive URL
	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	// Read ZIP archive (use unauthenticated client for pre-signed storage URLs)
	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
	if err != nil {
		return "", fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}

	// Apply file pattern filter if specified
	if filePattern != "" {
		filtered := make([]logFile, 0)
		for _, lf := range logFiles {
			matched, err := filepath.Match(filePattern, lf.name)
			if err != nil {
				return "", fmt.Errorf("invalid file pattern %q: %w", filePattern, err)
			}
			if matched {
				filtered = append(filtered, lf)
			}
		}
		logFiles = filtered
	}

	return formatLogFiles(logFiles, head, tail, offset, noHeaders, filterOpts)
}

// GetWorkflowLogs retrieves the logs for a workflow run and returns them as a string.
// The logs can be filtered by substring or regex pattern, with optional context lines.
// After filtering, results can be limited by line count using head, tail, and offset parameters.
// - offset: skip first N lines (0-based)
// - head: return at most N lines from the offset (if specified)
// - tail: return the last N lines (takes precedence over head+offset)
// If noHeaders is true, file headers (=== filename ===) are not included.
func (c *Client) GetWorkflowLogs(ctx context.Context, runID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	return c.GetWorkflowLogsWithPattern(ctx, runID, head, tail, offset, noHeaders, "", filterOpts)
}

// GetWorkflowJobLogs retrieves logs for a specific job
func (c *Client) GetWorkflowJobLogs(ctx context.Context, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	// Get the log archive
	url, resp, err := c.gh.Actions.GetWorkflowJobLogs(ctx, c.owner, c.repo, jobID, maxRedirects)
	if err != nil {
		return "", fmt.Errorf("failed to get job log URL for job %d: %w", jobID, err)
	}

	// Check response status
	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get job logs")
		}
	}

	// Fetch the redirected payload URL without auth headers.
	// Some storage backends reject Authorization headers on pre-signed URLs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to build job log request for job %d: %w", jobID, err)
	}
	zipResp, err := presignedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch job logs for job %d: %w", jobID, err)
	}
	defer zipResp.Body.Close()

	if zipResp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: zipResp.StatusCode, Message: fmt.Sprintf("failed to fetch job logs: HTTP %d", zipResp.StatusCode)}
	}

	// Read the payload data (may be ZIP or plain text), bounded to maxLogFileSize.
	zipData, err := io.ReadAll(io.LimitReader(zipResp.Body, maxLogFileSize))
	if err != nil {
		return "", fmt.Errorf("failed to read job logs for job %d: %w", jobID, err)
	}

	// Collect all log files from ZIP payload when available.
	// GitHub may also return plain text for job log downloads.
	var logFiles []logFile
	if zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData))); err == nil {
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

			logFiles = append(logFiles, logFile{
				name: file.Name,
				data: string(content),
			})
		}
	} else {
		logFiles = append(logFiles, logFile{
			name: fmt.Sprintf("job-%d.log", jobID),
			data: string(zipData),
		})
	}

	return formatLogFiles(logFiles, head, tail, offset, noHeaders, filterOpts)
}

// GetWorkflowJobLogsFromRunArchive retrieves logs for a job from the workflow
// run log archive. This is a fallback for cases where GitHub's job log endpoint
// returns 404 but the run-level log archive still contains the job's system logs.
func (c *Client) GetWorkflowJobLogsFromRunArchive(ctx context.Context, runID, jobID int64, head, tail, offset int, noHeaders bool, filterOpts *LogFilterOptions) (string, error) {
	jobs, err := c.GetWorkflowJobs(ctx, runID, "", 0)
	if err != nil {
		return "", err
	}

	var jobName string
	for _, job := range jobs {
		if job.ID == jobID {
			jobName = job.Name
			break
		}
	}
	if jobName == "" {
		return "", fmt.Errorf("job %d not found in run %d", jobID, runID)
	}

	url, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.owner, c.repo, runID, maxRedirects)
	if err != nil {
		return "", fmt.Errorf("failed to get workflow log URL for run %d: %w", runID, err)
	}

	if resp != nil && resp.StatusCode != 0 {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
			return "", newHTTPErrorFromGitHub(resp, "failed to get workflow logs")
		}
	}

	logFiles, _, err := readZipArchive(url.String(), presignedHTTPClient)
	if err != nil {
		return "", fmt.Errorf("failed to read log archive for run %d: %w", runID, err)
	}

	prefix := jobName + "/"
	filtered := make([]logFile, 0)
	for _, lf := range logFiles {
		if strings.HasPrefix(lf.name, prefix) {
			filtered = append(filtered, lf)
		}
	}
	if len(filtered) == 0 {
		return "", fmt.Errorf("no logs for job %q (%d) found in run %d archive", jobName, jobID, runID)
	}

	return formatLogFiles(filtered, head, tail, offset, noHeaders, filterOpts)
}
