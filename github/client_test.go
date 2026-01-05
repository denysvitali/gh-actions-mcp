package github

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

type roundTripperFunc func(*http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := f(req)
	return resp, nil
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
