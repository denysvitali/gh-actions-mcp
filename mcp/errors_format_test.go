package mcp

import (
	"errors"
	"net/http"
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
