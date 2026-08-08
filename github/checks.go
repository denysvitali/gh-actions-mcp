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

// isLikelyCommitRef reports whether ref looks like a (possibly abbreviated) commit
// SHA: 7 to 40 hex digits. Anything else is treated as a branch name. The
// heuristic is deliberately loose — a 7-hex-character branch name would be
// misclassified, which is why GetCheckRunsForRef still prefix-matches head SHAs
// client-side rather than trusting it.
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

// checkRunFilter is the resolved form of GetCheckRunsOptions plus the ref
// classification, so the filtering pass needs no nil checks.
type checkRunFilter struct {
	ref         string
	refIsCommit bool
	name        string
	status      string
	// keepAll disables the "newest run per workflow name" deduplication.
	keepAll bool
}

// newCheckRunFilter resolves opts (which may be nil) against ref.
func newCheckRunFilter(ref string, opts *GetCheckRunsOptions) checkRunFilter {
	filter := checkRunFilter{ref: ref, refIsCommit: isLikelyCommitRef(ref)}
	if opts != nil {
		filter.name = opts.CheckName
		filter.status = opts.Status
		filter.keepAll = opts.Filter == "all"
	}
	return filter
}

// matches reports whether a workflow run belongs in the result. A commit-like ref
// is matched case-insensitively as a prefix of the run's head SHA, which is what
// makes abbreviated SHAs work.
func (f checkRunFilter) matches(run *github.WorkflowRun) bool {
	if run == nil {
		return false
	}
	if f.refIsCommit && !strings.HasPrefix(strings.ToLower(run.GetHeadSHA()), strings.ToLower(f.ref)) {
		return false
	}
	if f.name != "" && run.GetName() != f.name {
		return false
	}
	if f.status != "" && run.GetStatus() != f.status {
		return false
	}
	return true
}

// GetCheckRunsForRef reports the combined CI status of a ref, synthesised from
// workflow runs rather than the Checks API — many fine-grained PATs cannot read
// check runs, but every token that can list runs can produce this.
//
// An empty ref resolves to the local HEAD commit. A ref that looks like a SHA is
// prefix-matched against run head SHAs client-side; anything else is passed to
// GitHub as a branch filter. By default only the newest run per workflow name is
// kept; opts.Filter == "all" keeps every matching run. The returned CheckRuns are
// in GitHub's page order for "all" and in unspecified (map) order otherwise.
func (c *Client) GetCheckRunsForRef(ctx context.Context, ref string, opts *GetCheckRunsOptions) (*CombinedCheckStatus, error) {
	if ref == "" {
		commit, err := GetLastCommit()
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		ref = commit.SHA
	}

	filter := newCheckRunFilter(ref, opts)
	runOpts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: c.perPageLimit},
	}
	if !filter.refIsCommit {
		runOpts.Branch = ref
	}

	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.owner, c.repo, runOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for ref %s: %w", ref, err)
	}

	matched := make([]*github.WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		if filter.matches(run) {
			matched = append(matched, run)
		}
	}
	if !filter.keepAll {
		matched = latestRunPerName(matched)
	}

	return c.combinedStatus(ref, matched), nil
}

// latestRunPerName keeps, for each workflow name, the run with the highest run
// number. Order is not preserved: the result comes out of a map.
func latestRunPerName(runs []*github.WorkflowRun) []*github.WorkflowRun {
	latest := make(map[string]*github.WorkflowRun, len(runs))
	for _, run := range runs {
		name := run.GetName()
		if existing, ok := latest[name]; !ok || run.GetRunNumber() > existing.GetRunNumber() {
			latest[name] = run
		}
	}
	deduped := make([]*github.WorkflowRun, 0, len(latest))
	for _, run := range latest {
		deduped = append(deduped, run)
	}
	return deduped
}

// combinedStatus projects workflow runs onto the check-run shape and aggregates
// them. A run's conclusion is counted when it has one; otherwise its
// still-running status is counted instead, so pending work is visible in
// ByConclusion.
func (c *Client) combinedStatus(ref string, runs []*github.WorkflowRun) *CombinedCheckStatus {
	result := &CombinedCheckStatus{
		SHA:          ref,
		CheckRuns:    make([]*CheckRun, 0, len(runs)),
		ByConclusion: make(map[string]int),
	}

	for _, run := range runs {
		result.CheckRuns = append(result.CheckRuns, &CheckRun{
			ID:          run.GetID(),
			Name:        run.GetName(),
			Status:      run.GetStatus(),
			Conclusion:  run.GetConclusion(),
			StartedAt:   formatTime(run.RunStartedAt),
			CompletedAt: formatTime(run.UpdatedAt),
			AppName:     "github-actions",
			DetailsURL:  run.GetHTMLURL(),
		})

		switch {
		case run.GetConclusion() != "":
			result.ByConclusion[run.GetConclusion()]++
		case run.GetStatus() != "completed":
			result.ByConclusion[run.GetStatus()]++
		}
	}

	result.TotalCount = len(result.CheckRuns)
	result.State = c.determineOverallState(result.CheckRuns)
	return result
}

// determineOverallState aggregates individual check runs into one state.
// Precedence is pending > failure > success > neutral: any unfinished check makes
// the whole ref pending, and a set of only skipped or cancelled checks is neutral.
// An empty set is pending.
func (c *Client) determineOverallState(checkRuns []*CheckRun) string {
	if len(checkRuns) == 0 {
		return "pending"
	}

	hasPending := false
	hasFailure := false
	hasSuccess := false

	for _, cr := range checkRuns {
		if cr.Status == "completed" {
			switch cr.Conclusion {
			case "failure", "timed_out":
				hasFailure = true
			case "success":
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
