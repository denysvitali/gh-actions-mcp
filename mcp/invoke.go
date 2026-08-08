package mcp

import (
	"context"
	"fmt"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// localSession is the lazily created in-memory ("loopback") MCP session pair
// that InvokeTool calls through, so the CLI path exercises exactly the same
// request handling as a remote transport.
//
// Concurrency contract:
//   - mu guards client, server and cancel, and nothing else. All three are
//     written together on first use and cleared together by Close; every read
//     and write of them must hold mu.
//   - A mutex (rather than sync.Once or a channel) is the right primitive here
//     because the session is also torn down by Close and must therefore be
//     re-settable, and because the critical section is short and does no
//     network or disk I/O: it only connects the two ends of an in-memory
//     transport and performs the MCP initialize handshake over it.
//   - mu is a leaf lock: no other lock is acquired while it is held, so there
//     is no lock-ordering constraint.
//   - The session deliberately outlives the request that created it (see
//     ensure); cancel belongs to the session, not to any single call.
type localSession struct {
	mu     sync.Mutex //nolint:forbidigo // Guards client, server, and cancel; it is a leaf lock.
	client *sdkmcp.ClientSession
	server *sdkmcp.ServerSession
	cancel context.CancelFunc
}

// InvokeTool executes a tool through an official SDK client session. This
// keeps the local CLI path identical to calls arriving over MCP transports.
func (s *MCPServer) InvokeTool(ctx context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error) { //nolint:contextcheck // The cached loopback session intentionally has a separate lifetime.
	if ctx == nil {
		ctx = context.Background()
	}
	if args == nil {
		args = map[string]any{}
	}

	session, err := s.invoke.ensure(s.srv, s.version)
	if err != nil {
		return nil, err
	}
	return session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
}

// ensure returns the memoized client session, creating it on first use.
//
// The session is intentionally rooted in context.Background() rather than in
// the calling request's context: it is cached across calls and must stay usable
// after the call that created it returns. Its lifetime is owned by Close, which
// cancels it. Threading a per-call context in here would tear the shared
// session down as soon as the first InvokeTool call finished.
func (l *localSession) ensure(srv *sdkmcp.Server, version string) (*sdkmcp.ClientSession, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect local MCP server: %w", err)
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "gh-actions-mcp-cli",
		Version: version,
	}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		return nil, fmt.Errorf("failed to connect local MCP client: %w", err)
	}

	l.cancel = cancel
	l.server = serverSession
	l.client = clientSession
	return clientSession, nil
}

// Close releases the lazy in-memory MCP session, if one was created. It is safe
// to call more than once and on a server that never invoked a tool, and it
// returns the first teardown error while still releasing everything else.
func (s *MCPServer) Close() error {
	return s.invoke.close()
}

// close tears down both ends of the session and cancels its context, returning
// the first error encountered. All fields are cleared, so a later ensure call
// rebuilds the session.
func (l *localSession) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var firstErr error
	if l.client != nil {
		firstErr = l.client.Close()
		l.client = nil
	}
	if l.server != nil {
		if err := l.server.Close(); firstErr == nil {
			firstErr = err
		}
		l.server = nil
	}
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	return firstErr
}
