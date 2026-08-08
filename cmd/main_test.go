package cmd

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine running. The one
// goroutine this package can start is the HTTP listener in
// serveStreamableHTTP, which is documented to exit via its buffered errCh; this
// check is what keeps that claim true.
//
// The single ignore is not ours: every Streamable HTTP session the go-sdk
// handler accepts owns a reader goroutine that lives until the session is
// terminated or SessionTimeout (30 minutes) elapses. streamableHTTPHandler
// hands the handler to net/http and keeps no reference, so a test has no way to
// tear those sessions down. Only the SDK's own session reader is ignored, so a
// leak in our listener goroutine still fails here.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/modelcontextprotocol/go-sdk/mcp.(*streamableServerConn).Read"),
	)
}
