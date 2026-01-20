package mcp

import (
	"context"
	"encoding/json"
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

func TestToolResultHelpers(t *testing.T) {
	t.Run("NewToolResultText", func(t *testing.T) {
		result := mcp.NewToolResultText("test text")
		assert.NotNil(t, result)
		assert.False(t, result.IsError)
		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.Equal(t, "test text", content.Text)
	})

	t.Run("NewToolResultError", func(t *testing.T) {
		result := mcp.NewToolResultError("error message")
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.Equal(t, "error message", content.Text)
	})
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

	// Helper to create a CallToolRequest from args
	makeRequest := func(args map[string]interface{}) mcp.CallToolRequest {
		return mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: args,
			},
		}
	}

	// Test that tools return expected error types for missing args
	testCases := []struct {
		name   string
		args   map[string]interface{}
		assert func(*mcp.CallToolResult) bool
	}{
		{
			name: "get_actions_status with empty args",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				// Should return some result, not error about nil client
				return r != nil
			},
		},
		{
			name: "list_workflows with empty args",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				return r != nil
			},
		},
		{
			name: "get_workflow_runs missing workflow_id",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				if r == nil {
					return false
				}
				content, ok := r.Content[0].(mcp.TextContent)
				if !ok {
					return false
				}
				return content.Text == "workflow_id is required"
			},
		},
		{
			name: "trigger_workflow missing workflow_id",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				if r == nil {
					return false
				}
				content, ok := r.Content[0].(mcp.TextContent)
				if !ok {
					return false
				}
				return content.Text == "workflow_id is required"
			},
		},
		{
			name: "cancel_workflow_run missing run_id",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				if r == nil {
					return false
				}
				content, ok := r.Content[0].(mcp.TextContent)
				if !ok {
					return false
				}
				return content.Text == "run_id is required"
			},
		},
		{
			name: "rerun_workflow missing run_id",
			args: map[string]interface{}{},
			assert: func(r *mcp.CallToolResult) bool {
				if r == nil {
					return false
				}
				content, ok := r.Content[0].(mcp.TextContent)
				if !ok {
					return false
				}
				return content.Text == "run_id is required"
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result *mcp.CallToolResult
			var err error

			switch tc.name {
			case "get_actions_status with empty args":
				result, err = server.getActionsStatus(context.Background(), makeRequest(tc.args))
			case "list_workflows with empty args":
				result, err = server.listWorkflows(context.Background(), makeRequest(tc.args))
			case "get_workflow_runs missing workflow_id":
				result, err = server.getWorkflowRuns(context.Background(), makeRequest(tc.args))
			case "trigger_workflow missing workflow_id":
				result, err = server.triggerWorkflow(context.Background(), makeRequest(tc.args))
			case "cancel_workflow_run missing run_id":
				result, err = server.cancelWorkflowRun(context.Background(), makeRequest(tc.args))
			case "rerun_workflow missing run_id":
				result, err = server.rerunWorkflow(context.Background(), makeRequest(tc.args))
			}

			assert.NoError(t, err)
			assert.True(t, tc.assert(result), "assertion failed for %s", tc.name)
		})
	}
}

// Test GetActionsStatus with mocked data
func TestGetActionsStatusWithMockData(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	// Create a real GitHub client that will fail (no token), but we can test the error path
	cfg := &config.Config{
		Token:     "invalid-token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}

	server := NewMCPServer(cfg, logger)

	// Call with empty args - should get an error from GitHub API
	result, err := server.getActionsStatus(context.Background(), mcp.CallToolRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	// The result should contain an error since the token is invalid
	content, ok := result.Content[0].(mcp.TextContent)
	assert.True(t, ok)
	// Should contain either the original error or the authentication error message
	assert.True(t, strings.Contains(content.Text, "failed to get actions status") ||
		strings.Contains(content.Text, "authentication failed"))
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

// Test context handling
func TestContextHandling(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	cfg := &config.Config{
		Token:     "test-token",
		RepoOwner: "test-owner",
		RepoName:  "test-repo",
	}

	server := NewMCPServer(cfg, logger)

	// All methods should accept context and work with empty args
	methods := []struct {
		name string
		fn   func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}{
		{"get_actions_status", server.getActionsStatus},
		{"list_workflows", server.listWorkflows},
		{"get_workflow_runs", server.getWorkflowRuns},
		{"trigger_workflow", server.triggerWorkflow},
		{"cancel_workflow_run", server.cancelWorkflowRun},
		{"rerun_workflow", server.rerunWorkflow},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			result, err := m.fn(context.Background(), mcp.CallToolRequest{})
			assert.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
}

// Test error scenarios for MCP server tools
func TestMCPServerErrorScenarios(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	t.Run("Authentication error formatting", func(t *testing.T) {
		cfg := &config.Config{
			Token:     "invalid-token",
			RepoOwner: "owner",
			RepoName:  "repo",
		}

		server := NewMCPServer(cfg, logger)

		// Test that auth errors are properly formatted
		result, err := server.getActionsStatus(context.Background(), mcp.CallToolRequest{})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		// Should mention authentication or the original error
		assert.Contains(t, content.Text, "failed")
	})

	t.Run("Invalid workflow ID formats", func(t *testing.T) {
		cfg := &config.Config{
			Token:     "test-token",
			RepoOwner: "test-owner",
			RepoName:  "test-repo",
		}

		server := NewMCPServer(cfg, logger)

		// Test with invalid workflow ID
		result, err := server.getWorkflowRuns(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]interface{}{
					"workflow_id": "invalid-workflow-12345",
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Should get an error result
		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.NotEmpty(t, content.Text)
	})

	t.Run("Invalid run ID", func(t *testing.T) {
		cfg := &config.Config{
			Token:     "test-token",
			RepoOwner: "test-owner",
			RepoName:  "test-repo",
		}

		server := NewMCPServer(cfg, logger)

		// Test cancel with invalid run ID
		result, err := server.cancelWorkflowRun(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]interface{}{
					"run_id": float64(999999999),
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Should get an error result
		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.NotEmpty(t, content.Text)
	})
}

// Test getWorkflowLogs error scenarios
func TestGetWorkflowLogsErrorScenarios(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	cfg := &config.Config{
		Token:     "test-token",
		RepoOwner: "test-owner",
		RepoName:  "test-repo",
	}

	server := NewMCPServer(cfg, logger)

	t.Run("Missing run_id", func(t *testing.T) {
		result, err := server.getWorkflowLogs(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]interface{}{},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.Contains(t, content.Text, "run_id is required")
	})

	t.Run("Mutually exclusive filters", func(t *testing.T) {
		result, err := server.getWorkflowLogs(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]interface{}{
					"run_id":       float64(123),
					"filter":       "error",
					"filter_regex": "[Ee]rror",
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.Contains(t, content.Text, "mutually exclusive")
	})

	t.Run("Invalid run ID", func(t *testing.T) {
		result, err := server.getWorkflowLogs(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]interface{}{
					"run_id": float64(999999999),
				},
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Should get an error result (not found or auth error)
		content, ok := result.Content[0].(mcp.TextContent)
		assert.True(t, ok)
		assert.NotEmpty(t, content.Text)
	})
}

