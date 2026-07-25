package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	appmcp "github.com/denysvitali/gh-actions-mcp/mcp"
	mcptypes "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunToolRejectsInvalidJSONArgs(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	cfgFile = writeTestConfig(t, "token: test-token\n")
	toolArgsJSON = "{"
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runTool(cmd, []string{"list_workflows"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse --args as JSON object")
}

func TestRunToolUsesJSONRepoArgs(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/example-org/example-repo/actions/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":1,"workflows":[{"id":1,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfgFile = writeTestConfig(t, "token: test-token\napi_base_url: "+ts.URL+"/\nupload_url: "+ts.URL+"/\n")
	toolArgsJSON = `{"owner":"example-org","repo":"example-repo","limit":1}`
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	output := captureStdout(t, func() {
		require.NoError(t, runTool(cmd, []string{"list_workflows"}))
	})

	assert.Contains(t, output, `"name":"CI"`)
}

func TestRunToolReturnsUnknownToolError(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	cfgFile = writeTestConfig(t, "token: test-token\n")
	toolArgsJSON = `{}`
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runTool(cmd, []string{"missing_tool"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown tool "missing_tool"`)
}

func TestRunToolUsesSDKArgumentValidation(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	cfgFile = writeTestConfig(t, "token: test-token\n")
	toolArgsJSON = `{"run_id":"not-a-number"}`
	logLevel = "info"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runTool(cmd, []string{"get_run"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating")
}

func TestStreamableHTTPHandler(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	mcpHTTPPath = "/mcp"
	server, err := appmcp.NewMCPServer(&config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}, logrus.New())
	require.NoError(t, err)
	defer server.Close()

	handler, err := streamableHTTPHandler(server)
	require.NoError(t, err)

	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, healthRequest)
	assert.Equal(t, http.StatusOK, healthResponse.Code)
	assert.Equal(t, "ok\n", healthResponse.Body.String())

	initializeRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	initializeRequest.Header.Set("Content-Type", "application/json")
	initializeRequest.Header.Set("Accept", "application/json, text/event-stream")
	initializeResponse := httptest.NewRecorder()
	handler.ServeHTTP(initializeResponse, initializeRequest)
	assert.Equal(t, http.StatusOK, initializeResponse.Code)
	assert.Contains(t, initializeResponse.Body.String(), `"name":"github-actions-mcp"`)
}

func TestStreamableHTTPBearerAuth(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()

	mcpHTTPPath = "/mcp"
	mcpHTTPToken = "secret"
	server, err := appmcp.NewMCPServer(&config.Config{Token: "token", RepoOwner: "owner", RepoName: "repo"}, logrus.New())
	require.NoError(t, err)
	defer server.Close()
	handler, err := streamableHTTPHandler(server)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestStreamableHTTPClientRoundTrip(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()
	mcpHTTPPath = "/mcp"
	mcpHTTPMaxBody = 1 << 20
	server, err := appmcp.NewMCPServer(&config.Config{Token: "token", RepoOwner: "owner", RepoName: "repo", ServerVersion: "test-version"}, logrus.New())
	require.NoError(t, err)
	defer server.Close()
	handler, err := streamableHTTPHandler(server)
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcptypes.NewClient(&mcptypes.Implementation{Name: "conformance-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &mcptypes.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	require.NoError(t, err)
	defer session.Close()
	assert.Equal(t, "test-version", session.InitializeResult().ServerInfo.Version)
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, tools.Tools, 12)
}

func TestStreamableHTTPBodyLimitAndOrigins(t *testing.T) {
	restore := preserveCommandGlobals()
	defer restore()
	mcpHTTPPath = "/mcp"
	mcpHTTPMaxBody = 64
	mcpHTTPOrigins = []string{"https://trusted.example"}
	server, err := appmcp.NewMCPServer(&config.Config{Token: "token", RepoOwner: "owner", RepoName: "repo"}, logrus.New())
	require.NoError(t, err)
	defer server.Close()
	handler, err := streamableHTTPHandler(server)
	require.NoError(t, err)

	large := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 128)))
	large.Header.Set("Content-Type", "application/json")
	large.Header.Set("Accept", "application/json")
	largeResponse := httptest.NewRecorder()
	handler.ServeHTTP(largeResponse, large)
	assert.NotEqual(t, http.StatusOK, largeResponse.Code)

	crossOrigin := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	crossOrigin.Host = "local.example"
	crossOrigin.Header.Set("Origin", "https://untrusted.example")
	crossOrigin.Header.Set("Content-Type", "application/json")
	crossOrigin.Header.Set("Accept", "application/json, text/event-stream")
	crossOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginResponse, crossOrigin)
	assert.Equal(t, http.StatusForbidden, crossOriginResponse.Code)
}

func TestIsLoopbackAddress(t *testing.T) {
	assert.True(t, isLoopbackAddress("127.0.0.1:8080"))
	assert.True(t, isLoopbackAddress("[::1]:8080"))
	assert.True(t, isLoopbackAddress("localhost:8080"))
	assert.False(t, isLoopbackAddress("0.0.0.0:8080"))
}
