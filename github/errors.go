package github

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/go-github/v89/github"
)

// statusCodePattern extracts the numeric status code from a go-github error
// message such as "unexpected status code: 404 Not Found". The literal
// "status code: " prefix is required so that repository names like "401k" are
// not mistaken for a status code.
//
// Compiled once at package init: IsHTTPError is on the hot path of every
// not-found probe.
var statusCodePattern = regexp.MustCompile(`status code:\s*(\d+)`)

// HTTPError carries the HTTP status code of a failed GitHub API call alongside a
// human-readable message. Error returns Message verbatim, so the status code is
// only visible through the field or through a message that already embeds it
// (which is what newHTTPErrorFromGitHub produces).
type HTTPError struct {
	StatusCode int
	Message    string
}

// Error returns Message unchanged. It does not append the status code.
func (e *HTTPError) Error() string {
	return e.Message
}

// IsHTTPError reports whether err represents an HTTP failure with the given
// status code.
//
// It recognises two shapes: an *HTTPError passed directly (not wrapped — see the
// caveat below), and any error whose message contains go-github's
// "status code: <n>" rendering.
//
// Caveat: a wrapped *HTTPError is NOT recognised, because the check is a bare
// type assertion rather than errors.As, and HTTPError's own message format does
// not match statusCodePattern. That is a known defect, documented in
// FINDINGS.md#1 and deliberately left in place here; changing it would be a
// behaviour change. Callers that wrap must use errors.As directly.
func IsHTTPError(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	//nolint:errorlint // FINDINGS.md#1 deliberately preserves the direct-only contract in this refactor.
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode == statusCode
	}
	matches := statusCodePattern.FindStringSubmatch(err.Error())
	if len(matches) > 1 {
		code, _ := strconv.Atoi(matches[1])
		return code == statusCode
	}
	return false
}

// newHTTPErrorFromGitHub builds an *HTTPError from a github.Response. A nil
// response yields status code 0, so the caller always gets a non-nil error whose
// message ends in ": HTTP <code>".
func newHTTPErrorFromGitHub(resp *github.Response, msg string) error {
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	return &HTTPError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("%s: HTTP %d", msg, statusCode),
	}
}
