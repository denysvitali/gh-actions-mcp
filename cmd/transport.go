package cmd

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	appmcp "github.com/denysvitali/gh-actions-mcp/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func serveMCP(ctx context.Context, server *appmcp.MCPServer) error {
	switch strings.ToLower(strings.TrimSpace(mcpTransport)) {
	case "", "stdio":
		return server.GetServer().Run(ctx, &sdkmcp.StdioTransport{})
	case "http":
		return serveStreamableHTTP(ctx, server)
	default:
		return fmt.Errorf("unsupported MCP transport %q (allowed: stdio, http)", mcpTransport)
	}
}

func streamableHTTPHandler(server *appmcp.MCPServer) (http.Handler, error) {
	if !strings.HasPrefix(mcpHTTPPath, "/") || strings.ContainsAny(mcpHTTPPath, "{}*") {
		return nil, fmt.Errorf("http path must be an absolute literal path")
	}

	mcpHandler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server.GetServer() },
		&sdkmcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute},
	)
	if mcpHTTPMaxBody <= 0 {
		return nil, fmt.Errorf("http max body must be positive")
	}
	originProtection := http.NewCrossOriginProtection()
	for _, origin := range mcpHTTPOrigins {
		if err := originProtection.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("invalid trusted origin %q: %w", origin, err)
		}
	}
	limited := requestBodyLimit(mcpHandler, mcpHTTPMaxBody)
	protected := originProtection.Handler(limited)
	endpoint := bearerAuth(protected, httpBearerToken())
	mux := http.NewServeMux()
	mux.Handle(mcpHTTPPath, endpoint)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux, nil
}

func serveStreamableHTTP(ctx context.Context, server *appmcp.MCPServer) error {
	if httpBearerToken() == "" && !isLoopbackAddress(mcpHTTPAddress) {
		return errors.New("refusing unauthenticated Streamable HTTP on a non-loopback address; set --http-token or GH_ACTIONS_MCP_HTTP_TOKEN")
	}
	handler, err := streamableHTTPHandler(server)
	if err != nil {
		return err
	}
	httpServer := newStreamableHTTPServer(handler)

	if (mcpHTTPTLSCert == "") != (mcpHTTPTLSKey == "") {
		return errors.New("both --http-tls-cert and --http-tls-key are required to enable TLS")
	}
	scheme := "http"
	if mcpHTTPTLSCert != "" {
		scheme = "https"
	}
	log.Infof("Serving MCP Streamable HTTP on %s://%s%s", scheme, mcpHTTPAddress, mcpHTTPPath)

	// Concurrency contract for the goroutine below — the only `go` statement in
	// this repository.
	//
	//	Owner:      serveStreamableHTTP (this function, this call).
	//	Started by: this call, after every configuration check has passed.
	//	Exit path:  it sends exactly one value on errCh and returns. errCh is
	//	            buffered with capacity 1, so the send never blocks even when
	//	            nobody is left to receive — the goroutine cannot leak.
	//	Waited on:  serverExit below. On the ctx.Done() path it drains errCh
	//	            after Shutdown returns, so serveStreamableHTTP never returns
	//	            while the listener goroutine is still running.
	//	Shares:     nothing mutable. It reads the mcpHTTPTLS* flags, which are
	//	            written once by flag parsing before this function is called.
	errCh := make(chan error, 1)
	go func() {
		if mcpHTTPTLSCert != "" {
			errCh <- httpServer.ListenAndServeTLS(mcpHTTPTLSCert, mcpHTTPTLSKey)
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()

	return serverExit(ctx, httpServer, errCh)
}

// newStreamableHTTPServer builds the HTTP server with bounded timeouts.
func newStreamableHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              mcpHTTPAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A single MCP request can legitimately hold the response open for as
		// long as the longest-running tool: wait_for_run, wait_all and
		// wait_for_commit_checks accept timeout_minutes with Maximum(120), so
		// 120 minutes is the longest a handler can block. 125 leaves five
		// minutes of margin for the final response write. Lower it and a
		// legitimate two-hour wait would be cut off mid-stream.
		WriteTimeout:   125 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
	}
}

// serverExit blocks until either the listener goroutine fails or ctx is
// cancelled, in which case the server is drained with a bounded grace period.
// It always waits for the listener goroutine before returning.
func serverExit(ctx context.Context, httpServer *http.Server, errCh <-chan error) error {
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// contextcheck: deriving from ctx is wrong here — ctx is already
		// cancelled, which is why we are on this branch, so a derived context
		// would make Shutdown give up immediately and drop in-flight
		// responses. A fresh context is the grace period.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func requestBodyLimit(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Body != nil {
			request.Body = http.MaxBytesReader(w, request.Body, maxBytes)
		}
		next.ServeHTTP(w, request)
	})
}

func httpBearerToken() string {
	if mcpHTTPToken != "" {
		return mcpHTTPToken
	}
	return os.Getenv("GH_ACTIONS_MCP_HTTP_TOKEN")
}

func bearerAuth(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
