package github

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	githubapi "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPErrorError(t *testing.T) {
	t.Parallel()

	// Error() returns Message verbatim; it does not re-render StatusCode.
	err := &HTTPError{StatusCode: 404, Message: "nope"}
	assert.Equal(t, "nope", err.Error())
}

func TestIsHTTPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		statusCode int
		want       bool
	}{
		{name: "nil error", err: nil, statusCode: 404, want: false},
		{
			name:       "direct HTTPError matching",
			err:        &HTTPError{StatusCode: 404, Message: "nope"},
			statusCode: 404,
			want:       true,
		},
		{
			name:       "direct HTTPError not matching",
			err:        &HTTPError{StatusCode: 500, Message: "boom"},
			statusCode: 404,
			want:       false,
		},
		{
			name:       "go-github style message",
			err:        errors.New("unexpected status code: 404 Not Found"),
			statusCode: 404,
			want:       true,
		},
		{
			name:       "go-github style message with different code",
			err:        errors.New("unexpected status code: 502 Bad Gateway"),
			statusCode: 404,
			want:       false,
		},
		{
			name:       "repo name that looks like a status code is not matched",
			err:        errors.New("repository 401k not found"),
			statusCode: 401,
			want:       false,
		},
		{
			name:       "plain message without a status code",
			err:        errors.New("dial tcp: connection refused"),
			statusCode: 404,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsHTTPError(tt.err, tt.statusCode))
		})
	}
}

// TestIsHTTPError_WrappedIsMissed pins the CURRENT, BUGGY behaviour: a
// *HTTPError wrapped with %w is not recognised, because IsHTTPError uses a bare
// type assertion instead of errors.As, and the message-regex fallback does not
// match HTTPError's "<msg>: HTTP <code>" rendering.
//
// See FINDINGS.md#1. Do not "fix" this test — fix IsHTTPError in a separate
// fix: commit and update this test then.
func TestIsHTTPError_WrappedIsMissed(t *testing.T) {
	t.Parallel()

	base := newHTTPErrorFromGitHub(&githubapi.Response{Response: &http.Response{StatusCode: 404}}, "get run")
	require.Equal(t, "get run: HTTP 404", base.Error())

	wrapped := fmt.Errorf("get run: %w", base)

	assert.True(t, IsHTTPError(base, 404), "unwrapped error is recognised")
	assert.False(t, IsHTTPError(wrapped, 404), "FINDINGS.md#1: wrapped error is NOT recognised")

	var target *HTTPError
	assert.True(t, errors.As(wrapped, &target), "errors.As would recognise it")
}

func TestNewHTTPErrorFromGitHub(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resp     *githubapi.Response
		msg      string
		wantCode int
		wantMsg  string
	}{
		{
			name:     "nil response yields status code 0",
			resp:     nil,
			msg:      "failed to get workflow logs",
			wantCode: 0,
			wantMsg:  "failed to get workflow logs: HTTP 0",
		},
		{
			name:     "status code is copied from the response",
			resp:     &githubapi.Response{Response: &http.Response{StatusCode: 410}},
			msg:      "gone",
			wantCode: 410,
			wantMsg:  "gone: HTTP 410",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := newHTTPErrorFromGitHub(tt.resp, tt.msg)
			var httpErr *HTTPError
			require.ErrorAs(t, err, &httpErr)
			assert.Equal(t, tt.wantCode, httpErr.StatusCode)
			assert.Equal(t, tt.wantMsg, httpErr.Error())
		})
	}
}
