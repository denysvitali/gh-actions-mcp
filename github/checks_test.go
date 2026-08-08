package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCheckRunsForRef_UsesWorkflowRunsNotChecksAPI(t *testing.T) {
	const (
		owner = "example-owner"
		repo  = "example-repo"
	)

	checksEndpointCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "main", r.URL.Query().Get("branch"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
		  "total_count": 3,
		  "workflow_runs": [
		    {"id": 11, "name": "Build", "status": "completed", "conclusion": "failure", "run_number": 11, "html_url": "https://example.test/r/11"},
		    {"id": 10, "name": "Build", "status": "completed", "conclusion": "success", "run_number": 10, "html_url": "https://example.test/r/10"},
		    {"id": 12, "name": "Lint",  "status": "in_progress", "conclusion": null, "run_number": 5, "html_url": "https://example.test/r/12"}
		  ]
		}`))
	})
	mux.HandleFunc("/repos/"+owner+"/"+repo+"/commits/main/check-runs", func(w http.ResponseWriter, r *http.Request) {
		checksEndpointCalled = true
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"should not be called"}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	baseURL := ts.URL + "/"
	ghc, err := githubapi.NewClient(githubapi.WithHTTPClient(ts.Client()), githubapi.WithAuthToken("test-token"), githubapi.WithURLs(&baseURL, nil))
	require.NoError(t, err)

	client := &Client{
		owner:        owner,
		repo:         repo,
		gh:           ghc,
		perPageLimit: 50,
	}

	status, err := client.GetCheckRunsForRef(context.Background(), "main", &GetCheckRunsOptions{Filter: "latest"})
	assert.NoError(t, err)
	assert.False(t, checksEndpointCalled)
	assert.Equal(t, 2, status.TotalCount)
	assert.Equal(t, "pending", status.State)
	assert.Equal(t, 1, status.ByConclusion["failure"])
	assert.Equal(t, 1, status.ByConclusion["in_progress"])
}

// makeArtifactZIP creates an in-memory ZIP archive with the given files.

func TestIsLikelyCommitRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref  string
		want bool
	}{
		{ref: "abcdef1", want: true},                                    // 7 hex chars: the minimum
		{ref: "ABCDEF1", want: true},                                    // upper-case hex is accepted
		{ref: "0123456789abcdef0123456789abcdef01234567", want: true},   // 40 hex chars: the maximum
		{ref: "abcdef", want: false},                                    // too short
		{ref: "0123456789abcdef0123456789abcdef012345678", want: false}, // too long
		{ref: "main", want: false},
		{ref: "release/1.0", want: false},
		{ref: "deadbeeg", want: false}, // 'g' is not hex
		{ref: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isLikelyCommitRef(tt.ref))
		})
	}
}

func TestDetermineOverallState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		runs []*CheckRun
		want string
	}{
		{name: "no checks is pending", runs: nil, want: "pending"},
		{
			name: "an in-progress check outranks everything else",
			runs: []*CheckRun{
				{Status: "completed", Conclusion: "failure"},
				{Status: "in_progress"},
			},
			want: "pending",
		},
		{
			name: "queued counts as pending",
			runs: []*CheckRun{{Status: "queued"}},
			want: "pending",
		},
		{
			name: "failure outranks success",
			runs: []*CheckRun{
				{Status: "completed", Conclusion: "success"},
				{Status: "completed", Conclusion: "failure"},
			},
			want: "failure",
		},
		{
			name: "timed_out counts as failure",
			runs: []*CheckRun{{Status: "completed", Conclusion: "timed_out"}},
			want: "failure",
		},
		{
			name: "all successful is success",
			runs: []*CheckRun{{Status: "completed", Conclusion: "success"}},
			want: "success",
		},
		{
			name: "only skipped or cancelled checks are neutral",
			runs: []*CheckRun{
				{Status: "completed", Conclusion: "skipped"},
				{Status: "completed", Conclusion: "cancelled"},
			},
			want: "neutral",
		},
	}

	client := &Client{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, client.determineOverallState(tt.runs))
		})
	}
}

func TestGetCheckRunsForRef_Filtering(t *testing.T) {
	t.Parallel()

	const runsJSON = `{"total_count":5,"workflow_runs":[
		{"id":1,"name":"CI","status":"completed","conclusion":"success","head_sha":"ABCDEF1234567890","run_number":1,"html_url":"https://example.com/1"},
		{"id":2,"name":"CI","status":"completed","conclusion":"failure","head_sha":"abcdef1234567890","run_number":2,"html_url":"https://example.com/2"},
		{"id":3,"name":"Lint","status":"in_progress","head_sha":"abcdef1234567890","run_number":1,"html_url":"https://example.com/3"},
		{"id":4,"name":"CI","status":"completed","conclusion":"success","head_sha":"9999999999999999","run_number":9,"html_url":"https://example.com/4"},
		{"id":5,"name":"","status":"completed","conclusion":"success","head_sha":"abcdef1234567890","run_number":1,"html_url":"https://example.com/5"}
	]}`

	newClient := func(t *testing.T) *Client {
		t.Helper()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", jsonHandler(runsJSON))
		return newMuxClient(t, mux)
	}

	t.Run("latest mode keeps only the highest run number per workflow name", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "abcdef1234567890", nil)
		require.NoError(t, err)

		// Runs 1 and 2 share the name "CI"; only run 2 (higher run_number) survives.
		// Run 4 is dropped because its head SHA does not match the ref.
		names := map[string]int64{}
		for _, cr := range status.CheckRuns {
			names[cr.Name] = cr.ID
		}
		assert.Equal(t, map[string]int64{"CI": 2, "Lint": 3, "": 5}, names)
		assert.Equal(t, 3, status.TotalCount)
		assert.Equal(t, "pending", status.State, "the in-progress Lint run keeps the overall state pending")
		assert.Equal(t, "abcdef1234567890", status.SHA)
	})

	t.Run("all mode keeps every matching run", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "abcdef1234567890", &GetCheckRunsOptions{Filter: "all"})
		require.NoError(t, err)
		assert.Equal(t, 4, status.TotalCount)
		assert.Equal(t, map[string]int{"success": 2, "failure": 1, "in_progress": 1}, status.ByConclusion)
	})

	t.Run("SHA matching is a case-insensitive prefix match", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "ABCDEF1", &GetCheckRunsOptions{Filter: "all"})
		require.NoError(t, err)
		assert.Equal(t, 4, status.TotalCount)
	})

	t.Run("a non-commit-looking ref is sent to GitHub as a branch filter", func(t *testing.T) {
		t.Parallel()

		var gotBranch string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
			gotBranch = r.URL.Query().Get("branch")
			jsonHandler(runsJSON)(w, r)
		})
		client := newMuxClient(t, mux)

		status, err := client.GetCheckRunsForRef(context.Background(), "main", &GetCheckRunsOptions{Filter: "all"})
		require.NoError(t, err)
		assert.Equal(t, "main", gotBranch)
		// No client-side SHA filtering happens for a branch ref, so run 4 survives.
		assert.Equal(t, 5, status.TotalCount)
	})

	t.Run("check name filter", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "abcdef1234567890", &GetCheckRunsOptions{CheckName: "CI", Filter: "all"})
		require.NoError(t, err)
		require.Len(t, status.CheckRuns, 2)
		ids := []int64{status.CheckRuns[0].ID, status.CheckRuns[1].ID}
		assert.ElementsMatch(t, []int64{1, 2}, ids)
		assert.Equal(t, "failure", status.State)
	})

	t.Run("status filter", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "abcdef1234567890", &GetCheckRunsOptions{Status: "in_progress", Filter: "all"})
		require.NoError(t, err)
		require.Len(t, status.CheckRuns, 1)
		assert.Equal(t, int64(3), status.CheckRuns[0].ID)
	})

	t.Run("every run is reported as the github-actions app", func(t *testing.T) {
		t.Parallel()

		status, err := newClient(t).GetCheckRunsForRef(context.Background(), "abcdef1234567890", &GetCheckRunsOptions{Filter: "all"})
		require.NoError(t, err)
		for _, cr := range status.CheckRuns {
			assert.Equal(t, "github-actions", cr.AppName)
			assert.NotEmpty(t, cr.DetailsURL)
		}
	})

	t.Run("API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs", statusHandler(http.StatusForbidden))
		client := newMuxClient(t, mux)

		_, err := client.GetCheckRunsForRef(context.Background(), "abcdef1234567890", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflow runs for ref abcdef1234567890")
	})
}
