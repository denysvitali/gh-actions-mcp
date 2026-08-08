package mcp

import (
	"fmt"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"
)

// Plumbing shared by every typed tool handler. The handlers themselves live in
// the handlers_*.go files, one per tool family.

// defaultPerPageLimit is the API page size used when the config does not set one.
const defaultPerPageLimit = 50

// clientFromInput resolves the repository a single call targets and returns a
// client bound to it, along with the resolved owner and repo for error messages.
//
// Resolution order: explicit arguments win over the configured repository, and an
// "owner/repo" string in the repo argument wins over both, so a caller can pass
// a single slug. The returned client is a fresh one — the server's own client is
// only used for the configured repository — which keeps a per-call override from
// leaking into later calls.
func (s *MCPServer) clientFromInput(input repoInput) (*github.Client, string, string, error) {
	owner := strings.TrimSpace(input.Owner)
	repo := strings.TrimSpace(input.Repo)
	if owner == "" {
		owner = s.config.RepoOwner
	}
	if repo == "" {
		repo = s.config.RepoName
	}
	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		if parts[0] != "" && parts[1] != "" {
			owner, repo = parts[0], parts[1]
		}
	}
	if owner == "" || repo == "" {
		return nil, "", "", fmt.Errorf("repository owner/repo not set. Provide owner and repo arguments")
	}
	perPage := s.config.PerPageLimit
	if perPage <= 0 {
		perPage = defaultPerPageLimit
	}
	client, err := github.NewClientWithOptions(github.ClientOptions{
		Token: s.config.Token, Owner: owner, Repo: repo, PerPageLimit: perPage,
		APIBaseURL: s.config.APIBaseURL, UploadURL: s.config.UploadURL, RetryMax: s.config.RetryMax,
		AuthUsername: s.config.AuthUsername,
	})
	return client, owner, repo, err
}

// intValue reads an optional positive integer argument. A missing argument and a
// non-positive one are both "not requested", which the log helpers spell 0.
func intValue(value *int64) int {
	if value == nil || *value <= 0 {
		return 0
	}
	return int(*value)
}
