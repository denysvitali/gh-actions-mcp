//go:build integration

package tests

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/github"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getTestClient returns a GitHub client configured from environment variables
func getTestClient(t *testing.T) *github.Client {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("GITHUB_TOKEN not set, skipping integration test")
	}

	owner := os.Getenv("GITHUB_OWNER")
	if owner == "" {
		owner = "denysvitali" // default to the project owner
	}

	repo := os.Getenv("GITHUB_REPO")
	if repo == "" {
		repo = "gh-actions-mcp" // default to this repo
	}

	return github.NewClient(token, owner, repo)
}

// getTestWorkflowID returns a workflow ID to use for testing
func getTestWorkflowID(t *testing.T) string {
	id := os.Getenv("GITHUB_WORKFLOW_ID")
	if id == "" {
		id = "CI" // default to a common workflow name
	}
	return id
}

// getTestRef returns a git ref to use for testing
func getTestRef() string {
	ref := os.Getenv("GITHUB_REF")
	if ref == "" {
		ref = "main"
	}
	return ref
}

// TestGetCurrentBranch tests that GetCurrentBranch works correctly
func TestGetCurrentBranch(t *testing.T) {
	branch, err := github.GetCurrentBranch()
	if err != nil {
		// It's OK if we're not in a git repo or in detached HEAD
		if strings.Contains(err.Error(), "not in a git repository") ||
			strings.Contains(err.Error(), "failed to get working directory") {
			t.Skipf("Not in a git repository: %v", err)
		}
		t.Logf("GetCurrentBranch returned error (may be detached HEAD): %v", err)
	}

	if branch != "" {
		t.Logf("Current branch: %s", branch)
	} else {
		t.Log("No branch detected (may be in detached HEAD state)")
	}
}

// TestGetWorkflows tests listing workflows from a real repository
func TestGetWorkflows(t *testing.T) {
	client := getTestClient(t)
	ctx := context.Background()

	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, workflows, "Repository should have at least one workflow")

	t.Logf("Found %d workflows", len(workflows))
	for _, wf := range workflows {
		t.Logf("  - %s (ID: %d, Path: %s, State: %s)", wf.Name, wf.ID, wf.Path, wf.State)
	}
}

// TestGetActionsStatus tests getting the overall actions status
func TestGetActionsStatus(t *testing.T) {
	client := getTestClient(t)
	ctx := context.Background()

	status, err := client.GetActionsStatus(ctx, 10)
	require.NoError(t, err)

	assert.Greater(t, status.TotalWorkflows, 0, "Should have at least one workflow")
	t.Logf("Total workflows: %d", status.TotalWorkflows)
	t.Logf("Total runs: %d", status.TotalRuns)
	t.Logf("Successful runs: %d", status.SuccessfulRuns)
	t.Logf("Failed runs: %d", status.FailedRuns)
	t.Logf("In progress runs: %d", status.InProgressRuns)

	if len(status.RecentRuns) > 0 {
		t.Logf("Recent runs:")
		for _, run := range status.RecentRuns {
			t.Logf("  - Run #%d: %s (%s) on %s by %s", run.RunNumber, run.Name, run.Conclusion, run.Branch, run.Actor)
		}
	}
}

// TestGetWorkflowRuns tests getting runs for a specific workflow
func TestGetWorkflowRuns(t *testing.T) {
	client := getTestClient(t)
	ctx := context.Background()

	// First, get a workflow to test with
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, workflows, "Repository should have at least one workflow")

	workflowID := workflows[0].ID
	workflowName := workflows[0].Name

	runs, err := client.GetWorkflowRuns(ctx, workflowID, "")
	require.NoError(t, err)

	t.Logf("Found %d runs for workflow %s (ID: %d)", len(runs), workflowName, workflowID)
	for _, run := range runs {
		t.Logf("  - Run #%d: %s (%s) on %s", run.RunNumber, run.Status, run.Conclusion, run.Branch)
	}
}

// TestTriggerWorkflowAndWait tests triggering a workflow and waiting for it to complete
func TestTriggerWorkflowAndWait(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := getTestClient(t)
	ctx := context.Background()

	workflowID := getTestWorkflowID(t)
	ref := getTestRef()

	t.Logf("Triggering workflow %s on ref %s", workflowID, ref)

	err := client.TriggerWorkflow(ctx, workflowID, ref)
	if err != nil {
		// Skip if workflow doesn't exist or can't be triggered
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			t.Skipf("Workflow %s not found in repository", workflowID)
		}
		// Skip if the workflow file doesn't have workflow_dispatch event
		if strings.Contains(err.Error(), "workflow_dispatch") || strings.Contains(err.Error(), "400") {
			t.Skipf("Workflow %s does not support workflow_dispatch event", workflowID)
		}
		require.NoError(t, err)
	}

	t.Log("Workflow triggered successfully")

	// Give it a moment to start
	time.Sleep(5 * time.Second)

	// Get the workflow runs to find the one we just triggered
	// Note: This is a simplified approach - in a real scenario you'd want to
	// poll and wait for the new run to appear
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)

	for _, wf := range workflows {
		if wf.Name == workflowID {
			runs, err := client.GetWorkflowRuns(ctx, wf.ID, ref)
			require.NoError(t, err)

			if len(runs) > 0 {
				latestRun := runs[0]
				t.Logf("Latest run: #%d (ID: %d, Status: %s, Conclusion: %s)",
					latestRun.RunNumber, latestRun.ID, latestRun.Status, latestRun.Conclusion)

				// If the run is in progress or queued, wait for it to complete
				if latestRun.Status == "in_progress" || latestRun.Status == "queued" {
					t.Log("Waiting for workflow to complete...")
					result, err := client.WaitForWorkflowRun(ctx, latestRun.ID, 10, 300)
					require.NoError(t, err)

					assert.False(t, result.TimedOut, "Workflow should complete within timeout")
					assert.NotNil(t, result.Run, "Result should contain run info")

					t.Logf("Workflow completed: %s (%s)", result.Run.Conclusion, result.Run.Status)
					t.Logf("Polls: %d, Elapsed: %v", result.PollCount, result.Elapsed)
				}
			}
			break
		}
	}
}

// TestGetWorkflowLogs tests retrieving logs from a workflow run
func TestGetWorkflowLogs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := getTestClient(t)
	ctx := context.Background()

	// First, find a completed workflow run to get logs from
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)

	var runID int64
	for _, wf := range workflows {
		runs, err := client.GetWorkflowRuns(ctx, wf.ID, "")
		require.NoError(t, err)

		for _, run := range runs {
			if run.Status == "completed" {
				runID = run.ID
				t.Logf("Using run #%d (ID: %d) from workflow %s", run.RunNumber, run.ID, wf.Name)
				break
			}
		}
		if runID != 0 {
			break
		}
	}

	if runID == 0 {
		t.Skip("No completed workflow runs found to retrieve logs from")
	}

	// Get all logs
	logs, err := client.GetWorkflowLogs(ctx, runID, 0, 0, false, nil)
	if err != nil {
		// Logs might not be available for various reasons
		t.Logf("Could not retrieve logs: %v", err)
		return
	}

	t.Logf("Retrieved %d bytes of logs", len(logs))
	if len(logs) > 0 {
		// Show first 500 chars
		preview := logs
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		t.Logf("Log preview:\n%s", preview)
	}
}

// TestGetWorkflowLogsWithFilter tests retrieving logs with filtering
func TestGetWorkflowLogsWithFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := getTestClient(t)
	ctx := context.Background()

	// Find a completed workflow run
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)

	var runID int64
	for _, wf := range workflows {
		runs, err := client.GetWorkflowRuns(ctx, wf.ID, "")
		require.NoError(t, err)

		for _, run := range runs {
			if run.Status == "completed" {
				runID = run.ID
				break
			}
		}
		if runID != 0 {
			break
		}
	}

	if runID == 0 {
		t.Skip("No completed workflow runs found")
	}

	// Test filtering by common log patterns
	filterOpts := &github.LogFilterOptions{
		Filter:       "error",
		ContextLines: 2,
	}

	logs, err := client.GetWorkflowLogs(ctx, runID, 0, 0, false, filterOpts)
	if err != nil {
		t.Logf("Could not retrieve logs: %v", err)
		return
	}

	t.Logf("Filtered logs containing 'error' (%d bytes)", len(logs))
	if len(logs) > 0 {
		t.Logf("Filtered log preview:\n%s", logs)
	}
}

// TestWorkflowLifecycle tests the full lifecycle: trigger -> wait -> get logs
func TestWorkflowLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	github.SetLogger(logger)

	client := getTestClient(t)
	ctx := context.Background()

	workflowID := getTestWorkflowID(t)
	ref := getTestRef()

	t.Log("=== Starting workflow lifecycle test ===")

	// Step 1: Trigger the workflow
	t.Log("Step 1: Triggering workflow...")
	err := client.TriggerWorkflow(ctx, workflowID, ref)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			t.Skipf("Workflow %s not found", workflowID)
		}
		if strings.Contains(err.Error(), "workflow_dispatch") || strings.Contains(err.Error(), "400") {
			t.Skipf("Workflow %s does not support workflow_dispatch", workflowID)
		}
		require.NoError(t, err)
	}
	t.Log("Workflow triggered successfully")

	// Step 2: Wait a moment for the workflow to start
	time.Sleep(5 * time.Second)

	// Step 3: Find the triggered run
	t.Log("Step 2: Finding the triggered workflow run...")
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)

	var triggeredRunID int64
	var workflowIDInt int64

	for _, wf := range workflows {
		if wf.Name == workflowID {
			workflowIDInt = wf.ID
			runs, err := client.GetWorkflowRuns(ctx, wf.ID, ref)
			require.NoError(t, err)

			if len(runs) > 0 {
				// Get the most recent run
				triggeredRunID = runs[0].ID
				t.Logf("Found run #%d (ID: %d, Status: %s)", runs[0].RunNumber, runs[0].ID, runs[0].Status)
				break
			}
		}
	}

	if triggeredRunID == 0 {
		t.Skip("Could not find the triggered workflow run")
	}

	// workflowIDInt is used when getting workflow runs
	_ = workflowIDInt

	// Step 4: Wait for completion
	t.Log("Step 3: Waiting for workflow to complete...")
	result, err := client.WaitForWorkflowRun(ctx, triggeredRunID, 10, 300)
	require.NoError(t, err)
	require.NotNil(t, result.Run)

	t.Logf("Workflow completed: %s (%s)", result.Run.Conclusion, result.Run.Status)
	t.Logf("Duration: %v, Polls: %d", result.Elapsed, result.PollCount)

	// Step 5: Get logs
	t.Log("Step 4: Retrieving workflow logs...")
	logs, err := client.GetWorkflowLogs(ctx, triggeredRunID, 100, 0, false, nil)
	if err != nil {
		t.Logf("Could not retrieve logs: %v", err)
	} else {
		t.Logf("Retrieved %d bytes of logs", len(logs))
		if len(logs) > 0 {
			preview := logs
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			t.Logf("Log preview:\n%s", preview)
		}
	}

	t.Log("=== Workflow lifecycle test completed ===")
}

// TestParseWorkflowID tests the ParseWorkflowID helper function
func TestParseWorkflowID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"Valid ID", "12345", 12345, false},
		{"Zero", "0", 0, false},
		{"Large ID", "9223372036854775807", 9223372036854775807, false},
		{"Invalid letters", "abc", 0, true},
		{"Invalid mix", "123abc", 0, true},
		{"Empty string", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := github.ParseWorkflowID(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestGetWorkflowRun tests getting a single workflow run by ID
func TestGetWorkflowRun(t *testing.T) {
	client := getTestClient(t)
	ctx := context.Background()

	// First get a workflow run ID from the list
	workflows, err := client.GetWorkflows(ctx)
	require.NoError(t, err)

	var runID int64
	for _, wf := range workflows {
		runs, err := client.GetWorkflowRuns(ctx, wf.ID, "")
		require.NoError(t, err)

		if len(runs) > 0 {
			runID = runs[0].ID
			break
		}
	}

	if runID == 0 {
		t.Skip("No workflow runs found")
	}

	run, err := client.GetWorkflowRun(ctx, runID)
	require.NoError(t, err)

	assert.NotNil(t, run)
	assert.Equal(t, runID, run.ID)
	t.Logf("Run #%d: %s (%s) on %s", run.RunNumber, run.Name, run.Status, run.Branch)
}

// TestCancelAndRerunWorkflow tests canceling and rerunning a workflow
func TestCancelAndRerunWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Skipping cancel/rerun test to avoid disrupting actual workflows")

	// This test is skipped by default to avoid disrupting running workflows
	// To enable it, remove the Skip above and ensure you have a test workflow
	// that can be safely interrupted and restarted

	client := getTestClient(t)
	ctx := context.Background()

	// This would require triggering a workflow first, then canceling it,
	// then rerunning it. For safety, we skip this test.
	_ = client
	_ = ctx
}
