package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryTransportRetriesSafeTransientResponses(t *testing.T) {
	attempts := 0
	transport := NewRetryTransport(roundTripperFunc(func(req *http.Request) *http.Response {
		attempts++
		status := http.StatusServiceUnavailable
		if attempts == 3 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
			Request:    req,
		}
	}), 3)
	transport.sleep = func(context.Context, time.Duration) error { return nil }
	transport.jitter = func(delay time.Duration) time.Duration { return delay }

	request, err := http.NewRequest(http.MethodGet, "https://api.github.test/resource", nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, 3, attempts)
}

func TestRetryTransportDoesNotReplayMutations(t *testing.T) {
	attempts := 0
	transport := NewRetryTransport(roundTripperFunc(func(req *http.Request) *http.Response {
		attempts++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
			Request:    req,
		}
	}), 3)

	request, err := http.NewRequest(http.MethodPost, "https://api.github.test/resource", strings.NewReader("payload"))
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Equal(t, 1, attempts)
}

func TestRetryTransportHonorsRetryAfter(t *testing.T) {
	attempts := 0
	var delay time.Duration
	transport := NewRetryTransport(roundTripperFunc(func(req *http.Request) *http.Response {
		attempts++
		status := http.StatusTooManyRequests
		header := http.Header{"Retry-After": []string{"2"}}
		if attempts == 2 {
			status = http.StatusOK
			header = make(http.Header)
			header.Set("X-RateLimit-Remaining", "4999")
			header.Set("X-RateLimit-Reset", "2000000000")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("response")), Request: req}
	}), 1)
	transport.sleep = func(_ context.Context, got time.Duration) error {
		delay = got
		return nil
	}
	transport.jitter = func(delay time.Duration) time.Duration { return delay }

	request, err := http.NewRequest(http.MethodGet, "https://api.github.test/resource", nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, 2*time.Second, delay)
	assert.Equal(t, 2, attempts)
	stats := transport.Stats()
	assert.Equal(t, int64(2), stats.Attempts)
	assert.Equal(t, int64(1), stats.Retries)
	assert.Equal(t, int64(1), stats.RateLimits)
	assert.Equal(t, int64(4999), stats.LastRateLimitRemaining)
	assert.Equal(t, int64(2000000000), stats.LastRateLimitReset)
	assert.Positive(t, stats.TotalRequestTime)
}
