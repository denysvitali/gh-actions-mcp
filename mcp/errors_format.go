package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/config"
	ghapi "github.com/google/go-github/v89/github"
)

// formatAuthErrorWithRepo turns a GitHub API failure into an operator-facing
// message that names the likely token problem for repo.
//
// The branches are ordered from most specific to least specific and are
// evaluated first-match-wins. That order is part of the tool output contract and
// is pinned by errors_format_test.go: an error that matches several branches
// must resolve to the earliest one, so branches may only be appended.
//
// Classification prefers the HTTP status carried by a *ghapi.ErrorResponse. The
// substring checks are the fallback for errors that never carried one: transport
// failures, github.HTTPError and hand-built errors. They are kept because they
// are what the status-bearing path degrades to, and because dropping them would
// change the classification of existing messages.
func (s *MCPServer) formatAuthErrorWithRepo(err error, msg, repo string) string {
	errStr := ""
	if err != nil {
		errStr = strings.ToLower(err.Error())
	}
	status := httpStatusOf(err)

	if strings.Contains(errStr, "resource not accessible by personal access token") {
		return fmt.Sprintf("%s: %v\nGitHub rejected the token for this endpoint.\nFor fine-grained PATs, grant repository access plus:\n- Actions: Read (runs/jobs/logs/artifacts)\nFor classic PATs on private repos, include the 'repo' scope.", msg, err)
	}

	if status == http.StatusUnauthorized || containsAny(errStr, "401", "unauthorized", "bad credentials", "log access unauthorized") {
		return fmt.Sprintf("%s: %v\nGitHub rejected authentication for %s.\nSet a valid GITHUB_TOKEN and ensure it can read Actions data in this repository.", msg, err, repo)
	}

	var rateLimitErr *ghapi.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return fmt.Sprintf("%s: GitHub API rate limit exceeded for %s.\nTry again later or use a token with higher rate limits.", msg, repo)
	}

	if status == http.StatusForbidden || containsAny(errStr, "403", "insufficient", "forbidden") {
		return fmt.Sprintf("%s: %v\nGitHub accepted authentication but denied authorization for %s.\nThe token likely lacks required repository permissions for this operation.", msg, err, repo)
	}

	if status == http.StatusNotFound || strings.Contains(errStr, "404") {
		return fmt.Sprintf("%s: %v\nGitHub returned 404 for %s.\nThis usually means the run/ref/artifact is not in this repository, or the token cannot see a private repository.", msg, err, repo)
	}

	if config.IsAuthenticationError(err) {
		return fmt.Sprintf("authentication failed: %v\nMake sure GITHUB_TOKEN is set (or run 'gh auth login' on macOS) and has access to %s", err, repo)
	}
	return fmt.Sprintf("%s: %v", msg, err)
}

// formatAuthError formats an error message with authentication context, naming
// the repository this server was configured with.
func (s *MCPServer) formatAuthError(err error, msg string) string {
	repo := fmt.Sprintf("%s/%s", s.config.RepoOwner, s.config.RepoName)
	return s.formatAuthErrorWithRepo(err, msg, repo)
}

// formatAuthErrorForRepo formats an error message naming owner/repo, which for
// per-call repository overrides is not necessarily the configured repository.
func (s *MCPServer) formatAuthErrorForRepo(err error, msg, owner, repo string) string {
	return s.formatAuthErrorWithRepo(err, msg, fmt.Sprintf("%s/%s", owner, repo))
}

// httpStatusOf reports the HTTP status code GitHub returned for err, or 0 when
// err did not come from a GitHub API response. Note that *ghapi.RateLimitError
// is deliberately not an *ghapi.ErrorResponse, so rate limits report 0 here and
// are classified by their own branch.
func httpStatusOf(err error) int {
	var response *ghapi.ErrorResponse
	if errors.As(err, &response) && response.Response != nil {
		return response.Response.StatusCode
	}
	return 0
}

// containsAny reports whether haystack contains any of needles.
func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
