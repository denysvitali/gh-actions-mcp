package github

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

const DefaultRetryMax = 3

const maxRetryDelay = 30 * time.Second

// RetryTransport retries safe GitHub reads after transient failures and rate
// limits. Mutation requests are never replayed.
type RetryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
	sleep      func(context.Context, time.Duration) error
	jitter     func(time.Duration) time.Duration
	stats      RetryStats
}

type RetryStats struct {
	Attempts               atomic.Int64
	Retries                atomic.Int64
	RateLimits             atomic.Int64
	TransientErrors        atomic.Int64
	TransportErrors        atomic.Int64
	WaitNanoseconds        atomic.Int64
	RequestNanoseconds     atomic.Int64
	MaxLatencyNanoseconds  atomic.Int64
	LastRateLimitRemaining atomic.Int64
	LastRateLimitReset     atomic.Int64
}

type RetryStatsSnapshot struct {
	Attempts, Retries, RateLimits, TransientErrors, TransportErrors int64
	TotalWait                                                       time.Duration
	TotalRequestTime, MaxLatency                                    time.Duration
	LastRateLimitRemaining, LastRateLimitReset                      int64
}

func NewRetryTransport(base http.RoundTripper, maxRetries int) *RetryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	transport := &RetryTransport{
		base:       base,
		maxRetries: maxRetries,
		baseDelay:  250 * time.Millisecond,
		sleep:      sleepContext,
		jitter:     jitterDelay,
	}
	transport.stats.LastRateLimitRemaining.Store(-1)
	return transport
}

func (t *RetryTransport) RoundTrip(request *http.Request) (*http.Response, error) { //nolint:gocognit // Retry state is deliberately visible in one bounded loop.
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return t.base.RoundTrip(request)
	}

	for attempt := 0; ; attempt++ {
		t.stats.Attempts.Add(1)
		started := time.Now()
		response, err := t.base.RoundTrip(request)
		latency := time.Since(started)
		t.recordResponse(request, response, latency)
		if err != nil {
			t.stats.TransportErrors.Add(1)
			if attempt >= t.maxRetries || request.Context().Err() != nil {
				return nil, err
			}
			delay := t.jitter(retryBackoff(t.baseDelay, attempt))
			t.recordRetry(request, 0, delay, "transport_error")
			if sleepErr := t.sleep(request.Context(), delay); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		if response == nil || attempt >= t.maxRetries || !retryableResponse(response) {
			return response, err
		}

		delay, explicit := retryDelay(response, t.baseDelay, attempt)
		if !explicit {
			delay = t.jitter(delay)
		}
		reason := "transient"
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden {
			reason = "rate_limit"
			t.stats.RateLimits.Add(1)
		} else {
			t.stats.TransientErrors.Add(1)
		}
		t.recordRetry(request, response.StatusCode, delay, reason)
		_, _ = io.CopyN(io.Discard, response.Body, 64<<10)
		_ = response.Body.Close()
		if err := t.sleep(request.Context(), delay); err != nil {
			return nil, err
		}
	}
}

func (t *RetryTransport) recordResponse(request *http.Request, response *http.Response, latency time.Duration) {
	t.stats.RequestNanoseconds.Add(int64(latency))
	for {
		current := t.stats.MaxLatencyNanoseconds.Load()
		if int64(latency) <= current || t.stats.MaxLatencyNanoseconds.CompareAndSwap(current, int64(latency)) {
			break
		}
	}
	status := 0
	remaining, reset := int64(-1), int64(0)
	if response != nil {
		status = response.StatusCode
		if value, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Remaining"), 10, 64); err == nil {
			remaining = value
			t.stats.LastRateLimitRemaining.Store(value)
		}
		if value, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = value
			t.stats.LastRateLimitReset.Store(value)
		}
	}
	log.WithFields(logrus.Fields{
		"method": request.Method, "host": request.URL.Host, "status": status,
		"latency": latency, "rate_limit_remaining": remaining, "rate_limit_reset": reset,
	}).Debug("GitHub API request completed")
}

func (t *RetryTransport) recordRetry(request *http.Request, status int, delay time.Duration, reason string) {
	t.stats.Retries.Add(1)
	t.stats.WaitNanoseconds.Add(int64(delay))
	log.WithFields(logrus.Fields{
		"method": request.Method, "host": request.URL.Host, "status": status,
		"delay": delay, "reason": reason,
	}).Warn("retrying GitHub API request")
}

func (t *RetryTransport) Stats() RetryStatsSnapshot {
	return RetryStatsSnapshot{
		Attempts: t.stats.Attempts.Load(), Retries: t.stats.Retries.Load(),
		RateLimits: t.stats.RateLimits.Load(), TransientErrors: t.stats.TransientErrors.Load(),
		TransportErrors: t.stats.TransportErrors.Load(), TotalWait: time.Duration(t.stats.WaitNanoseconds.Load()),
		TotalRequestTime: time.Duration(t.stats.RequestNanoseconds.Load()), MaxLatency: time.Duration(t.stats.MaxLatencyNanoseconds.Load()),
		LastRateLimitRemaining: t.stats.LastRateLimitRemaining.Load(), LastRateLimitReset: t.stats.LastRateLimitReset.Load(),
	}
}

func retryableResponse(response *http.Response) bool {
	switch response.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusForbidden:
		return response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0"
	default:
		return false
	}
}

func retryDelay(response *http.Response, base time.Duration, attempt int) (time.Duration, bool) {
	if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return min(time.Duration(seconds)*time.Second, maxRetryDelay), true
		}
		if deadline, err := http.ParseTime(value); err == nil {
			return min(max(time.Until(deadline), 0), maxRetryDelay), true
		}
	}
	if value := response.Header.Get("X-RateLimit-Reset"); value != "" {
		if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
			return min(max(time.Until(time.Unix(unix, 0)), 0), maxRetryDelay), true
		}
	}
	return retryBackoff(base, attempt), false
}

func retryBackoff(base time.Duration, attempt int) time.Duration {
	return min(base<<attempt, maxRetryDelay)
}

func jitterDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	factor := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(delay) * factor)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
