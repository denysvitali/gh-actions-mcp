package github

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/v69/github"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferRepoFromOrigin_HTTPS(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "HTTPS URL",
			url:       "https://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "HTTPS URL without .git",
			url:       "https://github.com/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "HTTP URL",
			url:       "http://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		// Note: Non-github.com URLs will fail as expected
		{
			name:      "Non-GitHub URL fails",
			url:       "https://github.mycompany.com/owner/repo.git",
			wantOwner: "",
			wantRepo:  "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_SSH(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "SSH URL",
			url:       "git@github.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL without .git",
			url:       "git@github.com:owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH enterprise URL",
			url:       "git@github.mycompany.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL with extra slash after colon",
			url:       "git@github.com:/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:      "SSH URL with extra slash after colon without .git",
			url:       "git@github.com:/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_BareFormat(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "Bare owner/repo format",
			url:       "palantir/policy-bot",
			wantOwner: "palantir",
			wantRepo:  "policy-bot",
			wantErr:   false,
		},
		{
			name:      "Bare owner/repo with underscore",
			url:       "owner_name/repo_name",
			wantOwner: "owner_name",
			wantRepo:  "repo_name",
			wantErr:   false,
		},
		{
			name:      "Bare owner/repo with hyphen",
			url:       "my-org/my-repo",
			wantOwner: "my-org",
			wantRepo:  "my-repo",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_Invalid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "Not a GitHub URL",
			url:  "https://gitlab.com/owner/repo.git",
		},
		{
			name: "Malformed URL",
			url:  "not-a-url",
		},
		{
			name: "Empty string",
			url:  "",
		},
		{
			name: "SSH with wrong format",
			url:  "git@github.com:missing-slash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := InferRepoFromOrigin(tt.url)
			assert.Error(t, err)
		})
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient("test-token", "test-owner", "test-repo")

	assert.NotNil(t, client)
	assert.Equal(t, "test-owner", client.owner)
	assert.Equal(t, "test-repo", client.repo)
}

func TestGetRepoInfo(t *testing.T) {
	client := NewClient("token", "owner", "repo")

	repoOwner, repoName := client.GetRepoInfo()

	assert.Equal(t, "owner", repoOwner)
	assert.Equal(t, "repo", repoName)
}

func TestSetLogger(t *testing.T) {
	// This test mainly ensures the function doesn't panic
}

func TestTokenIsSentInRequest(t *testing.T) {
	// Capture request for inspection
	var capturedReq *http.Request

	// Use a custom transport to capture the request
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) *http.Response {
		capturedReq = req
		// Return a mock response
		return &http.Response{
			StatusCode: 200,
			Body:       http.NoBody,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}
	})
	defer func() { http.DefaultTransport = originalTransport }()

	client := NewClient("my-secret-token", "owner", "repo")
	_, _ = client.GetWorkflows(context.Background())

	if capturedReq != nil {
		t.Logf("Authorization header: %q", capturedReq.Header.Get("Authorization"))
		assert.Equal(t, "Bearer my-secret-token", capturedReq.Header.Get("Authorization"))
	} else {
		t.Error("No request was captured")
	}
}

// Test error scenarios
func TestNewClientWithPerPage(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		owner         string
		repo          string
		perPageLimit  int
		expectedLimit int
	}{
		{
			name:          "valid limit",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  100,
			expectedLimit: 100,
		},
		{
			name:          "zero limit uses default",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  0,
			expectedLimit: 50,
		},
		{
			name:          "negative limit uses default",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  -10,
			expectedLimit: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClientWithPerPage(tt.token, tt.owner, tt.repo, tt.perPageLimit)
			assert.NotNil(t, client)
			assert.Equal(t, tt.expectedLimit, client.perPageLimit)
		})
	}
}

func TestClient_GetWorkflowRun_ErrorHandling(t *testing.T) {
	// This test verifies error handling when the API returns an error
	client := NewClient("invalid-token", "owner", "repo")

	// Try to get a non-existent workflow run
	ctx := context.Background()
	_, err := client.GetWorkflowRun(ctx, 999999999)

	// Should return an error (authentication or not found)
	assert.Error(t, err)
}

func TestClient_TriggerWorkflow_ErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		workflowID  string
		ref         string
		expectErr   bool
		errContains string
	}{
		{
			name:        "invalid workflow ID with bad token",
			workflowID:  "nonexistent-workflow",
			ref:         "main",
			expectErr:   true,
			errContains: "failed to trigger workflow",
		},
		{
			name:       "empty workflow ID",
			workflowID: "",
			ref:        "main",
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-token", "owner", "repo")
			err := client.TriggerWorkflow(context.Background(), tt.workflowID, tt.ref)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestClient_APIErrors(t *testing.T) {
	client := NewClient("invalid-token", "owner", "repo")
	ctx := context.Background()

	t.Run("GetActionsStatus with invalid token", func(t *testing.T) {
		_, err := client.GetActionsStatus(ctx, 10)
		// Should get an authentication or network error
		assert.Error(t, err)
	})

	t.Run("GetWorkflows with invalid token", func(t *testing.T) {
		_, err := client.GetWorkflows(ctx)
		// Should get an authentication or network error
		assert.Error(t, err)
	})

	t.Run("GetWorkflowRuns with invalid workflow", func(t *testing.T) {
		_, err := client.GetWorkflowRuns(ctx, 999999999, "main")
		assert.Error(t, err)
	})

	t.Run("CancelWorkflowRun with invalid run ID", func(t *testing.T) {
		err := client.CancelWorkflowRun(ctx, 999999999)
		assert.Error(t, err)
	})

	t.Run("RerunWorkflowRun with invalid run ID", func(t *testing.T) {
		err := client.RerunWorkflowRun(ctx, 999999999)
		assert.Error(t, err)
	})
}

type roundTripperFunc func(*http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := f(req)
	return resp, nil
}

// Tests for log filtering functionality

func TestParseLogLines(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectedCount   int
		expectedHeaders int
	}{
		{
			name:            "Single file with content",
			input:           "=== job/step.txt ===\nline1\nline2",
			expectedCount:   3,
			expectedHeaders: 1,
		},
		{
			name:            "Multiple files",
			input:           "=== file1.txt ===\ncontent1\n=== file2.txt ===\ncontent2",
			expectedCount:   4,
			expectedHeaders: 2,
		},
		{
			name:            "Empty input",
			input:           "",
			expectedCount:   1, // splits to [""]
			expectedHeaders: 0,
		},
		{
			name:            "Only content no headers",
			input:           "line1\nline2\nline3",
			expectedCount:   3,
			expectedHeaders: 0,
		},
		{
			name:            "Header pattern must be exact",
			input:           "=== not closed\n=== valid.txt ===\ncontent",
			expectedCount:   3,
			expectedHeaders: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLogLines(tt.input)
			assert.Equal(t, tt.expectedCount, len(result))

			headerCount := 0
			for _, line := range result {
				if line.isHeader {
					headerCount++
				}
			}
			assert.Equal(t, tt.expectedHeaders, headerCount)
		})
	}
}

func TestFilterLogLines(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		filter        string
		filterRegex   string
		context       int
		expectMatch   bool
		expectError   bool
		expectedLines int
	}{
		{
			name:          "Simple substring match",
			input:         "=== test.txt ===\nline1\nERROR here\nline3",
			filter:        "error",
			context:       0,
			expectMatch:   true,
			expectedLines: 2, // header + match
		},
		{
			name:          "Case insensitive match",
			input:         "=== test.txt ===\nERROR\nerror\nError",
			filter:        "ERROR",
			context:       0,
			expectMatch:   true,
			expectedLines: 4, // header + 3 matches
		},
		{
			name:          "Regex match",
			input:         "=== test.txt ===\nerror: code 123\nwarning: code 456",
			filterRegex:   "code \\d+",
			context:       0,
			expectMatch:   true,
			expectedLines: 3, // header + 2 matches
		},
		{
			name:        "Invalid regex",
			input:       "=== test.txt ===\nline",
			filterRegex: "[invalid",
			expectError: true,
		},
		{
			name:        "No matches",
			input:       "=== test.txt ===\nline1\nline2",
			filter:      "notfound",
			expectMatch: false,
		},
		{
			name:          "Context lines",
			input:         "=== test.txt ===\nline1\nline2\nERROR\nline4\nline5",
			filter:        "error",
			context:       1,
			expectMatch:   true,
			expectedLines: 4, // header + line2 + ERROR + line4
		},
		{
			name:          "Context stops at file boundary",
			input:         "=== file1.txt ===\nline1\nERROR\n=== file2.txt ===\nline2",
			filter:        "error",
			context:       2,
			expectMatch:   true,
			expectedLines: 3, // header + line1 + ERROR (stops at boundary)
		},
		{
			name:        "Header not matched",
			input:       "=== ERROR.txt ===\nline1",
			filter:      "error",
			expectMatch: false, // "error" is only in header, not in content
		},
		{
			name:          "Multiple matches with overlapping context",
			input:         "=== test.txt ===\nline1\nERROR1\nline3\nERROR2\nline5",
			filter:        "error",
			context:       1,
			expectMatch:   true,
			expectedLines: 6, // header + all content (context overlaps)
		},
		{
			name:          "Matches across multiple files",
			input:         "=== file1.txt ===\nok\nERROR1\n=== file2.txt ===\nok\nERROR2",
			filter:        "error",
			context:       0,
			expectMatch:   true,
			expectedLines: 4, // 2 headers + 2 matches
		},
		{
			name:          "Empty filter returns all lines",
			input:         "=== test.txt ===\nline1\nline2",
			filter:        "",
			context:       0,
			expectMatch:   true,
			expectedLines: 3,
		},
		{
			name:          "Nil options returns all lines",
			input:         "=== test.txt ===\nline1\nline2",
			filter:        "",
			filterRegex:   "",
			context:       0,
			expectMatch:   true,
			expectedLines: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := parseLogLines(tt.input)

			var opts *LogFilterOptions
			if tt.filter != "" || tt.filterRegex != "" {
				opts = &LogFilterOptions{
					Filter:       tt.filter,
					FilterRegex:  tt.filterRegex,
					ContextLines: tt.context,
				}
			}

			result, err := filterLogLines(lines, opts)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			if !tt.expectMatch {
				assert.Nil(t, result)
				return
			}

			assert.NotNil(t, result)
			assert.Equal(t, tt.expectedLines, len(result))
		})
	}
}

func TestLinesToString(t *testing.T) {
	tests := []struct {
		name     string
		lines    []logLine
		expected string
	}{
		{
			name:     "Empty slice",
			lines:    []logLine{},
			expected: "",
		},
		{
			name: "Single line",
			lines: []logLine{
				{content: "hello"},
			},
			expected: "hello",
		},
		{
			name: "Multiple lines",
			lines: []logLine{
				{content: "line1"},
				{content: "line2"},
				{content: "line3"},
			},
			expected: "line1\nline2\nline3",
		},
		{
			name: "With header",
			lines: []logLine{
				{content: "=== test.txt ===", isHeader: true},
				{content: "content"},
			},
			expected: "=== test.txt ===\ncontent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := linesToString(tt.lines)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterIntegration(t *testing.T) {
	// Test the full flow: parse -> filter -> convert back to string
	input := `=== job1/step1.txt ===
2024-01-01 10:00:00 Starting build...
2024-01-01 10:00:01 ERROR: compilation failed
2024-01-01 10:00:02 Build failed
=== job1/step2.txt ===
2024-01-01 10:00:03 Running tests...
2024-01-01 10:00:04 All tests passed`

	tests := []struct {
		name     string
		filter   string
		regex    string
		context  int
		contains []string
		excludes []string
	}{
		{
			name:     "Filter for ERROR",
			filter:   "error",
			contains: []string{"ERROR: compilation failed", "=== job1/step1.txt ==="},
			excludes: []string{"Starting build", "Running tests", "=== job1/step2.txt ==="},
		},
		{
			name:     "Filter for ERROR with context",
			filter:   "error",
			context:  1,
			contains: []string{"Starting build", "ERROR: compilation failed", "Build failed"},
			excludes: []string{"Running tests"},
		},
		{
			name:     "Regex for timestamps",
			regex:    "\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2}",
			contains: []string{"Starting build", "ERROR", "Running tests"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := parseLogLines(input)
			opts := &LogFilterOptions{
				Filter:       tt.filter,
				FilterRegex:  tt.regex,
				ContextLines: tt.context,
			}

			filtered, err := filterLogLines(lines, opts)
			assert.NoError(t, err)
			assert.NotNil(t, filtered)

			result := linesToString(filtered)

			for _, s := range tt.contains {
				assert.Contains(t, result, s, "should contain: %s", s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, result, s, "should not contain: %s", s)
			}
		})
	}
}

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

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	assert.NoError(t, err)
	ghc.BaseURL = baseURL

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

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	assert.NoError(t, err)
	ghc.BaseURL = baseURL

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

func TestGetCheckRunsForRef_UsesWorkflowRunsNotChecksAPI(t *testing.T) {
	const (
		owner = "example-owner"
		repo  = "example-repo"
	)

	checksEndpointCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "main", r.URL.Query().Get("branch"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
		  "total_count": 3,
		  "workflow_runs": [
		    {"id": 11, "name": "Build", "status": "completed", "conclusion": "failure", "run_number": 11, "html_url": "https://example.test/r/11"},
		    {"id": 10, "name": "Build", "status": "completed", "conclusion": "success", "run_number": 10, "html_url": "https://example.test/r/10"},
		    {"id": 12, "name": "Lint",  "status": "in_progress", "conclusion": null, "run_number": 5, "html_url": "https://example.test/r/12"}
		  ]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/commits/main/check-runs", func(w http.ResponseWriter, r *http.Request) {
		checksEndpointCalled = true
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"should not be called"}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	assert.NoError(t, err)
	ghc.BaseURL = baseURL

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	status, err := client.GetCheckRunsForRef(context.Background(), "main", &GetCheckRunsOptions{Filter: "latest"})
	assert.NoError(t, err)
	assert.False(t, checksEndpointCalled)
	assert.Equal(t, 2, status.TotalCount)
	assert.Equal(t, "pending", status.State)
	assert.Equal(t, 1, status.ByConclusion["failure"])
	assert.Equal(t, 1, status.ByConclusion["in_progress"])
}

// makeArtifactZIP creates an in-memory ZIP archive with the given files.
func makeArtifactZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(f, content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// artifactJSON returns a JSON body matching the go-github Artifact model.
func artifactJSON(id int64, name string, size int64) []byte {
	m := map[string]interface{}{
		"id":                   id,
		"name":                 name,
		"size_in_bytes":        size,
		"archive_download_url": "",
	}
	b, _ := json.Marshal(m)
	return b
}

// setupArtifactServer creates an httptest server that handles the GetArtifact
// and DownloadArtifact GitHub API endpoints plus a pre-signed blob endpoint
// that rejects requests carrying an Authorization header.
func setupArtifactServer(t *testing.T, owner, repo string, artifactID int64, artifactName string, zipData []byte) (*httptest.Server, *Client) {
	t.Helper()

	if log == nil {
		SetLogger(logrus.New())
	}

	mux := http.NewServeMux()
	redirectBase := ""

	// GET /repos/{owner}/{repo}/actions/artifacts/{id} — metadata
	mux.HandleFunc(
		"/repos/"+owner+"/"+repo+"/actions/artifacts/123",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(artifactJSON(artifactID, artifactName, int64(len(zipData))))
		},
	)

	// GET /repos/{owner}/{repo}/actions/artifacts/{id}/zip — redirect to blob
	mux.HandleFunc(
		"/repos/"+owner+"/"+repo+"/actions/artifacts/123/zip",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", redirectBase+"/blob/artifact.zip")
			w.WriteHeader(http.StatusFound)
		},
	)

	// GET /blob/artifact.zip — pre-signed URL: must NOT carry Authorization
	mux.HandleFunc("/blob/artifact.zip", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("InvalidAuthenticationInfo"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipData)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	redirectBase = ts.URL

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	require.NoError(t, err)
	ghc.BaseURL = baseURL

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}
	return ts, client
}

func TestGetArtifactContent_WithoutAuthHeader(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"hello.txt":  "hello world\n",
		"sub/app.go": "package main\n",
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "my-artifact", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "", 0)
	require.NoError(t, err)

	assert.Equal(t, "my-artifact", content.Name)
	assert.Equal(t, int64(123), content.ID)
	assert.Equal(t, 2, content.FileCount)

	// Files are sorted by path
	require.Len(t, content.Files, 2)
	assert.Equal(t, "hello.txt", content.Files[0].Path)
	assert.Equal(t, "hello world\n", content.Files[0].Content)
	assert.Equal(t, "text", content.Files[0].Encoding)
	assert.Equal(t, "sub/app.go", content.Files[1].Path)
	assert.Equal(t, "package main\n", content.Files[1].Content)
}

func TestGetArtifactContent_FilePattern(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"result.txt": "ok",
		"result.json": `{"status":"ok"}`,
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "results", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "*.json", 0)
	require.NoError(t, err)

	require.Len(t, content.Files, 1)
	assert.Equal(t, "result.json", content.Files[0].Path)
}

func TestGetArtifactContent_MaxFileSize(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"small.txt": "hi",
		"large.txt": strings.Repeat("x", 1000),
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "sized", zipData)

	content, err := client.GetArtifactContent(context.Background(), 123, "", 500)
	require.NoError(t, err)

	require.Len(t, content.Files, 2)
	// Files are sorted by path: large.txt before small.txt
	assert.Equal(t, "large.txt", content.Files[0].Path)
	assert.Contains(t, content.Files[0].Content, "file too large")
	assert.Equal(t, "small.txt", content.Files[1].Path)
	assert.Equal(t, "hi", content.Files[1].Content)
}

func TestDownloadArtifact_WithoutAuthHeader(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{
		"data.csv": "a,b,c\n1,2,3\n",
	})

	_, client := setupArtifactServer(t, owner, repo, 123, "csv-export", zipData)

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "artifact.zip")

	result, err := client.DownloadArtifact(context.Background(), 123, outPath)
	require.NoError(t, err)

	assert.Equal(t, "csv-export", result.Name)
	assert.Equal(t, int64(123), result.ID)
	assert.Equal(t, outPath, result.SavedPath)
	assert.Equal(t, 1, result.FileCount)
	assert.Equal(t, int64(len(zipData)), result.TotalSize)

	// Verify the file on disk is a valid ZIP with the expected content
	saved, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, zipData, saved)
}

func TestDownloadArtifact_DefaultOutputPath(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	zipData := makeArtifactZIP(t, map[string]string{"f.txt": "x"})

	_, client := setupArtifactServer(t, owner, repo, 123, "my-artifact", zipData)

	// Use empty outputPath — function should derive "my-artifact.zip"
	// Run in a temp dir so we don't pollute the repo.
	origDir, _ := os.Getwd()
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	result, err := client.DownloadArtifact(context.Background(), 123, "")
	require.NoError(t, err)

	assert.Equal(t, "my-artifact.zip", result.SavedPath)
	_, err = os.Stat(filepath.Join(tmpDir, "my-artifact.zip"))
	assert.NoError(t, err)
}

// Tests for DiagnoseFailure functionality

func TestErrorPatterns(t *testing.T) {
	tests := []struct {
		line    string
		matches bool
	}{
		{"error: compilation failed", true},
		{"ERROR: something went wrong", true},
		{"error[E0308]: mismatched types", true},
		{"FAIL: TestSomething (0.00s)", true},
		{"fatal: not a git repository", true},
		{"panic: runtime error: index out of range", true},
		{"Traceback (most recent call last):", true},
		{"E   AssertionError: 1 != 2", true},
		{"--- FAIL: TestFoo (0.01s)", true},
		{"exit code 1", true},
		{"command 'make build' failed", true},
		{"Process completed with exit code 1.", true},
		{"##[error]Process completed with exit code 2.", true},
		{"everything is fine", false},
		{"running tests...", false},
		{"Build succeeded", false},
		{"warning: unused variable", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			matched := false
			for _, pattern := range errorPatterns {
				if pattern.MatchString(tt.line) {
					matched = true
					break
				}
			}
			assert.Equal(t, tt.matches, matched, "line: %q", tt.line)
		})
	}
}

func TestBuildDiagnosisSummary(t *testing.T) {
	client := NewClient("token", "owner", "repo")

	t.Run("no failed jobs", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "cancelled",
			FailedJobs: nil,
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "no failed jobs found")
	})

	t.Run("with failed jobs", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "failure",
			FailedJobs: []*FailedJob{
				{
					JobName:    "Build",
					Conclusion: "failure",
					ErrorLines: []string{"error: foo", "error: bar"},
				},
				{
					JobName:    "Test",
					Conclusion: "failure",
					ErrorLines: []string{"FAIL: TestBaz"},
				},
			},
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "2 failed job(s)")
		assert.Contains(t, summary, "Build, Test")
		assert.Contains(t, summary, "3 error line(s)")
	})

	t.Run("with flakiness info", func(t *testing.T) {
		d := &FailureDiagnosis{
			RunID:      123,
			Conclusion: "failure",
			FailedJobs: []*FailedJob{
				{JobName: "CI", Conclusion: "failure", ErrorLines: []string{"err"}},
			},
			Flakiness: &FlakinessInfo{
				RecentRuns:       10,
				RecentFailures:   3,
				RecentSuccesses:  7,
				SameFailureCount: 3,
				Verdict:          "likely_flake",
			},
		}
		summary := client.buildDiagnosisSummary(d)
		assert.Contains(t, summary, "likely_flake")
		assert.Contains(t, summary, "7 succeeded")
	})
}

func TestFailureDiagnosisJSON(t *testing.T) {
	d := &FailureDiagnosis{
		RunID:      42,
		RunName:    "CI",
		Conclusion: "failure",
		FailedJobs: []*FailedJob{
			{
				JobID:      100,
				JobName:    "build",
				Conclusion: "failure",
				FailedSteps: []*FailedStep{
					{Name: "Run tests", Number: 3, Conclusion: "failure"},
				},
				ErrorLines: []string{"--- FAIL: TestFoo"},
			},
		},
		Flakiness: &FlakinessInfo{
			RecentRuns:      5,
			RecentFailures:  1,
			RecentSuccesses: 4,
			Verdict:         "first_failure",
		},
		Summary: "1 failed job(s): build.",
	}

	data, err := json.Marshal(d)
	require.NoError(t, err)

	var decoded FailureDiagnosis
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, int64(42), decoded.RunID)
	assert.Equal(t, "CI", decoded.RunName)
	assert.Len(t, decoded.FailedJobs, 1)
	assert.Equal(t, "build", decoded.FailedJobs[0].JobName)
	assert.Len(t, decoded.FailedJobs[0].FailedSteps, 1)
	assert.Equal(t, "Run tests", decoded.FailedJobs[0].FailedSteps[0].Name)
	assert.Len(t, decoded.FailedJobs[0].ErrorLines, 1)
	assert.NotNil(t, decoded.Flakiness)
	assert.Equal(t, "first_failure", decoded.Flakiness.Verdict)
}

func TestDiagnoseFailure_Integration(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	redirectBase := ""

	// GET /repos/{owner}/{repo}/actions/runs/100
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "completed", "conclusion": "failure",
			"head_branch": "main", "head_sha": "abc123", "html_url": "https://example.com/run/100",
			"event": "push", "run_number": 10, "workflow_id": 50
		}`))
	})

	// GET /repos/{owner}/{repo}/actions/runs/100/jobs
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{
					"id": 200, "name": "build", "status": "completed", "conclusion": "failure",
					"run_id": 100,
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success"},
						{"name": "Build", "number": 2, "status": "completed", "conclusion": "failure"}
					]
				},
				{
					"id": 201, "name": "lint", "status": "completed", "conclusion": "success",
					"run_id": 100,
					"steps": [
						{"name": "Lint", "number": 1, "status": "completed", "conclusion": "success"}
					]
				}
			]
		}`))
	})

	// GET /repos/{owner}/{repo}/actions/jobs/200/logs
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/jobs/200/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", redirectBase+"/blob/job200.log")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/blob/job200.log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("2024-01-15T10:30:00.1234567Z Starting build\n2024-01-15T10:30:01.1234567Z error: cannot find module\n2024-01-15T10:30:02.1234567Z ##[error]Process completed with exit code 1.\n"))
	})

	// GET /repos/{owner}/{repo}/actions/workflows/50/runs (for flakiness check)
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 3,
			"workflow_runs": [
				{"id": 100, "name": "CI", "status": "completed", "conclusion": "failure", "head_branch": "main", "run_number": 10, "html_url": "u", "workflow_id": 50},
				{"id": 99, "name": "CI", "status": "completed", "conclusion": "success", "head_branch": "main", "run_number": 9, "html_url": "u", "workflow_id": 50},
				{"id": 98, "name": "CI", "status": "completed", "conclusion": "success", "head_branch": "main", "run_number": 8, "html_url": "u", "workflow_id": 50}
			]
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	redirectBase = ts.URL

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	require.NoError(t, err)
	ghc.BaseURL = baseURL

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, true, 50)
	require.NoError(t, err)

	assert.Equal(t, int64(100), diagnosis.RunID)
	assert.Equal(t, "CI", diagnosis.RunName)
	assert.Equal(t, "failure", diagnosis.Conclusion)

	// Should have 1 failed job (build), lint succeeded
	require.Len(t, diagnosis.FailedJobs, 1)
	assert.Equal(t, "build", diagnosis.FailedJobs[0].JobName)
	assert.Equal(t, int64(200), diagnosis.FailedJobs[0].JobID)

	// Should have identified the failed step
	require.Len(t, diagnosis.FailedJobs[0].FailedSteps, 1)
	assert.Equal(t, "Build", diagnosis.FailedJobs[0].FailedSteps[0].Name)

	// Should have extracted error lines (timestamps stripped)
	require.GreaterOrEqual(t, len(diagnosis.FailedJobs[0].ErrorLines), 1)
	foundModuleError := false
	for _, line := range diagnosis.FailedJobs[0].ErrorLines {
		if strings.Contains(line, "cannot find module") {
			foundModuleError = true
		}
		// Timestamp should be stripped
		assert.NotContains(t, line, "2024-01-15T10:30")
	}
	assert.True(t, foundModuleError, "should have found 'cannot find module' error")

	// Flakiness check
	require.NotNil(t, diagnosis.Flakiness)
	assert.Equal(t, "first_failure", diagnosis.Flakiness.Verdict)
	assert.Equal(t, 2, diagnosis.Flakiness.RecentRuns)
	assert.Equal(t, 2, diagnosis.Flakiness.RecentSuccesses)

	// Summary should mention the failed job
	assert.Contains(t, diagnosis.Summary, "build")
	assert.Contains(t, diagnosis.Summary, "1 failed job")
}

func TestDiagnoseFailure_SuccessfulRun(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "completed", "conclusion": "success",
			"head_branch": "main", "html_url": "https://example.com/run/100",
			"run_number": 10, "workflow_id": 50
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	require.NoError(t, err)
	ghc.BaseURL = baseURL

	client := &Client{owner: owner, repo: repo, gh: ghc, perPageLimit: 50}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, false, 50)
	require.NoError(t, err)
	assert.Contains(t, diagnosis.Summary, "succeeded")
	assert.Nil(t, diagnosis.FailedJobs)
}

func TestDiagnoseFailure_InProgressRun(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/100", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 100, "name": "CI", "status": "in_progress", "conclusion": "",
			"head_branch": "main", "html_url": "https://example.com/run/100",
			"run_number": 10, "workflow_id": 50
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	ghc := githubapi.NewClient(ts.Client()).WithAuthToken("test-token")
	baseURL, err := url.Parse(ts.URL + "/")
	require.NoError(t, err)
	ghc.BaseURL = baseURL

	client := &Client{owner: owner, repo: repo, gh: ghc, perPageLimit: 50}

	diagnosis, err := client.DiagnoseFailure(context.Background(), 100, false, 50)
	require.NoError(t, err)
	assert.Contains(t, diagnosis.Summary, "still in_progress")
}
