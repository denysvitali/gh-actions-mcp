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

// Handler tests for the analysis family, driven against an httptest stand-in for
// the GitHub API.

func TestAnalyzeTimingTool(t *testing.T) {
	const (
		owner = "test-owner"
		repo  = "test-repo"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 1,
			"workflows": [
				{"id": 50, "name": "CI", "path": ".github/workflows/ci.yml", "state": "active"}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"workflow_runs": [
				{
					"id": 103, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha103", "event": "push",
					"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:10:00Z",
					"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/103",
					"run_number": 13, "workflow_id": 50, "actor": {"login": "alice"}
				},
				{
					"id": 102, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha102", "event": "push",
					"created_at": "2026-04-19T10:00:00Z", "updated_at": "2026-04-19T10:08:00Z",
					"run_started_at": "2026-04-19T10:00:00Z", "html_url": "https://example.com/run/102",
					"run_number": 12, "workflow_id": 50, "actor": {"login": "alice"}
				}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/103/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{
					"id": 203, "name": "build", "status": "completed", "conclusion": "success", "run_id": 103,
					"started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:05:00Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:00:30Z"},
						{"name": "Unit Tests", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:30Z", "completed_at": "2026-04-20T10:05:00Z"}
					]
				},
				{
					"id": 204, "name": "lint", "status": "completed", "conclusion": "success", "run_id": 103,
					"started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:01:40Z",
					"steps": [
						{"name": "golangci-lint", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-20T10:00:00Z", "completed_at": "2026-04-20T10:01:40Z"}
					]
				}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/102/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"jobs": [
				{
					"id": 202, "name": "build", "status": "completed", "conclusion": "success", "run_id": 102,
					"started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:04:00Z",
					"steps": [
						{"name": "Checkout", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:00:20Z"},
						{"name": "Unit Tests", "number": 2, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:20Z", "completed_at": "2026-04-19T10:04:00Z"}
					]
				},
				{
					"id": 212, "name": "lint", "status": "completed", "conclusion": "success", "run_id": 102,
					"started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:01:20Z",
					"steps": [
						{"name": "golangci-lint", "number": 1, "status": "completed", "conclusion": "success", "started_at": "2026-04-19T10:00:00Z", "completed_at": "2026-04-19T10:01:20Z"}
					]
				}
			]
		}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	cfg := &config.Config{
		Token:        "token",
		RepoOwner:    owner,
		RepoName:     repo,
		APIBaseURL:   ts.URL + "/",
		UploadURL:    ts.URL + "/",
		PerPageLimit: 50,
	}

	server, err := NewMCPServer(cfg, logger)
	require.NoError(t, err)
	_, analysis, err := server.analyzeTimingTyped(context.Background(), nil, analyzeTimingInput{
		Workflow: "CI", Branch: "main", Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "workflow", analysis.Scope)
	assert.Equal(t, int64(50), analysis.WorkflowID)
	assert.Equal(t, "CI", analysis.WorkflowName)
	assert.Equal(t, 2, analysis.SampleCount)
	assert.Equal(t, int64(103), analysis.Focus.RunID)
	assert.NotEmpty(t, analysis.JobBreakdown)
}

func TestAnalyzeTimingTool_OmitsBranchWhenNotProvided(t *testing.T) {
	owner := "octo"
	repo := "hello-world"

	var listRunsBranch string

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/103", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 103, "name": "CI", "status": "completed", "conclusion": "success",
			"head_branch": "feature/test", "head_sha": "sha103", "event": "pull_request",
			"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:05:00Z",
			"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/103",
			"run_number": 13, "workflow_id": 50, "actor": {"login": "alice"}
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
		listRunsBranch = r.URL.Query().Get("branch")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total_count": 2,
			"workflow_runs": [
				{
					"id": 103, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "feature/test", "head_sha": "sha103", "event": "pull_request",
					"created_at": "2026-04-20T10:00:00Z", "updated_at": "2026-04-20T10:05:00Z",
					"run_started_at": "2026-04-20T10:00:00Z", "html_url": "https://example.com/run/103",
					"run_number": 13, "workflow_id": 50, "actor": {"login": "alice"}
				},
				{
					"id": 102, "name": "CI", "status": "completed", "conclusion": "success",
					"head_branch": "main", "head_sha": "sha102", "event": "push",
					"created_at": "2026-04-19T10:00:00Z", "updated_at": "2026-04-19T10:04:00Z",
					"run_started_at": "2026-04-19T10:00:00Z", "html_url": "https://example.com/run/102",
					"run_number": 12, "workflow_id": 50, "actor": {"login": "bob"}
				}
			]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/102/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"jobs":[{"id":202,"name":"build","status":"completed","conclusion":"success","run_id":102,"started_at":"2026-04-19T10:00:00Z","completed_at":"2026-04-19T10:03:00Z","steps":[{"name":"Unit Tests","number":1,"status":"completed","conclusion":"success","started_at":"2026-04-19T10:00:00Z","completed_at":"2026-04-19T10:03:00Z"}]}]}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs/103/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"jobs":[{"id":203,"name":"build","status":"completed","conclusion":"success","run_id":103,"started_at":"2026-04-20T10:00:00Z","completed_at":"2026-04-20T10:04:00Z","steps":[{"name":"Unit Tests","number":1,"status":"completed","conclusion":"success","started_at":"2026-04-20T10:00:00Z","completed_at":"2026-04-20T10:04:00Z"}]}]}`))
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

	runID := int64(103)
	_, _, err = server.analyzeTimingTyped(context.Background(), nil, analyzeTimingInput{RunID: &runID, Limit: 2})
	require.NoError(t, err)
	assert.Empty(t, listRunsBranch)
}
