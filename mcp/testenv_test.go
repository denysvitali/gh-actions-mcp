package mcp

import (
	"testing"

	"github.com/denysvitali/gh-actions-mcp/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// configOverrides names the config knobs a test may vary. It exists so tests
// set fields by name instead of positionally, and so adding a knob does not
// touch every call site.
type configOverrides struct {
	defaultLimit  int
	defaultLogLen int
}

// newTestServer builds a fully wired MCPServer against a fixed owner/repo and
// a discard logger. Pass nil to accept the zero-value config, which exercises
// the "fall back to the DefaultListLimit/DefaultLogLines constants" path.
func newTestServer(t *testing.T, apply func(*configOverrides)) *MCPServer {
	t.Helper()

	overrides := configOverrides{}
	if apply != nil {
		apply(&overrides)
	}

	logger := logrus.New()
	logger.SetOutput(discardWriter{})

	server, err := NewMCPServer(&config.Config{
		Token:         "test-token",
		RepoOwner:     "test-owner",
		RepoName:      "test-repo",
		DefaultLimit:  overrides.defaultLimit,
		DefaultLogLen: overrides.defaultLogLen,
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
