package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/require"
)

type trackedDeadlineBody struct {
	io.Reader
	closes atomic.Int32
	closed chan struct{}
}

func (b *trackedDeadlineBody) Close() error {
	if b.closes.Add(1) == 1 {
		close(b.closed)
	}
	return nil
}

func TestDeadlineTransportReleasesSDKErrorBody(t *testing.T) {
	body := &trackedDeadlineBody{Reader: strings.NewReader(`{"message":"not found"}`), closed: make(chan struct{})}
	var requestCtx context.Context
	transport := &deadlineTransport{timeout: time.Second, base: roundTripperFunc(func(req *http.Request) *http.Response {
		requestCtx = req.Context()
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: body, Request: req}
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.test", nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Error(t, githubapi.CheckResponse(response), "the SDK consumes and replaces an error body without closing it")
	require.Equal(t, int32(1), body.closes.Load(), "EOF must close the original body before SDK replacement")
	require.ErrorIs(t, requestCtx.Err(), context.Canceled)
	require.NoError(t, response.Body.Close())
	require.Equal(t, int32(1), body.closes.Load())
}

func TestDeadlineTransportClosesAbandonedBodyOnTimeout(t *testing.T) {
	body := &trackedDeadlineBody{Reader: strings.NewReader("unread"), closed: make(chan struct{})}
	transport := &deadlineTransport{timeout: 10 * time.Millisecond, base: roundTripperFunc(func(req *http.Request) *http.Response {
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 10*time.Millisecond)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.test", nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(req)
	require.NoError(t, err)
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("deadline failed to close unread response body")
	}
	require.NoError(t, response.Body.Close())
	require.Equal(t, int32(1), body.closes.Load())
}
