package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWorkflowsServer returns a test server that serves the workflows list
// endpoint and records the last Authorization header it saw.
func newWorkflowsServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var authHeader string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"workflows":[{"id":1,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &authHeader
}

func TestNewClientWithOptions_BasicAuthAgainstProxy(t *testing.T) {
	// gh-proxy authenticates its consumers with HTTP Basic, not Bearer tokens.
	ts, authHeader := newWorkflowsServer(t)

	client, err := NewClientWithOptions(ClientOptions{
		Token:        "proxy-secret",
		Owner:        "owner",
		Repo:         "repo",
		APIBaseURL:   ts.URL + "/",
		AuthUsername: "workspace",
	})
	require.NoError(t, err)

	workflows, err := client.GetWorkflows(context.Background())
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	assert.Equal(t, "CI", workflows[0].Name)
	assert.Equal(t, "Basic d29ya3NwYWNlOnByb3h5LXNlY3JldA==", *authHeader) // workspace:proxy-secret
}

func TestNewClientWithOptions_BearerAuthWithoutUsername(t *testing.T) {
	ts, authHeader := newWorkflowsServer(t)

	client, err := NewClientWithOptions(ClientOptions{
		Token:      "ghp_pat",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: ts.URL + "/",
	})
	require.NoError(t, err)

	_, err = client.GetWorkflows(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer ghp_pat", *authHeader)
}

func TestNewClientWithOptions_BaseURLTrailingSlashAdded(t *testing.T) {
	ts, _ := newWorkflowsServer(t)

	// Deliberately omit the trailing slash; the client must tolerate it.
	client, err := NewClientWithOptions(ClientOptions{
		Token:      "tok",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: ts.URL,
	})
	require.NoError(t, err)

	_, err = client.GetWorkflows(context.Background())
	require.NoError(t, err)
}

func TestNewClientWithOptions_UploadURLDefaultsToAPIBaseURL(t *testing.T) {
	client, err := NewClientWithOptions(ClientOptions{
		Token:      "tok",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: "http://proxy.example/api/",
	})
	require.NoError(t, err)
	assert.Equal(t, "http://proxy.example/api/", client.gh.UploadURL.String())
}

func TestNewClientWithOptions_ExplicitUploadURL(t *testing.T) {
	client, err := NewClientWithOptions(ClientOptions{
		Token:      "tok",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: "http://proxy.example/api/",
		UploadURL:  "http://proxy.example/api/uploads/",
	})
	require.NoError(t, err)
	assert.Equal(t, "http://proxy.example/api/uploads/", client.gh.UploadURL.String())
}

func TestNewClientWithOptions_InvalidBaseURL(t *testing.T) {
	_, err := NewClientWithOptions(ClientOptions{
		Token:      "tok",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: "http://exa mple.invalid/",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_base_url")
}

func TestNewClientWithOptions_InvalidUploadURL(t *testing.T) {
	_, err := NewClientWithOptions(ClientOptions{
		Token:      "tok",
		Owner:      "owner",
		Repo:       "repo",
		APIBaseURL: "http://proxy.example/api/",
		UploadURL:  "http://exa mple.invalid/",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_url")
}

func TestNewClientWithOptions_RetryMaxDefaults(t *testing.T) {
	client, err := NewClientWithOptions(ClientOptions{Token: "tok", Owner: "o", Repo: "r"})
	require.NoError(t, err)
	assert.NotNil(t, client.retry)
	assert.NotNil(t, client.cache)
}
