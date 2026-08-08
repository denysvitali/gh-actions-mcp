package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForRunFailsFastOnFailedJob(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"status":"in_progress","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/1"}`))
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"jobs":[{"id":10,"status":"completed","conclusion":"success"},{"id":11,"status":"in_progress","steps":[{"name":"Unit Tests","status":"completed","conclusion":"failure"}]}]}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	client, err := NewClientWithOptions(ClientOptions{Token: "token", Owner: "owner", Repo: "repo", APIBaseURL: ts.URL + "/", UploadURL: ts.URL + "/"})
	require.NoError(t, err)

	result, err := client.WaitForRun(context.Background(), 1, 1)
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	assert.Equal(t, "failure", result.Conclusion)
}

func TestWaitForAllWaitsForJobsRegardlessOfStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":2,"status":"in_progress","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/2"}`))
	})
	mux.HandleFunc("/repos/owner/repo/actions/runs/2/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"jobs":[{"id":20,"status":"completed","conclusion":"failure"},{"id":21,"status":"completed","conclusion":"cancelled"}]}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	client, err := NewClientWithOptions(ClientOptions{Token: "token", Owner: "owner", Repo: "repo", APIBaseURL: ts.URL + "/", UploadURL: ts.URL + "/"})
	require.NoError(t, err)

	result, err := client.WaitForAll(context.Background(), 2, 1)
	require.NoError(t, err)
	assert.Equal(t, "completed", result.Status)
	assert.Empty(t, result.Conclusion)
}

// newMuxClient returns a Client whose API base URL points at mux. The retry and
// cache transports are wired exactly as in production so tests exercise the real
// transport stack.
func newMuxClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	client, _ := newMuxClientWithURL(t, mux)
	return client
}

// newMuxClientWithURL additionally returns the server's base URL, which handlers
// that issue pre-signed-style 302 redirects need in order to build a Location.
func newMuxClientWithURL(t *testing.T, mux *http.ServeMux) (*Client, string) {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	client, err := NewClientWithOptions(ClientOptions{
		Token: "token", Owner: "owner", Repo: "repo",
		APIBaseURL: ts.URL + "/", UploadURL: ts.URL + "/",
	})
	require.NoError(t, err)
	return client, ts.URL
}

func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func statusHandler(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}
}

func TestWaitForRun_TerminatesOnFirstPoll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		waitAll        bool
		run            string
		jobs           string
		wantStatus     string
		wantConclusion string
	}{
		{
			name:           "run already completed ends a normal wait",
			run:            `{"id":1,"status":"completed","conclusion":"success","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:05:00Z","html_url":"https://example.com/run/1"}`,
			jobs:           `{"total_count":0,"jobs":[]}`,
			wantStatus:     "completed",
			wantConclusion: "success",
		},
		{
			name:           "failed job conclusion ends a normal wait early",
			run:            `{"id":1,"status":"in_progress","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/1"}`,
			jobs:           `{"total_count":1,"jobs":[{"id":10,"status":"completed","conclusion":"timed_out"}]}`,
			wantStatus:     "completed",
			wantConclusion: "timed_out",
		},
		{
			// The job has no conclusion yet, so the failed *step*'s conclusion is
			// reported as the run conclusion.
			name:           "failed step supplies the conclusion when the job has none",
			run:            `{"id":1,"status":"in_progress","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/1"}`,
			jobs:           `{"total_count":1,"jobs":[{"id":10,"status":"in_progress","steps":[{"name":"Build","status":"completed","conclusion":"timed_out"}]}]}`,
			wantStatus:     "completed",
			wantConclusion: "timed_out",
		},
		{
			name:           "wait_all with no jobs falls back to the run status",
			waitAll:        true,
			run:            `{"id":1,"status":"completed","conclusion":"failure","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/1"}`,
			jobs:           `{"total_count":0,"jobs":[]}`,
			wantStatus:     "completed",
			wantConclusion: "failure",
		},
		{
			// A normal wait ignores job state once the run itself is completed.
			name:           "completed run with a failed job still reports the run conclusion",
			run:            `{"id":1,"status":"completed","conclusion":"failure","created_at":"2026-01-01T00:00:00Z","html_url":"https://example.com/run/1"}`,
			jobs:           `{"total_count":1,"jobs":[{"id":10,"status":"completed","conclusion":"failure"}]}`,
			wantStatus:     "completed",
			wantConclusion: "failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(tt.run))
			mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", jsonHandler(tt.jobs))
			client := newMuxClient(t, mux)

			var (
				result *WaitRunResult
				err    error
			)
			if tt.waitAll {
				result, err = client.WaitForAll(context.Background(), 1, 1)
			} else {
				result, err = client.WaitForRun(context.Background(), 1, 1)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status)
			assert.Equal(t, tt.wantConclusion, result.Conclusion)
			assert.False(t, result.TimeoutReached)
			assert.Equal(t, "https://example.com/run/1", result.RunURL)
		})
	}
}

func TestWaitForRun_CancelledContext(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(`{"id":1,"status":"in_progress"}`))
	client := newMuxClient(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := client.WaitForRun(ctx, 1, 1)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, "cancelled", result.Status)
	assert.False(t, result.TimeoutReached)
}

func TestWaitForRun_APIErrors(t *testing.T) {
	t.Parallel()

	t.Run("run lookup failure aborts with a nil result", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		result, err := client.WaitForRun(context.Background(), 1, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get workflow run 1")
		assert.Nil(t, result)
	})

	t.Run("job lookup failure aborts with a nil result", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(`{"id":1,"status":"in_progress"}`))
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/jobs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		result, err := client.WaitForRun(context.Background(), 1, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get jobs for run 1")
		assert.Nil(t, result)
	})
}

func TestWaitForWorkflowRun(t *testing.T) {
	t.Parallel()

	t.Run("returns on the first completed poll", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(`{"id":1,"status":"completed","conclusion":"success"}`))
		client := newMuxClient(t, mux)

		result, err := client.WaitForWorkflowRun(context.Background(), 1, 0, 0)
		require.NoError(t, err)
		require.NotNil(t, result.Run)
		assert.Equal(t, 1, result.PollCount)
		assert.False(t, result.TimedOut)
	})

	t.Run("cancelled context returns ctx.Err with a zero-poll result", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1", jsonHandler(`{"id":1,"status":"in_progress"}`))
		client := newMuxClient(t, mux)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := client.WaitForWorkflowRun(ctx, 1, 1, 1)
		require.ErrorIs(t, err, context.Canceled)
		require.NotNil(t, result)
		assert.Equal(t, 0, result.PollCount)
	})

	t.Run("run lookup failure returns a nil result", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		result, err := client.WaitForWorkflowRun(context.Background(), 1, 1, 1)
		require.Error(t, err)
		assert.Nil(t, result)
	})
}

func TestWaitForCommitChecks(t *testing.T) {
	t.Parallel()

	t.Run("returns once every check run is completed", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", jsonHandler(`{"total_count":2,"workflow_runs":[
			{"id":1,"name":"CI","status":"completed","conclusion":"success","head_sha":"abcdef1234567890","run_number":2},
			{"id":2,"name":"Lint","status":"completed","conclusion":"failure","head_sha":"abcdef1234567890","run_number":1}
		]}`))
		client := newMuxClient(t, mux)

		result, err := client.WaitForCommitChecks(context.Background(), "abcdef1234567890", 1)
		require.NoError(t, err)
		assert.Equal(t, "failure", result.OverallConclusion)
		assert.Equal(t, 2, result.ChecksTotal)
		assert.Equal(t, map[string]int{"success": 1, "failure": 1}, result.ChecksByConclusion)
		assert.False(t, result.TimeoutReached)
	})

	t.Run("cancelled context reports a cancelled conclusion", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", jsonHandler(`{"total_count":0,"workflow_runs":[]}`))
		client := newMuxClient(t, mux)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := client.WaitForCommitChecks(ctx, "abcdef1234567890", 1)
		require.ErrorIs(t, err, context.Canceled)
		require.NotNil(t, result)
		assert.Equal(t, "cancelled", result.OverallConclusion)
	})

	t.Run("check lookup failure aborts with a nil result", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", statusHandler(http.StatusForbidden))
		client := newMuxClient(t, mux)

		result, err := client.WaitForCommitChecks(context.Background(), "abcdef1234567890", 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get check runs")
		assert.Nil(t, result)
	})
}

func TestManageRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		action      ManageRunAction
		path        string
		wantMessage string
	}{
		{
			name:        "cancel",
			action:      ManageRunActionCancel,
			path:        "/repos/owner/repo/actions/runs/1/cancel",
			wantMessage: "Successfully cancelled workflow run 1",
		},
		{
			name:        "rerun",
			action:      ManageRunActionRerun,
			path:        "/repos/owner/repo/actions/runs/1/rerun",
			wantMessage: "Successfully triggered rerun for workflow run 1",
		},
		{
			name:        "rerun_failed",
			action:      ManageRunActionRerunFailed,
			path:        "/repos/owner/repo/actions/runs/1/rerun-failed-jobs",
			wantMessage: "Successfully triggered rerun of failed jobs for workflow run 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc(tt.path, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
			})
			client := newMuxClient(t, mux)

			result, err := client.ManageRun(context.Background(), 1, tt.action)
			require.NoError(t, err)
			assert.Equal(t, "success", result.Status)
			assert.Equal(t, tt.action, result.Action)
			assert.Equal(t, int64(1), result.RunID)
			assert.Equal(t, tt.wantMessage, result.Message)
		})
	}

	t.Run("unknown action is the only error return", func(t *testing.T) {
		t.Parallel()

		client := newMuxClient(t, http.NewServeMux())
		result, err := client.ManageRun(context.Background(), 1, ManageRunAction("explode"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown action: explode")
		assert.Nil(t, result)
	})

	t.Run("an API failure is reported in the result, not as an error", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/cancel", statusHandler(http.StatusConflict))
		client := newMuxClient(t, mux)

		result, err := client.ManageRun(context.Background(), 1, ManageRunActionCancel)
		require.NoError(t, err)
		assert.Equal(t, "failed", result.Status)
		assert.NotEmpty(t, result.Message)
	})
}

// TestNextWaitAction exercises the poll decision directly. It is the whole point
// of extracting nextWaitAction: the timeout and job-failure decisions no longer
// need a server or a 15-second poll interval to reach.
func TestNextWaitAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mode           waitMode
		run            *WorkflowRun
		jobs           []*Job
		wantDone       bool
		wantConclusion string
		wantReason     waitReason
	}{
		{
			name:           "fail-fast: completed run ends the wait with the run conclusion",
			mode:           waitModeFailFast,
			run:            &WorkflowRun{Status: "completed", Conclusion: "success"},
			wantDone:       true,
			wantConclusion: "success",
			wantReason:     waitReasonRunCompleted,
		},
		{
			name: "fail-fast: in-progress run with no failures keeps polling",
			mode: waitModeFailFast,
			run:  &WorkflowRun{Status: "in_progress"},
			jobs: []*Job{
				{Status: "completed", Conclusion: "success"},
				{Status: "in_progress"},
			},
		},
		{
			name:           "fail-fast: a failed job conclusion ends the wait",
			mode:           waitModeFailFast,
			run:            &WorkflowRun{Status: "in_progress"},
			jobs:           []*Job{{Status: "completed", Conclusion: "failure"}},
			wantDone:       true,
			wantConclusion: "failure",
			wantReason:     waitReasonJobFailed,
		},
		{
			name:           "fail-fast: a timed-out job conclusion ends the wait",
			mode:           waitModeFailFast,
			run:            &WorkflowRun{Status: "in_progress"},
			jobs:           []*Job{{Status: "completed", Conclusion: "timed_out"}},
			wantDone:       true,
			wantConclusion: "timed_out",
			wantReason:     waitReasonJobFailed,
		},
		{
			name: "fail-fast: a failed step supplies the conclusion when the job has none",
			mode: waitModeFailFast,
			run:  &WorkflowRun{Status: "in_progress"},
			jobs: []*Job{{Status: "in_progress", Steps: []*Step{
				{Conclusion: "success"},
				{Conclusion: "failure"},
			}}},
			wantDone:       true,
			wantConclusion: "failure",
			wantReason:     waitReasonJobFailed,
		},
		{
			name: "fail-fast: a cancelled job is not a failure",
			mode: waitModeFailFast,
			run:  &WorkflowRun{Status: "in_progress"},
			jobs: []*Job{{Status: "completed", Conclusion: "cancelled"}},
		},
		{
			name: "fail-fast: the first failing job wins",
			mode: waitModeFailFast,
			run:  &WorkflowRun{Status: "in_progress"},
			jobs: []*Job{
				{Status: "completed", Conclusion: "timed_out"},
				{Status: "completed", Conclusion: "failure"},
			},
			wantDone:       true,
			wantConclusion: "timed_out",
			wantReason:     waitReasonJobFailed,
		},
		{
			name:           "wait-all: every job completed ends the wait",
			mode:           waitModeAllJobs,
			run:            &WorkflowRun{Status: "in_progress", Conclusion: "failure"},
			jobs:           []*Job{{Status: "completed"}, {Status: "completed"}},
			wantDone:       true,
			wantConclusion: "failure",
			wantReason:     waitReasonAllJobsDone,
		},
		{
			name: "wait-all: one unfinished job keeps polling even if another failed",
			mode: waitModeAllJobs,
			run:  &WorkflowRun{Status: "in_progress"},
			jobs: []*Job{{Status: "completed", Conclusion: "failure"}, {Status: "queued"}},
		},
		{
			name:           "wait-all: no jobs falls back to the run status",
			mode:           waitModeAllJobs,
			run:            &WorkflowRun{Status: "completed", Conclusion: "success"},
			wantDone:       true,
			wantConclusion: "success",
			wantReason:     waitReasonAllJobsDone,
		},
		{
			name: "wait-all: no jobs and an unfinished run keeps polling",
			mode: waitModeAllJobs,
			run:  &WorkflowRun{Status: "queued"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nextWaitAction(tt.mode, tt.run, tt.jobs)
			assert.Equal(t, tt.wantDone, got.done)
			assert.Equal(t, tt.wantConclusion, got.conclusion)
			if tt.wantDone {
				assert.Equal(t, tt.wantReason, got.reason)
			} else {
				assert.Equal(t, waitReasonNotDone, got.reason)
			}
		})
	}
}

func TestFirstFailedStepConclusion(t *testing.T) {
	t.Parallel()

	assert.Empty(t, firstFailedStepConclusion(nil))
	assert.Empty(t, firstFailedStepConclusion([]*Step{{Conclusion: "success"}, {Conclusion: "skipped"}}))
	assert.Equal(t, "failure", firstFailedStepConclusion([]*Step{{Conclusion: "success"}, {Conclusion: "failure"}}))
	assert.Equal(t, "timed_out", firstFailedStepConclusion([]*Step{{Conclusion: "timed_out"}, {Conclusion: "failure"}}))
}

func TestAllJobsCompleted(t *testing.T) {
	t.Parallel()

	assert.True(t, allJobsCompleted(nil), "an empty set is vacuously complete")
	assert.True(t, allJobsCompleted([]*Job{{Status: "completed"}}))
	assert.False(t, allJobsCompleted([]*Job{{Status: "completed"}, {Status: "in_progress"}}))
}

func TestCopyConclusionCounts(t *testing.T) {
	t.Parallel()

	source := map[string]int{"success": 2}
	copied := copyConclusionCounts(source)
	copied["success"] = 99
	assert.Equal(t, 2, source["success"], "the copy must not alias the source")
	assert.NotNil(t, copyConclusionCounts(nil))
}
