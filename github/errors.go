package github

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/go-github/v89/github"
)

// HTTPError represents an HTTP error with a status code
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return e.Message
}

// IsHTTPError checks if an error is an HTTPError with a specific status code
func IsHTTPError(err error, statusCode int) bool {
	if err == nil {
		return false
	}
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr.StatusCode == statusCode
	}
	// Check for go-github error format: "unexpected status code: 404 Not Found"
	// This regex specifically matches the status code pattern at the end of the message
	// preceded by "status code: " to avoid matching repo names like "401k"
	re := regexp.MustCompile(`status code:\s*(\d+)`)
	matches := re.FindStringSubmatch(err.Error())
	if len(matches) > 1 {
		code, _ := strconv.Atoi(matches[1])
		return code == statusCode
	}
	return false
}

// newHTTPErrorFromGitHub creates an HTTPError from a github.Response
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
