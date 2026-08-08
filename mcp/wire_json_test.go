package mcp

import (
	"encoding/json"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin that the package github models this package puts on the wire
// survive a JSON round trip with their field names intact. They live here, next
// to the transport that serialises them, because a tag change in package github
// is only observable as an MCP wire change.

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
