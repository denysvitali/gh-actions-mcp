package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/denysvitali/gh-actions-mcp/config"
	appmcp "github.com/denysvitali/gh-actions-mcp/mcp"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMCPServer(t *testing.T) *appmcp.MCPServer {
	t.Helper()

	server, err := appmcp.NewMCPServer(&config.Config{
		Token:     "token",
		RepoOwner: "owner",
		RepoName:  "repo",
	}, logrus.New())
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	return server
}

// TestNewStreamableHTTPServerTimeouts pins WriteTimeout against the reason it
// has that value: the wait_* tools accept timeout_minutes up to 120, so a
// handler can legitimately hold the response open for two hours. If someone
// raises the tool cap without raising this timeout, long waits get truncated
// mid-response, and this test is the warning.
func TestNewStreamableHTTPServerTimeouts(t *testing.T) {
	defer preserveCommandGlobals()()

	mcpHTTPAddress = "127.0.0.1:8080"
	server := newStreamableHTTPServer(nil)

	const maxToolWait = 120 * time.Minute // mcp.Maximum(120) on timeout_minutes
	assert.Greater(t, server.WriteTimeout, maxToolWait,
		"WriteTimeout must exceed the longest wait_* tool timeout")
	assert.Equal(t, 125*time.Minute, server.WriteTimeout)
	assert.Equal(t, 10*time.Second, server.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, server.ReadTimeout)
	assert.Equal(t, 2*time.Minute, server.IdleTimeout)
	assert.Equal(t, 64<<10, server.MaxHeaderBytes)
	assert.Equal(t, "127.0.0.1:8080", server.Addr)
}

// TestServeStreamableHTTPShutsDownOnContextCancel exercises the one goroutine
// in this repository end to end: it starts the listener, cancels the context,
// and requires that serveStreamableHTTP returns without an error. The goleak
// check in TestMain is the other half — it proves the goroutine is gone.
func TestServeStreamableHTTPShutsDownOnContextCancel(t *testing.T) {
	defer preserveCommandGlobals()()

	mcpHTTPAddress = "127.0.0.1:0" // let the kernel pick a free port
	mcpHTTPPath = "/mcp"
	mcpHTTPMaxBody = 1 << 20

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveStreamableHTTP(ctx, newTestMCPServer(t)) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled context is a clean shutdown, not an error")
	case <-time.After(15 * time.Second):
		t.Fatal("serveStreamableHTTP did not return after its context was cancelled")
	}
}

func TestServeStreamableHTTPRefusesUnauthenticatedNonLoopback(t *testing.T) {
	defer preserveCommandGlobals()()

	mcpHTTPAddress = "0.0.0.0:8080"
	mcpHTTPToken = ""
	t.Setenv("GH_ACTIONS_MCP_HTTP_TOKEN", "")

	err := serveStreamableHTTP(context.Background(), newTestMCPServer(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing unauthenticated Streamable HTTP on a non-loopback address")
}

func TestServeStreamableHTTPRequiresBothTLSFlags(t *testing.T) {
	defer preserveCommandGlobals()()

	mcpHTTPAddress = "127.0.0.1:0"
	mcpHTTPPath = "/mcp"
	mcpHTTPMaxBody = 1 << 20
	mcpHTTPTLSCert = "/nonexistent/tls.crt"
	mcpHTTPTLSKey = ""

	err := serveStreamableHTTP(context.Background(), newTestMCPServer(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both --http-tls-cert and --http-tls-key are required")
}

func TestServeMCPRejectsUnknownTransport(t *testing.T) {
	defer preserveCommandGlobals()()

	mcpTransport = "grpc"
	err := serveMCP(context.Background(), newTestMCPServer(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported MCP transport "grpc" (allowed: stdio, http)`)
}

func TestStreamableHTTPHandlerRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		maxBody   int64
		origins   []string
		wantError string
	}{
		{
			name:      "relative path",
			path:      "mcp",
			maxBody:   1 << 20,
			wantError: "http path must be an absolute literal path",
		},
		{
			name:      "wildcard path",
			path:      "/mcp/{rest...}",
			maxBody:   1 << 20,
			wantError: "http path must be an absolute literal path",
		},
		{
			name:      "non-positive max body",
			path:      "/mcp",
			maxBody:   0,
			wantError: "http max body must be positive",
		},
		{
			name:      "invalid trusted origin",
			path:      "/mcp",
			maxBody:   1 << 20,
			origins:   []string{"not-a-url"},
			wantError: `invalid trusted origin "not-a-url"`,
		},
	}

	for _, tt := range tests {
		// No t.Parallel(): these mutate package-level flag variables.
		t.Run(tt.name, func(t *testing.T) {
			defer preserveCommandGlobals()()

			mcpHTTPPath = tt.path
			mcpHTTPMaxBody = tt.maxBody
			mcpHTTPOrigins = tt.origins

			_, err := streamableHTTPHandler(newTestMCPServer(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantError)
		})
	}
}

func TestGetVersionPrefersVersionEnv(t *testing.T) {
	defer preserveCommandGlobals()()

	t.Setenv("VERSION", "v9.9.9-from-env")
	assert.Equal(t, "v9.9.9-from-env", getVersion())
}
