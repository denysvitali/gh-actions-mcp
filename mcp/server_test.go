package mcp

import (
	"encoding/json"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/denysvitali/gh-actions-mcp/github"

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

