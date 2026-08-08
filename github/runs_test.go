package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetActionsStatus(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(`{"total_count":2,"workflows":[{"id":1,"name":"CI"},{"id":2,"name":"Release"}]}`))
	mux.HandleFunc("/repos/owner/repo/actions/runs", jsonHandler(`{"total_count":7,"workflow_runs":[
		{"id":1,"status":"completed","conclusion":"success"},
		{"id":2,"status":"completed","conclusion":"failure"},
		{"id":3,"status":"completed","conclusion":"cancelled"},
		{"id":4,"status":"completed","conclusion":"timed_out"},
		{"id":5,"status":"completed","conclusion":"action_required"},
		{"id":6,"status":"in_progress"},
		{"id":7,"status":"queued"},
		{"id":8,"status":"pending"}
	]}`))
	client := newMuxClient(t, mux)

	status, err := client.GetActionsStatus(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 2, status.TotalWorkflows)
	assert.Equal(t, 7, status.TotalRuns, "TotalRuns comes from the API's total_count, not the page length")
	assert.Len(t, status.RecentRuns, 8)
	assert.Equal(t, 1, status.SuccessfulRuns)
	assert.Equal(t, 4, status.FailedRuns, "cancelled, timed_out and action_required all count as failed")
	assert.Equal(t, 1, status.InProgressRuns)
	assert.Equal(t, 1, status.QueuedRuns)
	assert.Equal(t, 1, status.PendingRuns)
}

func TestGetActionsStatus_Errors(t *testing.T) {
	t.Parallel()

	t.Run("workflow listing failure", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).GetActionsStatus(context.Background(), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflows")
	})

	t.Run("run listing failure", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(`{"total_count":0,"workflows":[]}`))
		mux.HandleFunc("/repos/owner/repo/actions/runs", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).GetActionsStatus(context.Background(), 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflow runs")
	})
}

func TestListRepositoryWorkflowRunsPage(t *testing.T) {
	t.Parallel()

	t.Run("every filter is forwarded as a query parameter", func(t *testing.T) {
		t.Parallel()

		var query url.Values
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.Query()
			jsonHandler(`{"total_count":0,"workflow_runs":[]}`)(w, r)
		})
		client := newMuxClient(t, mux)

		_, _, err := client.ListRepositoryWorkflowRunsPage(context.Background(), &ListRunsOptions{
			Branch:       "main",
			Status:       "completed",
			CreatedAfter: ">=2026-01-01",
			Event:        "push",
			Actor:        "alice",
			Per_page:     7,
			Page:         3,
		})
		require.NoError(t, err)
		assert.Equal(t, "main", query.Get("branch"))
		assert.Equal(t, "completed", query.Get("status"))
		assert.Equal(t, ">=2026-01-01", query.Get("created"))
		assert.Equal(t, "push", query.Get("event"))
		assert.Equal(t, "alice", query.Get("actor"))
		assert.Equal(t, "7", query.Get("per_page"))
		assert.Equal(t, "3", query.Get("page"))
	})

	t.Run("conclusion is filtered client-side, never sent to GitHub", func(t *testing.T) {
		t.Parallel()

		var query url.Values
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.Query()
			jsonHandler(`{"total_count":2,"workflow_runs":[
				{"id":1,"status":"completed","conclusion":"success"},
				{"id":2,"status":"completed","conclusion":"failure"}
			]}`)(w, r)
		})
		client := newMuxClient(t, mux)

		runs, _, err := client.ListRepositoryWorkflowRunsPage(context.Background(), &ListRunsOptions{Conclusion: "failure"})
		require.NoError(t, err)
		assert.Empty(t, query.Get("conclusion"))
		require.Len(t, runs, 1)
		assert.Equal(t, int64(2), runs[0].ID)
	})

	t.Run("a workflow ID selects the per-workflow endpoint", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", jsonHandler(`{"total_count":1,"workflow_runs":[{"id":9}]}`))
		client := newMuxClient(t, mux)

		workflowID := int64(50)
		runs, next, err := client.ListRepositoryWorkflowRunsPage(context.Background(), &ListRunsOptions{WorkflowID: &workflowID})
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, 0, next, "a response without a Link header has no next page")
	})

	t.Run("nil options are accepted", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", jsonHandler(`{"total_count":0,"workflow_runs":[]}`))
		runs, next, err := newMuxClient(t, mux).ListRepositoryWorkflowRunsPage(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, runs)
		assert.Equal(t, 0, next)
	})

	t.Run("an API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", statusHandler(http.StatusForbidden))
		_, _, err := newMuxClient(t, mux).ListRepositoryWorkflowRunsPage(context.Background(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflow runs")
	})
}

func TestListRepositoryWorkflowRunsWithOptions(t *testing.T) {
	t.Parallel()

	t.Run("follows pages until the limit is reached and truncates the overflow", func(t *testing.T) {
		t.Parallel()

		var base string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			w.Header().Set("Content-Type", "application/json")
			if page == "" || page == "1" {
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/actions/runs?page=2>; rel="next"`, base))
				_, _ = w.Write([]byte(`{"total_count":4,"workflow_runs":[{"id":1},{"id":2}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"total_count":4,"workflow_runs":[{"id":3},{"id":4}]}`))
		})
		client, url := newMuxClientWithURL(t, mux)
		base = url

		runs, err := client.ListRepositoryWorkflowRunsWithOptions(context.Background(), &ListRunsOptions{Per_page: 3})
		require.NoError(t, err)
		require.Len(t, runs, 3, "the result is truncated to the requested limit")
		assert.Equal(t, int64(1), runs[0].ID)
		assert.Equal(t, int64(3), runs[2].ID)
	})

	t.Run("nil options fall back to the client per-page limit", func(t *testing.T) {
		t.Parallel()

		var perPage string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			perPage = r.URL.Query().Get("per_page")
			jsonHandler(`{"total_count":0,"workflow_runs":[]}`)(w, r)
		})
		client := newMuxClient(t, mux)

		runs, err := client.ListRepositoryWorkflowRunsWithOptions(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, runs)
		assert.Equal(t, "50", perPage)
	})

	t.Run("an API failure is propagated", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).ListRepositoryWorkflowRunsWithOptions(context.Background(), nil)
		require.Error(t, err)
	})
}

func TestGetWorkflowJobs(t *testing.T) {
	t.Parallel()

	const jobsJSON = `{"total_count":3,"jobs":[
		{"id":1,"name":"build","status":"completed","conclusion":"success","run_attempt":1,"run_id":9,
		 "started_at":"2026-01-01T10:00:00Z","completed_at":"2026-01-01T10:05:00Z",
		 "runner_name":"gh-1","runner_group_name":"default","labels":["ubuntu-latest"],
		 "steps":[{"name":"Checkout","number":1,"status":"completed","conclusion":"success","started_at":"2026-01-01T10:00:00Z","completed_at":"2026-01-01T10:00:30Z"}]},
		{"id":2,"name":"retry","status":"completed","conclusion":"success","run_attempt":2,"run_id":9},
		{"id":3,"name":"nolabels","status":"queued","run_attempt":1,"run_id":9}
	]}`

	t.Run("maps jobs and steps, computing durations", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/9/jobs", jsonHandler(jobsJSON))
		jobs, err := newMuxClient(t, mux).GetWorkflowJobs(context.Background(), 9, "", 0)
		require.NoError(t, err)
		require.Len(t, jobs, 3)

		assert.Equal(t, "build", jobs[0].Name)
		assert.InDelta(t, 300.0, jobs[0].DurationSeconds, 0.01)
		assert.Equal(t, "gh-1", jobs[0].RunnerName)
		assert.Equal(t, "default", jobs[0].RunnerGroup)
		assert.Equal(t, []string{"ubuntu-latest"}, jobs[0].Labels)
		assert.Equal(t, int64(9), jobs[0].WorkflowRunID)
		require.Len(t, jobs[0].Steps, 1)
		assert.InDelta(t, 30.0, jobs[0].Steps[0].DurationSeconds, 0.01)

		assert.Zero(t, jobs[2].DurationSeconds, "a job with no timestamps has zero duration")
		assert.Empty(t, jobs[2].Labels)
	})

	t.Run("attempt number filters client-side", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/9/jobs", jsonHandler(jobsJSON))
		jobs, err := newMuxClient(t, mux).GetWorkflowJobs(context.Background(), 9, "", 2)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		assert.Equal(t, "retry", jobs[0].Name)
	})

	t.Run("filter is forwarded to GitHub", func(t *testing.T) {
		t.Parallel()

		var got string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/9/jobs", func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query().Get("filter")
			jsonHandler(`{"total_count":0,"jobs":[]}`)(w, r)
		})
		_, err := newMuxClient(t, mux).GetWorkflowJobs(context.Background(), 9, "latest", 0)
		require.NoError(t, err)
		assert.Equal(t, "latest", got)
	})

	t.Run("an API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/9/jobs", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).GetWorkflowJobs(context.Background(), 9, "", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list jobs for run 9")
	})
}

func TestRerunWorkflowRun(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/rerun", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	assert.NoError(t, newMuxClient(t, mux).RerunWorkflowRun(context.Background(), 1))
}

// TestCancelWorkflowRun_202IsReportedAsAnError pins the CURRENT behaviour for the
// status code GitHub actually returns when a cancellation is accepted (202).
// go-github surfaces 202 as *github.AcceptedError, so a successful cancellation
// is reported as a failure. Pinned here so a refactor cannot change it silently;
// the underlying defect is reported separately, not fixed.
func TestCancelWorkflowRun_202IsReportedAsAnError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs/2/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	client := newMuxClient(t, mux)

	err := client.CancelWorkflowRun(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to cancel workflow run 1")
	assert.Contains(t, err.Error(), "job scheduled on GitHub side")

	// ManageRun folds the same error into a "failed" result rather than returning it.
	result, err := client.ManageRun(context.Background(), 1, ManageRunActionCancel)
	require.NoError(t, err)
	assert.Equal(t, "failed", result.Status)

	// Any other 2xx is treated as success.
	assert.NoError(t, client.CancelWorkflowRun(context.Background(), 2))
}

func TestGetWorkflowRuns(t *testing.T) {
	t.Parallel()

	t.Run("branch is forwarded when set", func(t *testing.T) {
		t.Parallel()

		var branch string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", func(w http.ResponseWriter, r *http.Request) {
			branch = r.URL.Query().Get("branch")
			jsonHandler(`{"total_count":1,"workflow_runs":[{"id":1}]}`)(w, r)
		})
		runs, err := newMuxClient(t, mux).GetWorkflowRuns(context.Background(), 50, "release")
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, "release", branch)
	})

	t.Run("an API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/runs", statusHandler(http.StatusForbidden))
		_, err := newMuxClient(t, mux).GetWorkflowRuns(context.Background(), 50, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflow runs for workflow 50")
	})
}
