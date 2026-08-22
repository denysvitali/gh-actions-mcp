package mcp

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/config"
	ghapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rateLimitError(t *testing.T) *ghapi.RateLimitError {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/o/r/actions/runs", nil)
	require.NoError(t, err)
	return &ghapi.RateLimitError{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests, Request: request},
		Message:  "API rate limit exceeded",
		Rate:     ghapi.Rate{Reset: ghapi.Timestamp{Time: time.Now().Add(time.Hour)}},
	}
}

// TestFormatAuthErrorWithRepo_Classification pins the exact string returned by
// every classification branch of formatAuthErrorWithRepo, in branch order. These
// strings are user-facing tool output, so they are part of the wire contract.
func TestFormatAuthErrorWithRepo_Classification(t *testing.T) {
	t.Parallel()

	patErr := errors.New("GET https://api.github.com/repos/o/r/x: 403 Resource not accessible by personal access token []")
	unauthorizedErr := errors.New("failed to get workflow logs: HTTP 401 (log access unauthorized)")
	forbiddenErr := errors.New("GET https://api.github.com/repos/o/r/x: 403 Forbidden []")
	notFoundErr := errors.New("unexpected status code: 404 Not Found")
	authWordErr := errors.New("authentication transport failure")
	plainErr := errors.New("connection reset by peer")
	limitErr := rateLimitError(t)

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "branch 1: fine-grained PAT rejection",
			err:  patErr,
			want: "failed: " + patErr.Error() +
				"\nGitHub rejected the token for this endpoint." +
				"\nFor fine-grained PATs, grant repository access plus:" +
				"\n- Actions: Read (runs/jobs/logs/artifacts)" +
				"\nFor classic PATs on private repos, include the 'repo' scope.",
		},
		{
			name: "branch 2: 401 unauthorized",
			err:  unauthorizedErr,
			want: "failed: " + unauthorizedErr.Error() +
				"\nGitHub rejected authentication for o/r." +
				"\nSet a valid GITHUB_TOKEN and ensure it can read Actions data in this repository.",
		},
		{
			name: "branch 3: rate limit",
			err:  limitErr,
			want: "failed: GitHub API rate limit exceeded for o/r." +
				"\nTry again later or use a token with higher rate limits.",
		},
		{
			name: "branch 4: 403 forbidden",
			err:  forbiddenErr,
			want: "failed: " + forbiddenErr.Error() +
				"\nGitHub accepted authentication but denied authorization for o/r." +
				"\nThe token likely lacks required repository permissions for this operation.",
		},
		{
			name: "branch 5: 404 not found",
			err:  notFoundErr,
			want: "failed: " + notFoundErr.Error() +
				"\nGitHub returned 404 for o/r." +
				"\nThis usually means the run/ref/artifact is not in this repository, or the token cannot see a private repository.",
		},
		{
			name: "branch 6: config.IsAuthenticationError fallback",
			err:  authWordErr,
			want: "authentication failed: " + authWordErr.Error() +
				"\nMake sure GITHUB_TOKEN is set (or run 'gh auth login' on macOS) and has access to o/r",
		},
		{
			name: "branch 7: unclassified passthrough",
			err:  plainErr,
			want: "failed: " + plainErr.Error(),
		},
		{
			name: "nil error falls through to passthrough",
			err:  nil,
			want: "failed: <nil>",
		},
	}

	server := &MCPServer{config: &config.Config{RepoOwner: "o", RepoName: "r"}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, server.formatAuthErrorWithRepo(tc.err, "failed", "o/r"))
		})
	}
}

// TestFormatAuthErrorWithRepo_BranchOrder pins the precedence between branches.
// A single error matching several branches must resolve to the earliest one.
func TestFormatAuthErrorWithRepo_BranchOrder(t *testing.T) {
	t.Parallel()

	server := &MCPServer{config: &config.Config{RepoOwner: "o", RepoName: "r"}}

	tests := []struct {
		name         string
		err          error
		wantContains string
	}{
		{
			name:         "PAT message wins over 403",
			err:          errors.New("403 Resource not accessible by personal access token"),
			wantContains: "GitHub rejected the token for this endpoint",
		},
		{
			name:         "401 wins over 403 and 404",
			err:          errors.New("401 403 404"),
			wantContains: "GitHub rejected authentication for o/r",
		},
		{
			name:         "403 wins over 404",
			err:          errors.New("403 404"),
			wantContains: "denied authorization for o/r",
		},
		{
			// Documents FINDINGS: substring matching means a run ID that merely
			// contains "401" is classified as an authentication failure even
			// though GitHub returned 404.
			name:         "run id containing 401 is misclassified as 401",
			err:          errors.New("GET https://api.github.com/repos/o/r/actions/runs/401999: 404 Not Found"),
			wantContains: "GitHub rejected authentication for o/r",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, server.formatAuthErrorWithRepo(tc.err, "failed", "o/r"), tc.wantContains)
		})
	}
}

// TestFormatAuthErrorRepoResolution pins how the repo slug in the message is
// derived by each of the two wrappers.
func TestFormatAuthErrorRepoResolution(t *testing.T) {
	t.Parallel()

	server := &MCPServer{config: &config.Config{RepoOwner: "cfg-owner", RepoName: "cfg-repo"}}
	err := errors.New("unexpected status code: 404 Not Found")

	assert.Contains(t, server.formatAuthError(err, "failed"), "GitHub returned 404 for cfg-owner/cfg-repo.")
	assert.Contains(t, server.formatAuthErrorForRepo(err, "failed", "arg-owner", "arg-repo"),
		"GitHub returned 404 for arg-owner/arg-repo.")
}

func TestFormatAuthError_PermissionHints(t *testing.T) {
	server := &MCPServer{
		config: &config.Config{
			RepoOwner: "example-owner",
			RepoName:  "example-repo",
		},
	}

	tests := []struct {
		name     string
		msg      string
		err      error
		contains []string
	}{
		{
			name: "403 PAT limitation",
			msg:  "failed to get check status",
			err:  errors.New("GET https://api.github.com/repos/example-owner/example-repo/commits/abc/check-runs: 403 Resource not accessible by personal access token []"),
			contains: []string{
				"GitHub rejected the token for this endpoint",
				"Actions: Read",
				"'repo' scope",
			},
		},
		{
			name: "401 unauthorized logs",
			msg:  "failed to get logs for run 123",
			err:  errors.New("failed to get workflow logs: HTTP 401 (log access unauthorized)"),
			contains: []string{
				"GitHub rejected authentication",
				"example-owner/example-repo",
				"read Actions data",
			},
		},
		{
			name: "404 not found or hidden",
			msg:  "failed to get logs for run 456",
			err:  errors.New("failed to get workflow log URL for run 456: unexpected status code: 404 Not Found"),
			contains: []string{
				"GitHub returned 404",
				"not in this repository",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := server.formatAuthError(tc.err, tc.msg)
			for _, c := range tc.contains {
				assert.Contains(t, out, c)
			}
			// Ensure test data stays sanitized.
			assert.False(t, strings.Contains(strings.ToLower(out), "example-secret-repo"))
		})
	}
}

func TestRedactInternalURLs(t *testing.T) {
	t.Parallel()

	in := "GET http://gh-proxy.gh-proxy.svc.cluster.local/api/repos/denysvitali/not-a-real-repo-xyz/actions/runs/1/jobs?per_page=50: 404 Not Found []"
	out := redactInternalURLs(in)
	assert.Equal(t, "GET <internal> 404 Not Found []", out)
	assert.NotContains(t, out, "gh-proxy")
	assert.Equal(t, "plain", redactInternalURLs("plain"))
}

func TestFormatAuthErrorWithRepo_RedactsClusterURLs(t *testing.T) {
	t.Parallel()

	server := &MCPServer{config: &config.Config{RepoOwner: "o", RepoName: "r"}}
	err := errors.New("GET http://gh-proxy.gh-proxy.svc.cluster.local/api/repos/o/r/actions/runs/1/jobs: 404 Not Found []")
	out := server.formatAuthErrorWithRepo(err, "failed to get jobs for run 1", "o/r")
	assert.Contains(t, out, "GitHub returned 404 for o/r.")
	assert.NotContains(t, out, "gh-proxy")
	assert.Contains(t, out, "<internal>")
}
