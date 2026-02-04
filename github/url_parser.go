package github

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ActionsURL represents a parsed GitHub Actions URL
type ActionsURL struct {
	Owner string
	Repo  string
	RunID int64
	JobID int64 // Optional, 0 if not present
}

// IsJobURL returns true if this URL contains a job ID
func (a *ActionsURL) IsJobURL() bool {
	return a.JobID > 0
}

// String returns a string representation of the URL
func (a *ActionsURL) String() string {
	if a.IsJobURL() {
		return fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", a.Owner, a.Repo, a.RunID, a.JobID)
	}
	return fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", a.Owner, a.Repo, a.RunID)
}

// Pre-compiled regex patterns for URL parsing
var (
	// runURLPattern matches: https://github.com/owner/repo/actions/runs/123456
	runURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/actions/runs/(\d+)/?$`)

	// jobURLPattern matches: https://github.com/owner/repo/actions/runs/123456/job/789012
	jobURLPattern = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/actions/runs/(\d+)/job/(\d+)/?$`)
)

// ParseActionsURL parses a GitHub Actions URL and extracts owner, repo, runID, and optional jobID
func ParseActionsURL(url string) (*ActionsURL, error) {
	url = strings.TrimSpace(url)

	// Try job URL pattern first (more specific)
	if matches := jobURLPattern.FindStringSubmatch(url); matches != nil {
		runID, err := strconv.ParseInt(matches[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid run ID in URL: %s", matches[3])
		}

		jobID, err := strconv.ParseInt(matches[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid job ID in URL: %s", matches[4])
		}

		return &ActionsURL{
			Owner: matches[1],
			Repo:  matches[2],
			RunID: runID,
			JobID: jobID,
		}, nil
	}

	// Try run URL pattern
	if matches := runURLPattern.FindStringSubmatch(url); matches != nil {
		runID, err := strconv.ParseInt(matches[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid run ID in URL: %s", matches[3])
		}

		return &ActionsURL{
			Owner: matches[1],
			Repo:  matches[2],
			RunID: runID,
			JobID: 0,
		}, nil
	}

	return nil, fmt.Errorf("unsupported GitHub Actions URL format: %s", url)
}

// IsActionsURL checks if a string looks like a GitHub Actions URL
func IsActionsURL(url string) bool {
	url = strings.TrimSpace(url)
	return runURLPattern.MatchString(url) || jobURLPattern.MatchString(url)
}

// ParseRunID parses a run ID string (numeric only)
func ParseRunID(id string) (int64, error) {
	id = strings.TrimSpace(id)
	return strconv.ParseInt(id, 10, 64)
}

// ParseJobID parses a job ID string (numeric only)
func ParseJobID(id string) (int64, error) {
	id = strings.TrimSpace(id)
	return strconv.ParseInt(id, 10, 64)
}
