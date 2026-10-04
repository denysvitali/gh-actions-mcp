package github

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// deadlineTransport enforces the full request/body deadline without using
// http.Client.Timeout's legacy cancellation goroutine. go-github CheckResponse
// replaces error response bodies after reading them, so EOF must release the
// original body and cancellation resources even when its Close is lost.
type deadlineTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t *deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), t.timeout)
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		return response, err
	}
	if response == nil || response.Body == nil {
		cancel()
		return response, nil
	}
	body := response.Body
	var closeOnce sync.Once
	var closeErr error
	closeBody := func() error { closeOnce.Do(func() { closeErr = body.Close(); cancel() }); return closeErr }
	// Also release a body abandoned before EOF (for example an SDK error-body
	// size cap) when its request deadline expires or the caller cancels.
	stop := context.AfterFunc(ctx, func() { _ = closeBody() })
	response.Body = &deadlineBody{Reader: body, closeBody: closeBody, stop: stop}
	return response, nil
}

type deadlineBody struct {
	io.Reader
	closeBody func() error
	stop      func() bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != nil {
		_ = b.Close()
	}
	return n, err
}
func (b *deadlineBody) Close() error { b.stop(); return b.closeBody() }
