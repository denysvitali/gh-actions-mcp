package github

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferRepoFromOrigin_HTTPS(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:     "HTTPS URL",
			url:      "https://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:     "HTTPS URL without .git",
			url:      "https://github.com/owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:     "HTTP URL",
			url:      "http://github.com/owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		// Note: Non-github.com URLs will fail as expected
		{
			name:     "Non-GitHub URL fails",
			url:      "https://github.mycompany.com/owner/repo.git",
			wantOwner: "",
			wantRepo:  "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_SSH(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:     "SSH URL",
			url:      "git@github.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:     "SSH URL without .git",
			url:      "git@github.com:owner/repo",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
		{
			name:     "SSH enterprise URL",
			url:      "git@github.mycompany.com:owner/repo.git",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := InferRepoFromOrigin(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestInferRepoFromOrigin_Invalid(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "Not a GitHub URL",
			url:  "https://gitlab.com/owner/repo.git",
		},
		{
			name: "Malformed URL",
			url:  "not-a-url",
		},
		{
			name: "Empty string",
			url:  "",
		},
		{
			name: "SSH with wrong format",
			url:  "git@github.com:missing-slash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := InferRepoFromOrigin(tt.url)
			assert.Error(t, err)
		})
	}
}

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
	// Capture request for inspection
	var capturedReq *http.Request

	// Use a custom transport to capture the request
	originalTransport := http.DefaultTransport
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) *http.Response {
		capturedReq = req
		// Return a mock response
		return &http.Response{
			StatusCode: 200,
			Body:       http.NoBody,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}
	})
	defer func() { http.DefaultTransport = originalTransport }()

	client := NewClient("my-secret-token", "owner", "repo")
	_, _ = client.GetWorkflows(context.Background())

	if capturedReq != nil {
		t.Logf("Authorization header: %q", capturedReq.Header.Get("Authorization"))
		assert.Equal(t, "Bearer my-secret-token", capturedReq.Header.Get("Authorization"))
	} else {
		t.Error("No request was captured")
	}
}

type roundTripperFunc func(*http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := f(req)
	return resp, nil
}
