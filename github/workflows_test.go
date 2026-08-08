package github

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workflowsJSON = `{"total_count":2,"workflows":[
	{"id":50,"name":"CI","path":".github/workflows/ci.yml","state":"active"},
	{"id":51,"name":"Release","path":".github/workflows/release.yml","state":"disabled_manually"}
]}`

func TestListWorkflowsPage(t *testing.T) {
	t.Parallel()

	t.Run("maps every field and reports no next page", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(workflowsJSON))
		workflows, next, err := newMuxClient(t, mux).ListWorkflowsPage(context.Background(), 10, 1)
		require.NoError(t, err)
		require.Len(t, workflows, 2)
		assert.Equal(t, int64(50), workflows[0].ID)
		assert.Equal(t, "CI", workflows[0].Name)
		assert.Equal(t, ".github/workflows/ci.yml", workflows[0].Path)
		assert.Equal(t, "active", workflows[0].State)
		assert.Equal(t, 0, next)
	})

	t.Run("out-of-range perPage and page are normalised", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			perPage     int
			page        int
			wantPerPage string
			wantPage    string
		}{
			{name: "zero perPage falls back to the client limit", perPage: 0, page: 1, wantPerPage: "50", wantPage: "1"},
			{name: "negative perPage falls back to the client limit", perPage: -1, page: 1, wantPerPage: "50", wantPage: "1"},
			{name: "perPage above 100 falls back to the client limit", perPage: 500, page: 1, wantPerPage: "50", wantPage: "1"},
			{name: "zero page becomes page 1", perPage: 10, page: 0, wantPerPage: "10", wantPage: "1"},
			{name: "page above 1 is forwarded", perPage: 10, page: 3, wantPerPage: "10", wantPage: "3"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				var perPage, page string
				mux := http.NewServeMux()
				mux.HandleFunc("/repos/owner/repo/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
					perPage = r.URL.Query().Get("per_page")
					page = r.URL.Query().Get("page")
					jsonHandler(`{"total_count":0,"workflows":[]}`)(w, r)
				})
				_, _, err := newMuxClient(t, mux).ListWorkflowsPage(context.Background(), tt.perPage, tt.page)
				require.NoError(t, err)
				assert.Equal(t, tt.wantPerPage, perPage)
				assert.Equal(t, tt.wantPage, page)
			})
		}
	})

	t.Run("an API failure is wrapped", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", statusHandler(http.StatusForbidden))
		_, _, err := newMuxClient(t, mux).ListWorkflowsPage(context.Background(), 10, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to list workflows")
	})
}

func TestResolveWorkflowID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		selector   string
		wantID     int64
		wantName   string
		wantErrMsg string
	}{
		{name: "numeric ID resolves to the workflow's name", selector: "50", wantID: 50, wantName: "CI"},
		{
			// A numeric selector that no workflow matches still resolves; the raw
			// selector is echoed back as the name.
			name: "unknown numeric ID echoes the selector as the name", selector: "999", wantID: 999, wantName: "999",
		},
		{name: "name resolves", selector: "Release", wantID: 51, wantName: "Release"},
		{name: "path resolves", selector: ".github/workflows/ci.yml", wantID: 50, wantName: "CI"},
		{name: "unknown name is an error", selector: "Nope", wantErrMsg: "workflow Nope not found"},
		{name: "empty selector is an error", selector: "", wantErrMsg: "workflow  not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(workflowsJSON))
			id, name, err := newMuxClient(t, mux).ResolveWorkflowID(context.Background(), tt.selector)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestResolveWorkflowID_ListFailures(t *testing.T) {
	t.Parallel()

	for _, selector := range []string{"50", "CI"} {
		t.Run(selector, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/repos/owner/repo/actions/workflows", statusHandler(http.StatusForbidden))
			_, _, err := newMuxClient(t, mux).ResolveWorkflowID(context.Background(), selector)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to list workflows")
		})
	}
}

func TestTriggerWorkflow(t *testing.T) {
	t.Parallel()

	t.Run("dispatches against the resolved numeric ID", func(t *testing.T) {
		t.Parallel()

		var body string
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(workflowsJSON))
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/dispatches", func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			body = string(buf)
			w.WriteHeader(http.StatusNoContent)
		})
		require.NoError(t, newMuxClient(t, mux).TriggerWorkflow(context.Background(), "CI", "main"))
		assert.Contains(t, body, `"ref":"main"`)
	})

	t.Run("an unresolvable workflow is reported", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(workflowsJSON))
		err := newMuxClient(t, mux).TriggerWorkflow(context.Background(), "Nope", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to trigger workflow Nope")
	})

	t.Run("a dispatch failure is reported", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/workflows", jsonHandler(workflowsJSON))
		mux.HandleFunc("/repos/owner/repo/actions/workflows/50/dispatches", statusHandler(http.StatusUnprocessableEntity))
		err := newMuxClient(t, mux).TriggerWorkflow(context.Background(), "CI", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to trigger workflow CI")
	})
}

func TestParseWorkflowID(t *testing.T) {
	t.Parallel()

	id, err := ParseWorkflowID("42")
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)

	_, err = ParseWorkflowID("ci.yml")
	assert.Error(t, err)
}
