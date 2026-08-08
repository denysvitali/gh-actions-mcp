package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Construction and end-to-end SDK round trips. Per-family handler tests live in
// the handlers_*_test.go files; the wire-schema gate is tools_schema_test.go.

func TestNewMCPServer(t *testing.T) {
	logger := logrus.New()
	cfg := &config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}

	server, err := NewMCPServer(cfg, logger)
	require.NoError(t, err)

	assert.NotNil(t, server)
	assert.NotNil(t, server.srv)
	assert.NotNil(t, server.client)
	assert.NotNil(t, server.config)
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

	server, err := NewMCPServer(cfg, logger)
	require.NoError(t, err)

	// Verify server and its components are properly initialized
	assert.NotNil(t, server)
	assert.NotNil(t, server.GetServer())
}

func TestOfficialSDKRoundTrip(t *testing.T) {
	app, err := NewMCPServer(&config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}, logrus.New())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.GetServer().Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = clientSession.Close() }()

	tools, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, tools.Tools, 12)
	toolByName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolByName[tool.Name] = tool
	}
	require.NotNil(t, toolByName["list_runs"].Annotations)
	assert.True(t, toolByName["list_runs"].Annotations.ReadOnlyHint)
	require.NotNil(t, toolByName["manage_run"].Annotations)
	require.NotNil(t, toolByName["manage_run"].Annotations.DestructiveHint)
	assert.True(t, *toolByName["manage_run"].Annotations.DestructiveHint)
	assert.NotNil(t, toolByName["list_workflows"].OutputSchema)
	assert.NotNil(t, toolByName["list_runs"].OutputSchema)
	assert.NotNil(t, toolByName["analyze_timing"].OutputSchema)
	assert.NotNil(t, toolByName["wait_for_run"].OutputSchema)
	assert.NotNil(t, toolByName["manage_run"].OutputSchema)
	assert.NotNil(t, toolByName["diagnose_failure"].OutputSchema)

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_run",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.NotEmpty(t, result.Content)
}

func TestOfficialSDKResourceRoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs/123", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": 123,
			"name": "CI",
			"status": "completed",
			"conclusion": "success",
			"head_branch": "main",
			"head_sha": "abc123",
			"event": "push",
			"run_number": 7,
			"workflow_id": 99
		}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	app, err := NewMCPServer(&config.Config{
		Token:      "token",
		RepoOwner:  "owner",
		RepoName:   "repo",
		APIBaseURL: ts.URL + "/",
		UploadURL:  ts.URL + "/",
	}, logrus.New())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.GetServer().Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = clientSession.Close() }()

	templates, err := clientSession.ListResourceTemplates(ctx, nil)
	require.NoError(t, err)
	require.Len(t, templates.ResourceTemplates, 1)
	assert.Equal(t, workflowRunResourceTemplate, templates.ResourceTemplates[0].URITemplate)

	resource, err := clientSession.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "github-actions://runs/owner/repo/123",
	})
	require.NoError(t, err)
	require.Len(t, resource.Contents, 1)
	assert.Equal(t, "application/json", resource.Contents[0].MIMEType)
	assert.Contains(t, resource.Contents[0].Text, `"id":123`)
	assert.Contains(t, resource.Contents[0].Text, `"conclusion":"success"`)
}
