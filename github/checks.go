package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v89/github"
)

// CheckRun represents a GitHub check run
type CheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	DetailsURL  string `json:"details_url,omitempty"`
}

// CombinedCheckStatus represents the combined status of all check runs for a commit
type CombinedCheckStatus struct {
	SHA          string         `json:"sha"`
	State        string         `json:"state"` // "pending", "success", "failure", "neutral"
	TotalCount   int            `json:"total_count"`
	CheckRuns    []*CheckRun    `json:"check_runs"`
	ByConclusion map[string]int `json:"by_conclusion"`
}

// GetCheckRunsOptions contains parameters for getting check runs
type GetCheckRunsOptions struct {
	CheckName string // Optional: filter by check name
	Status    string // Optional: queued, in_progress, completed
	Filter    string // Optional: "latest" (default) or "all"
}

func isLikelyCommitRef(ref string) bool {
	if len(ref) < 7 || len(ref) > 40 {
		return false
	}
	for _, ch := range ref {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// GetCheckRunsForRef retrieves status information for a ref using workflow runs.
// It intentionally avoids the GitHub Checks API because many PATs cannot access it.
func (c *Client) GetCheckRunsForRef(ctx context.Context, ref string, opts *GetCheckRunsOptions) (*CombinedCheckStatus, error) {
	if ref == "" {
		commit, err := GetLastCommit()
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		ref = commit.SHA
	}

	runOpts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}

	refIsCommit := isLikelyCommitRef(ref)
	if !refIsCommit {
		runOpts.Branch = ref
	}

	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, runOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for ref %s: %w", ref, err)
	}

	filterByName := ""
	filterByStatus := ""
	filterMode := "latest"
	if opts != nil {
		filterByName = opts.CheckName
		filterByStatus = opts.Status
		if opts.Filter == "all" {
			filterMode = "all"
		}
	}

	filtered := make([]*github.WorkflowRun, 0)
	for _, run := range runs.WorkflowRuns {
		if run == nil {
			continue
		}
		if refIsCommit {
			headSHA := strings.ToLower(run.GetHeadSHA())
			if !strings.HasPrefix(headSHA, strings.ToLower(ref)) {
				continue
			}
		}
		if filterByName != "" && run.GetName() != filterByName {
			continue
		}
		if filterByStatus != "" && run.GetStatus() != filterByStatus {
			continue
		}
		filtered = append(filtered, run)
	}

	if filterMode != "all" {
		latestByName := make(map[string]*github.WorkflowRun)
		for _, run := range filtered {
			name := run.GetName()
			if existing, ok := latestByName[name]; !ok {
				latestByName[name] = run
			} else {
				if run.GetRunNumber() > existing.GetRunNumber() {
					latestByName[name] = run
				}
			}
		}
		deduped := make([]*github.WorkflowRun, 0, len(latestByName))
		for _, run := range latestByName {
			deduped = append(deduped, run)
		}
		filtered = deduped
	}

	result := &CombinedCheckStatus{
		SHA:          ref,
		CheckRuns:    make([]*CheckRun, 0),
		ByConclusion: make(map[string]int),
	}

	// Convert workflow runs to check-like entries.
	for _, run := range filtered {
		checkRun := &CheckRun{
			ID:          run.GetID(),
			Name:        run.GetName(),
			Status:      run.GetStatus(),
			Conclusion:  run.GetConclusion(),
			StartedAt:   formatTime(run.RunStartedAt),
			CompletedAt: formatTime(run.UpdatedAt),
			AppName:     "github-actions",
			DetailsURL:  run.GetHTMLURL(),
		}
		result.CheckRuns = append(result.CheckRuns, checkRun)

		// Count by conclusion
		if run.GetConclusion() != "" {
			result.ByConclusion[run.GetConclusion()]++
		} else if run.GetStatus() != "completed" {
			result.ByConclusion[run.GetStatus()]++
		}
	}

	result.TotalCount = len(result.CheckRuns)

	// Determine overall state
	result.State = c.determineOverallState(result.CheckRuns)

	return result, nil
}

// determineOverallState determines the overall check status from individual check runs
func (c *Client) determineOverallState(checkRuns []*CheckRun) string {
	if len(checkRuns) == 0 {
		return "pending"
	}

	hasPending := false
	hasFailure := false
	hasSuccess := false

	for _, cr := range checkRuns {
		if cr.Status == "completed" {
			if cr.Conclusion == "failure" || cr.Conclusion == "timed_out" {
				hasFailure = true
			} else if cr.Conclusion == "success" {
				hasSuccess = true
			}
		} else {
			// queued, in_progress, etc.
			hasPending = true
		}
	}

	if hasPending {
		return "pending"
	}
	if hasFailure {
		return "failure"
	}
	if hasSuccess {
		return "success"
	}
	return "neutral"
}
