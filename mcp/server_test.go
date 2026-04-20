package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMCPServer(t *testing.T) {
	logger := logrus.New()
	cfg := &config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}

	server := NewMCPServer(cfg, logger)

	assert.NotNil(t, server)
	assert.NotNil(t, server.srv)
	assert.NotNil(t, server.client)
	assert.NotNil(t, server.config)
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

func TestWorkflowRunJSON(t *testing.T) {
	run := &github.WorkflowRun{
		ID:         12345,
		Name:       "CI",
		Status:     "completed",
		Conclusion: "success",
		Branch:     "main",
		Event:      "push",
		Actor:      "testuser",
		RunNumber:  42,
		WorkflowID: 100,
	}

	data, err := json.Marshal(run)
	require.NoError(t, err)

	var decoded github.WorkflowRun
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, run.ID, decoded.ID)
	assert.Equal(t, run.Name, decoded.Name)
	assert.Equal(t, run.Status, decoded.Status)
	assert.Equal(t, run.Conclusion, decoded.Conclusion)
}

func TestActionsStatusJSON(t *testing.T) {
	status := &github.ActionsStatus{
		TotalWorkflows: 5,
		TotalRuns:      100,
		SuccessfulRuns: 80,
		FailedRuns:     15,
		InProgressRuns: 2,
		QueuedRuns:     1,
		PendingRuns:    2,
		RecentRuns: []*github.WorkflowRun{
			{ID: 1, Name: "CI"},
		},
	}

	data, err := json.Marshal(status)
	require.NoError(t, err)

	var decoded github.ActionsStatus
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, status.TotalWorkflows, decoded.TotalWorkflows)
	assert.Equal(t, status.TotalRuns, decoded.TotalRuns)
	assert.Len(t, decoded.RecentRuns, 1)
}

func TestWorkflowJSON(t *testing.T) {
	workflow := &github.Workflow{
		ID:    12345,
		Name:  "CI",
		Path:  ".github/workflows/ci.yml",
		State: "active",
	}

	data, err := json.Marshal(workflow)
	require.NoError(t, err)

	var decoded github.Workflow
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, workflow.ID, decoded.ID)
	assert.Equal(t, workflow.Name, decoded.Name)
	assert.Equal(t, workflow.Path, decoded.Path)
	assert.Equal(t, workflow.State, decoded.State)
}

// Test that the MCPServer tools are registered correctly
func TestMCPServerTools(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)
	cfg := &config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}

	server := NewMCPServer(cfg, logger)

	// Verify server and its components are properly initialized
	assert.NotNil(t, server)
	assert.NotNil(t, server.GetServer())
}

// Test workflow ID parsing
func TestWorkflowIDParsing(t *testing.T) {
	testCases := []struct {
		input   string
		wantID  int64
		wantErr bool
	}{
		{"12345", 12345, false},
		{"0", 0, false},
		{"abc", 0, true},
		{"", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			id, err := github.ParseWorkflowID(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantID, id)
			}
		})
	}
}

func TestIsValidRunElement(t *testing.T) {
	assert.True(t, isValidRunElement("info"))
	assert.True(t, isValidRunElement("logs"))
	assert.True(t, isValidRunElement("artifact_content"))
	assert.False(t, isValidRunElement("log"))
	assert.False(t, isValidRunElement("unknown"))
}

func TestFailureDiagnosisJSONFromServer(t *testing.T) {
	d := &github.FailureDiagnosis{
		RunID:      42,
		RunName:    "CI",
		RunURL:     "https://example.com/run/42",
		Branch:     "main",
		HeadSHA:    "abc123",
		Conclusion: "failure",
		FailedJobs: []*github.FailedJob{
			{
				JobID:      100,
				JobName:    "build",
				Conclusion: "failure",
				FailedSteps: []*github.FailedStep{
					{Name: "Compile", Number: 3, Conclusion: "failure"},
				},
				ErrorLines: []string{"error: undefined reference"},
			},
		},
		Flakiness: &github.FlakinessInfo{
			RecentRuns:      5,
			RecentFailures:  1,
			RecentSuccesses: 4,
			Verdict:         "first_failure",
		},
		Summary: "1 failed job(s): build.",
	}

	data, err := json.Marshal(d)
	require.NoError(t, err)

	var decoded github.FailureDiagnosis
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, d.RunID, decoded.RunID)
	assert.Equal(t, d.RunName, decoded.RunName)
	assert.Equal(t, d.RunURL, decoded.RunURL)
	assert.Equal(t, d.Branch, decoded.Branch)
	assert.Equal(t, d.Conclusion, decoded.Conclusion)
	assert.Len(t, decoded.FailedJobs, 1)
	assert.Equal(t, "build", decoded.FailedJobs[0].JobName)
	assert.Len(t, decoded.FailedJobs[0].ErrorLines, 1)
	assert.NotNil(t, decoded.Flakiness)
	assert.Equal(t, "first_failure", decoded.Flakiness.Verdict)
}

func TestFormatWorkflowStatusSummary(t *testing.T) {
	status := &github.CombinedCheckStatus{
		State:      "failure",
		TotalCount: 3,
		ByConclusion: map[string]int{
			"failure":     2,
			"in_progress": 1,
		},
		CheckRuns: []*github.CheckRun{
			{ID: 10, Name: "Build", Status: "completed", Conclusion: "failure"},
			{ID: 11, Name: "Lint", Status: "in_progress", Conclusion: ""},
		},
	}

	out := formatWorkflowStatusSummary("main", status, "latest")
	assert.Contains(t, out, "Workflow Status for main")
	assert.Contains(t, out, "Overall: failure")
	assert.Contains(t, out, "Workflows: 3")
	assert.Contains(t, out, "Filter Mode: latest")
	assert.Contains(t, out, "By Conclusion:")
	assert.Contains(t, out, "failure: 2")
	assert.Contains(t, out, "in_progress: 1")
	assert.Contains(t, out, "- Build: completed/failure (id: 10)")
	assert.Contains(t, out, "- Lint: in_progress/- (id: 11)")
}

func TestRepoFromArgs(t *testing.T) {
	server := &MCPServer{
		config: &config.Config{
			RepoOwner: "default-owner",
			RepoName:  "default-repo",
		},
	}

	owner, repo, err := server.repoFromArgs(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "default-owner", owner)
	assert.Equal(t, "default-repo", repo)

	owner, repo, err = server.repoFromArgs(map[string]interface{}{
		"owner": "override-owner",
		"repo":  "override-repo",
	})
	require.NoError(t, err)
	assert.Equal(t, "override-owner", owner)
	assert.Equal(t, "override-repo", repo)
}

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

	server := NewMCPServer(cfg, logger)
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "analyze_timing",
			Arguments: map[string]interface{}{
				"workflow": "CI",
				"branch":   "main",
				"limit":    2,
			},
		},
	}

	result, err := server.analyzeTiming(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)

	text, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)

	var analysis github.TimingAnalysis
	err = json.Unmarshal([]byte(text.Text), &analysis)
	require.NoError(t, err)
	assert.Equal(t, "workflow", analysis.Scope)
	assert.Equal(t, int64(50), analysis.WorkflowID)
	assert.Equal(t, "CI", analysis.WorkflowName)
	assert.Equal(t, 2, analysis.SampleCount)
	assert.Equal(t, int64(103), analysis.Focus.RunID)
	assert.NotEmpty(t, analysis.JobBreakdown)
}
