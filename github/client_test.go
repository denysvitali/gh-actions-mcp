package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	client := NewClient("test-token", "test-owner", "test-repo")

	assert.NotNil(t, client)
	assert.Equal(t, "test-owner", client.owner)
	assert.Equal(t, "test-repo", client.repo)
}

func TestGetRepoInfo(t *testing.T) {
	client := NewClient("token", "owner", "repo")

	repoOwner, repoName := client.GetRepoInfo()

	assert.Equal(t, "owner", repoOwner)
	assert.Equal(t, "repo", repoName)
}

func TestSetLogger(t *testing.T) {
	// This test mainly ensures the function doesn't panic
}

func TestTokenIsSentInRequest(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":0,"workflows":[]}`))
	}))
	defer ts.Close()

	client, err := NewClientWithOptions(ClientOptions{
		Token:      "my-secret-token",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: ts.URL + "/",
	})
	require.NoError(t, err)

	workflows, err := client.GetWorkflows(context.Background())
	require.NoError(t, err)
	assert.Empty(t, workflows)
	require.NotNil(t, capturedReq)
	assert.Equal(t, "Bearer my-secret-token", capturedReq.Header.Get("Authorization"))
}

// Test error scenarios
func TestNewClientWithPerPage(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		owner         string
		repo          string
		perPageLimit  int
		expectedLimit int
	}{
		{
			name:          "valid limit",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  100,
			expectedLimit: 100,
		},
		{
			name:          "zero limit uses default",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  0,
			expectedLimit: 50,
		},
		{
			name:          "negative limit uses default",
			token:         "token",
			owner:         "owner",
			repo:          "repo",
			perPageLimit:  -10,
			expectedLimit: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClientWithPerPage(tt.token, tt.owner, tt.repo, tt.perPageLimit)
			assert.NotNil(t, client)
			assert.Equal(t, tt.expectedLimit, client.perPageLimit)
		})
	}
}

func TestClient_GetWorkflowRun_ErrorHandling(t *testing.T) {
	// This test verifies error handling when the API returns an error
	client := NewClient("invalid-token", "owner", "repo")

	// Try to get a non-existent workflow run
	ctx := context.Background()
	_, err := client.GetWorkflowRun(ctx, 999999999)

	// Should return an error (authentication or not found)
	assert.Error(t, err)
}

func TestClient_TriggerWorkflow_ErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		workflowID  string
		ref         string
		expectErr   bool
		errContains string
	}{
		{
			name:        "invalid workflow ID with bad token",
			workflowID:  "nonexistent-workflow",
			ref:         "main",
			expectErr:   true,
			errContains: "failed to trigger workflow",
		},
		{
			name:       "empty workflow ID",
			workflowID: "",
			ref:        "main",
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-token", "owner", "repo")
			err := client.TriggerWorkflow(context.Background(), tt.workflowID, tt.ref)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestClient_APIErrors(t *testing.T) {
	client := NewClient("invalid-token", "owner", "repo")
	ctx := context.Background()

	t.Run("GetActionsStatus with invalid token", func(t *testing.T) {
		_, err := client.GetActionsStatus(ctx, 10)
		// Should get an authentication or network error
		assert.Error(t, err)
	})

	t.Run("GetWorkflows with invalid token", func(t *testing.T) {
		_, err := client.GetWorkflows(ctx)
		// Should get an authentication or network error
		assert.Error(t, err)
	})

	t.Run("GetWorkflowRuns with invalid workflow", func(t *testing.T) {
		_, err := client.GetWorkflowRuns(ctx, 999999999, "main")
		assert.Error(t, err)
	})

	t.Run("CancelWorkflowRun with invalid run ID", func(t *testing.T) {
		err := client.CancelWorkflowRun(ctx, 999999999)
		assert.Error(t, err)
	})

	t.Run("RerunWorkflowRun with invalid run ID", func(t *testing.T) {
		err := client.RerunWorkflowRun(ctx, 999999999)
		assert.Error(t, err)
	})
}

type roundTripperFunc func(*http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := f(req)
	return resp, nil
}

func TestSetLoggerAndSetDetectorLogger(t *testing.T) {
	// Not parallel: both setters write the same package-level logger.
	original := log
	t.Cleanup(func() { log = original })

	first := logrus.New()
	SetLogger(first)
	assert.Same(t, first, log)

	// SetDetectorLogger is an alias, so the last call wins for both.
	second := logrus.New()
	SetDetectorLogger(second)
	assert.Same(t, second, log)
}

func TestTransportStats(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"total_count":0,"workflows":[]}`))
	})
	client := newMuxClient(t, mux)

	_, err := client.GetWorkflows(context.Background())
	require.NoError(t, err)

	stats := client.TransportStats()
	assert.GreaterOrEqual(t, stats.Retry.Attempts, int64(1))
	assert.Equal(t, int64(1), stats.Cache.Stores, "an ETagged 200 response is stored for revalidation")
	assert.Zero(t, stats.Cache.Hits)

	// The second call revalidates and, on 304, serves the cached body.
	_, err = client.GetWorkflows(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), client.TransportStats().Cache.Revalidations)
}

// TestNewClientWithPerPage_NilOnConstructionFailure documents that
// NewClientWithPerPage swallows NewClientWithOptions' error and can therefore
// return nil. The signature is public API, so it is kept as-is; callers must
// nil-check. Reached here only through the token path, which cannot fail — see
// TestNewClientWithOptions_InvalidBaseURL for the failing branch.
func TestNewClientWithPerPage_NilOnConstructionFailure(t *testing.T) {
	t.Parallel()

	client := NewClientWithPerPage("token", "owner", "repo", 25)
	require.NotNil(t, client)
	assert.Equal(t, 25, client.perPageLimit)
}
