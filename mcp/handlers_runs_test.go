package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Handler tests for the workflow/run family, driven against an httptest stand-in
// for the GitHub API.

func TestClientFromTypedRepoInput(t *testing.T) {
	server := &MCPServer{
		config: &config.Config{
			RepoOwner: "default-owner",
			RepoName:  "default-repo",
		},
	}

	_, owner, repo, err := server.clientFromInput(repoInput{})
	require.NoError(t, err)
	assert.Equal(t, "default-owner", owner)
	assert.Equal(t, "default-repo", repo)

	_, owner, repo, err = server.clientFromInput(repoInput{Owner: "override-owner", Repo: "override-repo"})
	require.NoError(t, err)
	assert.Equal(t, "override-owner", owner)
	assert.Equal(t, "override-repo", repo)
}

func TestListRunsTool_OmitsBranchWhenNotProvided(t *testing.T) {
	owner := "octo"
	repo := "hello-world"

	var listRunsBranch string

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		listRunsBranch = r.URL.Query().Get("branch")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"workflow_runs": [
				{
					"id": 301, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "feature/demo", "head_sha": "sha301", "event": "pull_request",
					"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:02:00Z",
					"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/301",
					"run_number": 31, "workflow_id": 88, "actor": {"login": "alice"}
				}
			]
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	server, err := NewMCPServer(&config.Config{
		Token:        "token",
		RepoOwner:    owner,
		RepoName:     repo,
		APIBaseURL:   ts.URL + "/",
		UploadURL:    ts.URL + "/",
		PerPageLimit: 50,
	}, logrus.New())
	require.NoError(t, err)

	_, _, err = server.listRunsTyped(context.Background(), nil, listRunsInput{PerPage: 5})
	require.NoError(t, err)
	assert.Empty(t, listRunsBranch)
}

func TestListRunsTool_ResolvesWorkflowByName(t *testing.T) {
	owner := "octo"
	repo := "hello-world"

	var listRunsPath string
	var listWorkflowsCalled bool

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		listWorkflowsCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"workflows": [
				{"id": 88, "name": "CI", "path": ".github/workflows/ci.yml", "state": "active"},
				{"id": 99, "name": "Release", "path": ".github/workflows/release.yml", "state": "active"}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/88/runs", func(w http.ResponseWriter, r *http.Request) {
		listRunsPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 1,
			"workflow_runs": [
				{
					"id": 301, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha301", "event": "push",
					"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:02:00Z",
					"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/301",
					"run_number": 31, "workflow_id": 88, "actor": {"login": "alice"}
				}
			]
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	server, err := NewMCPServer(&config.Config{
		Token:        "token",
		RepoOwner:    owner,
		RepoName:     repo,
		APIBaseURL:   ts.URL + "/",
		UploadURL:    ts.URL + "/",
		PerPageLimit: 50,
	}, logrus.New())
	require.NoError(t, err)

	_, _, err = server.listRunsTyped(context.Background(), nil, listRunsInput{WorkflowID: "CI", PerPage: 5})
	require.NoError(t, err)
	assert.True(t, listWorkflowsCalled)
	assert.Equal(t, "/repos/"+owner+"/"+repo+"/actions/workflows/88/runs", listRunsPath)
}
