package github

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to test line splitting logic in isolation
func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		head     int
		tail     int
		expected string
	}{
		// Basic cases with trailing newlines
		{
			name:     "Single line with trailing newline - no limit",
			input:    "line1\n",
			head:     0,
			tail:     0,
			expected: "line1\n",
		},
		{
			name:     "Two lines with trailing newline - no limit",
			input:    "line1\nline2\n",
			head:     0,
			tail:     0,
			expected: "line1\nline2\n",
		},
		// Head limiting
		{
			name:     "Head limit 1 - two lines",
			input:    "line1\nline2\n",
			head:     1,
			tail:     0,
			expected: "line1\n",
		},
		{
			name:     "Head limit 2 - three lines",
			input:    "line1\nline2\nline3\n",
			head:     2,
			tail:     0,
			expected: "line1\nline2\n",
		},
		{
			name:     "Head limit greater than available lines",
			input:    "line1\nline2\n",
			head:     10,
			tail:     0,
			expected: "line1\nline2\n",
		},
		{
			name:     "Head limit 1 - single line",
			input:    "line1\n",
			head:     1,
			tail:     0,
			expected: "line1\n",
		},
		// Tail limiting
		{
			name:     "Tail limit 1 - two lines",
			input:    "line1\nline2\n",
			head:     0,
			tail:     1,
			expected: "line2\n",
		},
		{
			name:     "Tail limit 2 - three lines",
			input:    "line1\nline2\nline3\n",
			head:     0,
			tail:     2,
			expected: "line2\nline3\n",
		},
		{
			name:     "Tail limit greater than available lines",
			input:    "line1\nline2\n",
			head:     0,
			tail:     10,
			expected: "line1\nline2\n",
		},
		{
			name:     "Tail limit 1 - single line",
			input:    "line1\n",
			head:     0,
			tail:     1,
			expected: "line1\n",
		},
		// Tail takes precedence over head
		{
			name:     "Tail takes precedence over head",
			input:    "line1\nline2\nline3\n",
			head:     1,
			tail:     2,
			expected: "line2\nline3\n",
		},
		// Edge cases - no trailing newline
		{
			name:     "No trailing newline - head limit",
			input:    "line1\nline2\nline3",
			head:     2,
			tail:     0,
			expected: "line1\nline2\n",
		},
		{
			name:     "No trailing newline - tail limit",
			input:    "line1\nline2\nline3",
			head:     0,
			tail:     2,
			expected: "line2\nline3\n",
		},
		// Multiple trailing newlines
		{
			name:     "Multiple trailing newlines - head limit",
			input:    "line1\nline2\n\n\n",
			head:     1,
			tail:     0,
			expected: "line1\n",
		},
		{
			name:     "Multiple trailing newlines - tail limit",
			input:    "line1\nline2\n\n\n",
			head:     0,
			tail:     1,
			expected: "line2\n",
		},
		// Empty input
		{
			name:     "Empty string - no limit",
			input:    "",
			head:     0,
			tail:     0,
			expected: "\n",
		},
		// Only newlines
		{
			name:     "Only newlines - tail limit 1",
			input:    "\n\n\n",
			head:     0,
			tail:     1,
			expected: "\n",
		},
		{
			name:     "Only newlines - tail limit 2",
			input:    "\n\n\n",
			head:     0,
			tail:     2,
			expected: "\n",
		},
		// Realistic log output
		{
			name:     "Multi-line log output - head limit",
			input:    "[2024-01-01 10:00:00] Starting build...\n[2024-01-01 10:00:01] Running tests...\n[2024-01-01 10:00:02] Build complete\n",
			head:     2,
			tail:     0,
			expected: "[2024-01-01 10:00:00] Starting build...\n[2024-01-01 10:00:01] Running tests...\n",
		},
		{
			name:     "Multi-line log output - tail limit",
			input:    "[2024-01-01 10:00:00] Starting build...\n[2024-01-01 10:00:01] Running tests...\n[2024-01-01 10:00:02] Build complete\n",
			head:     0,
			tail:     2,
			expected: "[2024-01-01 10:00:01] Running tests...\n[2024-01-01 10:00:02] Build complete\n",
		},
		// Windows-style line endings (shouldn't be in logs but test anyway)
		{
			name:     "CRLF line endings - head limit",
			input:    "line1\r\nline2\r\nline3\r\n",
			head:     2,
			tail:     0,
			expected: "line1\r\nline2\r\n",
		},
		// Lines with special characters
		{
			name:     "Lines with special chars - tail limit",
			input:    "Error: file not found\nWarning: deprecated API\nInfo: processing...\n",
			head:     0,
			tail:     2,
			expected: "Warning: deprecated API\nInfo: processing...\n",
		},
		// Very long lines
		{
			name:     "Very long single line - head limit 1",
			input:    strings.Repeat("a", 1000) + "\n" + strings.Repeat("b", 1000) + "\n",
			head:     1,
			tail:     0,
			expected: strings.Repeat("a", 1000) + "\n",
		},
		// Single line without newline - head limit
		{
			name:     "Single line no newline - head limit",
			input:    "lonelyline",
			head:     5,
			tail:     0,
			expected: "lonelyline\n",
		},
		// Single line without newline - tail limit
		{
			name:     "Single line no newline - tail limit",
			input:    "lonelyline",
			head:     0,
			tail:     5,
			expected: "lonelyline\n",
		},
		// Lines with leading/trailing spaces
		{
			name:     "Lines with spaces - head limit",
			input:    "  line1  \n  line2  \n  line3  \n",
			head:     2,
			tail:     0,
			expected: "  line1  \n  line2  \n",
		},
		// Empty lines between content
		{
			name:     "Empty lines between content - tail limit",
			input:    "line1\n\nline3\n",
			head:     0,
			tail:     2,
			expected: "\nline3\n",
		},
		// Tab characters
		{
			name:     "Tab characters - head limit",
			input:    "\t\t\tline1\n\t\t\tline2\n",
			head:     1,
			tail:     0,
			expected: "\t\t\tline1\n",
		},
		// Head limit equals exact line count
		{
			name:     "Head equals line count - should return all",
			input:    "line1\nline2\nline3\n",
			head:     3,
			tail:     0,
			expected: "line1\nline2\nline3\n",
		},
		// Tail limit equals exact line count
		{
			name:     "Tail equals line count - should return all",
			input:    "line1\nline2\nline3\n",
			head:     0,
			tail:     3,
			expected: "line1\nline2\nline3\n",
		},
		// Head limit one less than available
		{
			name:     "Head one less than available",
			input:    "line1\nline2\nline3\n",
			head:     2,
			tail:     0,
			expected: "line1\nline2\n",
		},
		// Tail limit one less than available
		{
			name:     "Tail one less than available",
			input:    "line1\nline2\nline3\n",
			head:     0,
			tail:     2,
			expected: "line2\nline3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the TrimRight logic from GetWorkflowLogs
			logStr := strings.TrimRight(tt.input, "\n")

			// Apply line limiting
			var result string
			if tt.tail > 0 {
				lines := strings.Split(logStr, "\n")
				if len(lines) > tt.tail {
					lines = lines[len(lines)-tt.tail:]
					result = strings.Join(lines, "\n") + "\n"
				} else {
					result = logStr + "\n"
				}
			} else if tt.head > 0 {
				lines := strings.Split(logStr, "\n")
				if len(lines) > tt.head {
					lines = lines[:tt.head]
					result = strings.Join(lines, "\n") + "\n"
				} else {
					result = logStr + "\n"
				}
			} else {
				result = logStr + "\n"
			}

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetWorkflowJobLogs_RedirectPlainTextWithoutAuthHeader(t *testing.T) {
	const (
		owner = "example-owner"
		repo  = "example-repo"
		jobID = int64(12345)
	)

	mux := http.NewServeMux()
	redirectBase := ""
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/jobs/12345/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"missing auth"}`))
			return
		}
		w.Header().Set("Location", redirectBase+"/blob/job.log")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/job.log", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("InvalidAuthenticationInfo"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("line-1\nline-2\nline-3\n"))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	redirectBase = ts.URL

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	logs, err := client.GetWorkflowJobLogs(context.Background(), jobID, 0, 0, 0, true, nil)
	assert.NoError(t, err)
	assert.Contains(t, logs, "line-1")
	assert.Contains(t, logs, "line-3")
	assert.NotContains(t, logs, "InvalidAuthenticationInfo")
}

func TestGetWorkflowJobLogs_RedirectZipStillWorks(t *testing.T) {
	const (
		owner = "example-owner"
		repo  = "example-repo"
		jobID = int64(12345)
	)

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, err := zw.Create("step-1.log")
	assert.NoError(t, err)
	_, err = io.WriteString(f, "zip-line-1\nzip-line-2\n")
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())

	mux := http.NewServeMux()
	redirectBase := ""
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/jobs/12345/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", redirectBase+"/blob/job.zip")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/job.zip", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBuf.Bytes())
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	redirectBase = ts.URL

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	logs, err := client.GetWorkflowJobLogs(context.Background(), jobID, 0, 0, 0, false, nil)
	assert.NoError(t, err)
	assert.Contains(t, logs, "=== step-1.log ===")
	assert.Contains(t, logs, "zip-line-2")
}

func TestGetWorkflowJobLogsFromRunArchive_FiltersJobFolder(t *testing.T) {
	const (
		owner = "example-owner"
		repo  = "example-repo"
		runID = int64(100)
		jobID = int64(12345)
	)

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, err := zw.Create("Lint/system.txt")
	require.NoError(t, err)
	_, err = io.WriteString(f, "lint-system-line\n")
	require.NoError(t, err)
	f, err = zw.Create("Test/system.txt")
	require.NoError(t, err)
	_, err = io.WriteString(f, "test-system-line\n")
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	mux := http.NewServeMux()
	redirectBase := ""
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{"id": 12345, "name": "Lint", "status": "completed", "conclusion": "failure", "run_id": 100},
				{"id": 67890, "name": "Test", "status": "completed", "conclusion": "failure", "run_id": 100}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", redirectBase+"/blob/run.zip")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/run.zip", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBuf.Bytes())
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	redirectBase = ts.URL

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	logs, err := client.GetWorkflowJobLogsFromRunArchive(context.Background(), runID, jobID, 0, 0, 0, false, nil)
	require.NoError(t, err)
	assert.Contains(t, logs, "=== Lint/system.txt ===")
	assert.Contains(t, logs, "lint-system-line")
	assert.NotContains(t, logs, "test-system-line")
}

// TestFormatLogFiles pins the exact head/tail/offset/noHeaders/filter interaction
// before LogViewOptions is introduced. Every case here must still hold after the
// signature is routed through the options struct.
func TestFormatLogFiles(t *testing.T) {
	t.Parallel()

	two := []logFile{
		{name: "b.txt", data: "b1\nb2\n"},
		{name: "a.txt", data: "a1\na2\na3"},
	}

	tests := []struct {
		name    string
		files   []logFile
		head    int
		tail    int
		offset  int
		noHdr   bool
		filter  *LogFilterOptions
		want    string
		wantErr string
	}{
		{
			name:  "no limits: files sorted by name, headers added, missing newline supplied",
			files: two,
			want:  "=== a.txt ===\na1\na2\na3\n=== b.txt ===\nb1\nb2\n",
		},
		{
			name:  "noHeaders suppresses the === name === lines",
			files: two,
			noHdr: true,
			want:  "a1\na2\na3\nb1\nb2\n",
		},
		{
			name:  "head counts header lines too",
			files: two,
			head:  2,
			want:  "=== a.txt ===\na1\n",
		},
		{
			name:  "tail takes precedence over head",
			files: two,
			head:  1,
			tail:  2,
			want:  "b1\nb2\n",
		},
		{
			name:   "offset skips leading lines, including headers",
			files:  two,
			offset: 5,
			want:   "b1\nb2\n",
		},
		{
			name:   "offset beyond the line count yields an empty string",
			files:  two,
			offset: 100,
			want:   "",
		},
		{
			name:   "offset is applied before head",
			files:  two,
			offset: 1,
			head:   2,
			want:   "a1\na2\n",
		},
		{
			name:   "offset is ignored when tail is set",
			files:  two,
			offset: 1,
			tail:   1,
			want:   "b2\n",
		},
		{
			name:  "head larger than the input returns everything",
			files: two,
			head:  1000,
			want:  "=== a.txt ===\na1\na2\na3\n=== b.txt ===\nb1\nb2\n",
		},
		{
			name:   "filter keeps matching lines and their file header",
			files:  two,
			filter: &LogFilterOptions{Filter: "b1"},
			want:   "=== b.txt ===\nb1\n",
		},
		{
			name:   "filter with no match yields an empty string",
			files:  two,
			filter: &LogFilterOptions{Filter: "nothing-matches"},
			want:   "",
		},
		{
			name:    "invalid filter regex is reported",
			files:   two,
			filter:  &LogFilterOptions{FilterRegex: "[unterminated"},
			wantErr: "invalid regex pattern",
		},
		{
			name:   "empty filter options are ignored",
			files:  two,
			filter: &LogFilterOptions{ContextLines: 3},
			want:   "=== a.txt ===\na1\na2\na3\n=== b.txt ===\nb1\nb2\n",
		},
		{
			name:  "no files yields an empty string",
			files: nil,
			want:  "",
		},
		{
			name:  "empty file body yields just the header",
			files: []logFile{{name: "a.txt", data: ""}},
			want:  "=== a.txt ===\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// formatLogFiles sorts in place, so hand it a copy.
			files := append([]logFile(nil), tt.files...)
			got, err := formatLogFiles(files, LogViewOptions{
				Head: tt.head, Tail: tt.tail, Offset: tt.offset, NoHeaders: tt.noHdr, Filter: tt.filter,
			})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReadZipArchive(t *testing.T) {
	t.Parallel()

	t.Run("reads and sorts entries, skipping directories", func(t *testing.T) {
		t.Parallel()

		zipData := makeArtifactZIP(t, map[string]string{
			"b/2_second.txt": "second",
			"a/1_first.txt":  "first",
		})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipData)
		}))
		defer ts.Close()

		files, size, err := readZipArchive(ts.URL, ts.Client())
		require.NoError(t, err)
		require.Len(t, files, 2)
		assert.Equal(t, "a/1_first.txt", files[0].name)
		assert.Equal(t, "first", files[0].data)
		assert.Equal(t, "b/2_second.txt", files[1].name)
		assert.Equal(t, int64(len(zipData)), size)
	})

	t.Run("non-200 response is an error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		_, _, err := readZipArchive(ts.URL, ts.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch ZIP: HTTP 404")
	})

	t.Run("payload that is not a ZIP is an error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("this is not a zip"))
		}))
		defer ts.Close()

		_, _, err := readZipArchive(ts.URL, ts.Client())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open ZIP")
	})

	t.Run("unreachable host is an error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := ts.URL
		client := ts.Client()
		ts.Close()

		_, _, err := readZipArchive(url, client)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch ZIP")
	})

	t.Run("unknown content length takes the temp-file path", func(t *testing.T) {
		t.Parallel()

		zipData := makeArtifactZIP(t, map[string]string{"only.txt": "payload"})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Chunked encoding => ContentLength -1 => temp-file branch.
			w.Header().Set("Transfer-Encoding", "chunked")
			_, _ = w.Write(zipData)
		}))
		defer ts.Close()

		files, size, err := readZipArchive(ts.URL, ts.Client())
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Equal(t, "payload", files[0].data)
		assert.Equal(t, int64(len(zipData)), size)
	})
}

// newRunLogArchiveClient serves a run-log archive the way GitHub does: the API
// endpoint 302-redirects to a pre-signed URL that carries the ZIP.
func newRunLogArchiveClient(t *testing.T, runID int64, files map[string]string) *Client {
	t.Helper()

	zipData := makeArtifactZIP(t, files)
	base := ""
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/actions/runs/%d/logs", runID), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", base+"/blob/run.zip")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/run.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	})
	client, url := newMuxClientWithURL(t, mux)
	base = url
	return client
}

func TestGetWorkflowLogs(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"build/1_step.txt": "build-line-1\nbuild-line-2",
		"lint/1_step.txt":  "lint-line-1",
	}

	t.Run("concatenates every file with headers", func(t *testing.T) {
		t.Parallel()

		logs, err := newRunLogArchiveClient(t, 1, files).GetWorkflowLogs(context.Background(), 1, 0, 0, 0, false, nil)
		require.NoError(t, err)
		assert.Equal(t, "=== build/1_step.txt ===\nbuild-line-1\nbuild-line-2\n=== lint/1_step.txt ===\nlint-line-1\n", logs)
	})

	t.Run("head, tail, offset and noHeaders are honoured", func(t *testing.T) {
		t.Parallel()

		client := newRunLogArchiveClient(t, 1, files)

		head, err := client.GetWorkflowLogs(context.Background(), 1, 2, 0, 0, true, nil)
		require.NoError(t, err)
		assert.Equal(t, "build-line-1\nbuild-line-2\n", head)

		tail, err := client.GetWorkflowLogs(context.Background(), 1, 0, 1, 0, true, nil)
		require.NoError(t, err)
		assert.Equal(t, "lint-line-1\n", tail)

		offset, err := client.GetWorkflowLogs(context.Background(), 1, 0, 0, 1, true, nil)
		require.NoError(t, err)
		assert.Equal(t, "build-line-2\nlint-line-1\n", offset)
	})

	t.Run("filter options are applied", func(t *testing.T) {
		t.Parallel()

		logs, err := newRunLogArchiveClient(t, 1, files).GetWorkflowLogs(context.Background(), 1, 0, 0, 0, false, &LogFilterOptions{Filter: "lint-line"})
		require.NoError(t, err)
		assert.Equal(t, "=== lint/1_step.txt ===\nlint-line-1\n", logs)
	})

	t.Run("a log URL failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		_, err := client.GetWorkflowLogs(context.Background(), 1, 0, 0, 0, false, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get workflow log URL for run 1")
	})
}

func TestGetWorkflowLogsWithPattern(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"build/1_step.txt": "build-line",
		"lint/1_step.txt":  "lint-line",
	}

	t.Run("pattern selects matching archive entries", func(t *testing.T) {
		t.Parallel()

		logs, err := newRunLogArchiveClient(t, 1, files).GetWorkflowLogsWithPattern(context.Background(), 1, 0, 0, 0, false, "lint/*", nil)
		require.NoError(t, err)
		assert.Equal(t, "=== lint/1_step.txt ===\nlint-line\n", logs)
	})

	t.Run("a pattern matching nothing yields an empty result", func(t *testing.T) {
		t.Parallel()

		logs, err := newRunLogArchiveClient(t, 1, files).GetWorkflowLogsWithPattern(context.Background(), 1, 0, 0, 0, false, "nope/*", nil)
		require.NoError(t, err)
		assert.Empty(t, logs)
	})

	t.Run("an invalid pattern is reported", func(t *testing.T) {
		t.Parallel()

		_, err := newRunLogArchiveClient(t, 1, files).GetWorkflowLogsWithPattern(context.Background(), 1, 0, 0, 0, false, "[", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid file pattern "["`)
	})
}

func TestGetWorkflowLogFiles(t *testing.T) {
	t.Parallel()

	t.Run("reports each entry's path and byte size", func(t *testing.T) {
		t.Parallel()

		client := newRunLogArchiveClient(t, 1, map[string]string{
			"build/1_step.txt": "12345",
			"lint/1_step.txt":  "abc",
		})

		infos, err := client.GetWorkflowLogFiles(context.Background(), 1)
		require.NoError(t, err)
		require.Len(t, infos, 2)
		assert.Equal(t, "build/1_step.txt", infos[0].Path)
		assert.Equal(t, int64(5), infos[0].Size)
		assert.Equal(t, "lint/1_step.txt", infos[1].Path)
		assert.Equal(t, int64(3), infos[1].Size)
	})

	t.Run("a log URL failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", statusHandler(http.StatusGone))
		client := newMuxClient(t, mux)

		_, err := client.GetWorkflowLogFiles(context.Background(), 1)
		require.Error(t, err)
	})
}

func TestGetWorkflowJobLogsFromRunArchive_Errors(t *testing.T) {
	t.Parallel()

	t.Run("an unknown job ID is rejected before any log fetch", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", jsonHandler(`{"total_count":1,"jobs":[{"id":10,"name":"build"}]}`))
		client := newMuxClient(t, mux)

		_, err := client.GetWorkflowJobLogsFromRunArchive(context.Background(), 1, 999, 0, 0, 0, false, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job 999 not found in run 1")
	})

	t.Run("a job with no archive entries is an error", func(t *testing.T) {
		t.Parallel()

		zipData := makeArtifactZIP(t, map[string]string{"other/1_step.txt": "x"})
		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", jsonHandler(`{"total_count":1,"jobs":[{"id":10,"name":"build"}]}`))
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/run.zip")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/run.zip", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(zipData)
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		_, err := client.GetWorkflowJobLogsFromRunArchive(context.Background(), 1, 10, 0, 0, 0, false, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `no logs for job "build" (10) found in run 1 archive`)
	})

	t.Run("a job listing failure is propagated", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", statusHandler(http.StatusForbidden))
		client := newMuxClient(t, mux)

		_, err := client.GetWorkflowJobLogsFromRunArchive(context.Background(), 1, 10, 0, 0, 0, false, nil)
		require.Error(t, err)
	})
}

func TestGetWorkflowJobLogs_Errors(t *testing.T) {
	t.Parallel()

	t.Run("a log URL failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		_, err := client.GetWorkflowJobLogs(context.Background(), 10, 0, 0, 0, false, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get job log URL for job 10")
	})

	t.Run("a non-200 pre-signed response becomes an HTTPError", func(t *testing.T) {
		t.Parallel()

		base := ""
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/jobs/10/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", base+"/blob/gone.log")
			w.WriteHeader(http.StatusFound)
		})
		mux.HandleFunc("/blob/gone.log", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		_, err := client.GetWorkflowJobLogs(context.Background(), 10, 0, 0, 0, false, nil)
		require.Error(t, err)
		var httpErr *HTTPError
		require.ErrorAs(t, err, &httpErr)
		assert.Equal(t, http.StatusGone, httpErr.StatusCode)
	})
}
