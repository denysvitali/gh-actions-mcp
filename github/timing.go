package github

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v89/github"
)

// TimingAnalysisOptions contains parameters for workflow/job/step timing analysis.
type TimingAnalysisOptions struct {
	Workflow   string
	RunID      int64
	Branch     string
	JobName    string
	StepName   string
	Limit      int
	Conclusion string
}

// TimingStats summarizes durations across a set of runs.
type TimingStats struct {
	Count          int     `json:"count"`
	AverageSeconds float64 `json:"average_seconds"`
	MedianSeconds  float64 `json:"median_seconds"`
	MinSeconds     float64 `json:"min_seconds"`
	MaxSeconds     float64 `json:"max_seconds"`
}

// TimingSample captures a single workflow/job/step duration in a given run.
type TimingSample struct {
	RunID           int64   `json:"run_id"`
	RunNumber       int     `json:"run_number"`
	CreatedAt       string  `json:"created_at,omitempty"`
	Conclusion      string  `json:"conclusion,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// TimingComparison compares a focus run against recent history.
type TimingComparison struct {
	*TimingSample
	DeltaFromAverageSeconds  float64 `json:"delta_from_average_seconds"`
	DeltaFromAveragePercent  float64 `json:"delta_from_average_percent"`
	DeltaFromPreviousSeconds float64 `json:"delta_from_previous_seconds,omitempty"`
	DeltaFromPreviousPercent float64 `json:"delta_from_previous_percent,omitempty"`
}

// TimingBreakdownItem highlights a job or step within the focus run.
type TimingBreakdownItem struct {
	JobName                 string  `json:"job_name,omitempty"`
	StepName                string  `json:"step_name,omitempty"`
	DurationSeconds         float64 `json:"duration_seconds"`
	AverageDurationSeconds  float64 `json:"average_duration_seconds"`
	DeltaFromAverageSeconds float64 `json:"delta_from_average_seconds"`
	DeltaFromAveragePercent float64 `json:"delta_from_average_percent"`
}

// TimingAnalysis is the full result of comparing workflow/job/step timings.
type TimingAnalysis struct {
	Scope                string                 `json:"scope"`
	WorkflowID           int64                  `json:"workflow_id"`
	WorkflowName         string                 `json:"workflow_name"`
	Branch               string                 `json:"branch,omitempty"`
	JobName              string                 `json:"job_name,omitempty"`
	StepName             string                 `json:"step_name,omitempty"`
	SampleCount          int                    `json:"sample_count"`
	Statistics           *TimingStats           `json:"statistics"`
	Focus                *TimingComparison      `json:"focus"`
	RecentSamples        []*TimingSample        `json:"recent_samples"`
	JobBreakdown         []*TimingBreakdownItem `json:"job_breakdown,omitempty"`
	StepBreakdown        []*TimingBreakdownItem `json:"step_breakdown,omitempty"`
	JobBreakdownOmitted  int                    `json:"job_breakdown_omitted,omitempty"`
	StepBreakdownOmitted int                    `json:"step_breakdown_omitted,omitempty"`
}

// timingScope names the granularity a TimingAnalysis was computed at.
const (
	timingScopeWorkflow = "workflow"
	timingScopeJob      = "job"
	timingScopeStep     = "step"
)

// defaultTimingLimit is how many recent runs are sampled when Limit is unset.
const defaultTimingLimit = 10

// timingTarget is the resolved workflow the analysis runs against, plus the
// optional focus run the caller asked to compare.
type timingTarget struct {
	workflowID   int64
	workflowName string
	// focusRun is nil when the caller did not pin a run; the newest sampled run
	// then becomes the focus.
	focusRun *WorkflowRun
}

// AnalyzeTiming compares workflow, job, or step durations across recent runs of
// one workflow.
//
// Scope is derived from opts: StepName selects "step" (and requires JobName),
// JobName alone selects "job", neither selects "workflow". Exactly one of
// opts.RunID and opts.Workflow must identify the workflow; when RunID is given
// the run must already be completed and, if Workflow is also given, the two must
// agree.
//
// It returns an error when nothing matches: no completed runs with timing data,
// no samples for the requested scope, or a focus run that does not contain the
// requested job or step.
func (c *Client) AnalyzeTiming(ctx context.Context, opts *TimingAnalysisOptions) (*TimingAnalysis, error) {
	if opts == nil {
		return nil, fmt.Errorf("timing analysis options are required")
	}
	if opts.StepName != "" && opts.JobName == "" {
		return nil, fmt.Errorf("job_name is required when step_name is provided")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultTimingLimit
	}
	scope := timingScopeFor(opts)

	target, err := c.resolveTimingTarget(ctx, opts)
	if err != nil {
		return nil, err
	}

	runs, err := c.sampledTimingRuns(ctx, opts, target, limit)
	if err != nil {
		return nil, err
	}
	focusRun := target.focusRun
	if focusRun == nil {
		focusRun = runs[0]
	}

	jobsByRun, err := c.jobsForTimingRuns(ctx, runs)
	if err != nil {
		return nil, err
	}

	samples, err := timingSamples(runs, jobsByRun, scope, opts.JobName, opts.StepName)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("no timing samples matched scope %q for workflow %q", scope, target.workflowName)
	}

	focusIndex := indexOfTimingSample(samples, focusRun.ID)
	if focusIndex == -1 {
		return nil, fmt.Errorf("run %d does not include the requested %s", focusRun.ID, scope)
	}

	analysis := &TimingAnalysis{
		Scope:         scope,
		WorkflowID:    target.workflowID,
		WorkflowName:  target.workflowName,
		Branch:        opts.Branch,
		JobName:       opts.JobName,
		StepName:      opts.StepName,
		SampleCount:   len(samples),
		Statistics:    timingStatsFromSamples(samples),
		Focus:         focusComparison(samples, focusIndex),
		RecentSamples: samples,
	}
	addTimingBreakdowns(analysis, scope, jobsByRun, focusRun.ID, opts.JobName)
	return analysis, nil
}

// timingScopeFor derives the analysis scope from the requested filters.
func timingScopeFor(opts *TimingAnalysisOptions) string {
	switch {
	case opts.StepName != "":
		return timingScopeStep
	case opts.JobName != "":
		return timingScopeJob
	default:
		return timingScopeWorkflow
	}
}

// resolveTimingTarget determines which workflow to analyse, and validates the
// focus run against the caller's filters when one was pinned.
func (c *Client) resolveTimingTarget(ctx context.Context, opts *TimingAnalysisOptions) (timingTarget, error) {
	selector := strings.TrimSpace(opts.Workflow)

	if opts.RunID <= 0 {
		if selector == "" {
			return timingTarget{}, fmt.Errorf("workflow is required when run_id is not provided")
		}
		id, name, err := c.ResolveWorkflowID(ctx, selector)
		if err != nil {
			return timingTarget{}, fmt.Errorf("failed to resolve workflow %q: %w", selector, err)
		}
		return timingTarget{workflowID: id, workflowName: name}, nil
	}

	focusRun, err := c.GetWorkflowRun(ctx, opts.RunID)
	if err != nil {
		return timingTarget{}, fmt.Errorf("failed to get workflow run %d: %w", opts.RunID, err)
	}
	if focusRun.Status != "completed" {
		return timingTarget{}, fmt.Errorf("workflow run %d is %s; timing analysis requires a completed run", opts.RunID, focusRun.Status)
	}

	target := timingTarget{workflowID: focusRun.WorkflowID, workflowName: focusRun.Name, focusRun: focusRun}
	if selector != "" {
		id, name, err := c.ResolveWorkflowID(ctx, selector)
		if err != nil {
			return timingTarget{}, fmt.Errorf("failed to resolve workflow %q: %w", selector, err)
		}
		if id != focusRun.WorkflowID {
			return timingTarget{}, fmt.Errorf("run %d belongs to workflow %q (%d), not %q (%d)", opts.RunID, focusRun.Name, focusRun.WorkflowID, name, id)
		}
		target.workflowID, target.workflowName = id, name
	}
	if opts.Conclusion != "" && focusRun.Conclusion != opts.Conclusion {
		return timingTarget{}, fmt.Errorf("run %d concluded as %s, which does not match conclusion filter %q", opts.RunID, focusRun.Conclusion, opts.Conclusion)
	}
	return target, nil
}

// sampledTimingRuns fetches recent runs, drops those without usable timing data,
// re-adds the focus run if the page missed it, and trims to limit newest-first.
// The returned slice is never empty.
func (c *Client) sampledTimingRuns(ctx context.Context, opts *TimingAnalysisOptions, target timingTarget, limit int) ([]*WorkflowRun, error) {
	runs, err := c.listWorkflowRunsForTiming(ctx, target.workflowID, opts.Branch, limit)
	if err != nil {
		return nil, err
	}

	filtered := make([]*WorkflowRun, 0, len(runs)+1)
	for _, run := range runs {
		if matchesTimingRun(run, opts.Conclusion) {
			filtered = append(filtered, run)
		}
	}
	if target.focusRun != nil && matchesTimingRun(target.focusRun, opts.Conclusion) {
		filtered = appendTimingRunIfMissing(filtered, target.focusRun)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].RunNumber > filtered[j].RunNumber
	})
	filtered = limitTimingRuns(filtered, limit, opts.RunID)

	if len(filtered) == 0 {
		return nil, fmt.Errorf("no completed runs with timing data found for workflow %q", target.workflowName)
	}
	return filtered, nil
}

// jobsForTimingRuns fetches the job list of every sampled run. One failure aborts
// the whole analysis: a partial baseline would silently skew every delta.
func (c *Client) jobsForTimingRuns(ctx context.Context, runs []*WorkflowRun) (map[int64][]*Job, error) {
	jobsByRun := make(map[int64][]*Job, len(runs))
	for _, run := range runs {
		jobs, err := c.GetWorkflowJobs(ctx, run.ID, "", 0)
		if err != nil {
			return nil, fmt.Errorf("failed to get jobs for run %d: %w", run.ID, err)
		}
		jobsByRun[run.ID] = jobs
	}
	return jobsByRun, nil
}

// timingSamples reduces each run to one duration for the requested scope. Runs
// that do not contain the requested job or step contribute no sample, so the
// result can be shorter than runs.
func timingSamples(runs []*WorkflowRun, jobsByRun map[int64][]*Job, scope, jobName, stepName string) ([]*TimingSample, error) {
	samples := make([]*TimingSample, 0, len(runs))
	for _, run := range runs {
		sample, err := timingSampleForScope(run, jobsByRun[run.ID], scope, jobName, stepName)
		if err != nil {
			return nil, err
		}
		if sample != nil {
			samples = append(samples, sample)
		}
	}
	return samples, nil
}

// focusComparison compares the focus sample against the other samples. With only
// one sample the focus is its own baseline, so the deltas are zero rather than
// undefined.
func focusComparison(samples []*TimingSample, focusIndex int) *TimingComparison {
	baseline := samplesExcludingIndex(samples, focusIndex)
	if len(baseline) == 0 {
		baseline = samples
	}
	return compareTimingSample(samples[focusIndex], timingStatsFromSamples(baseline), previousTimingSample(samples, focusIndex))
}

// addTimingBreakdowns attaches the per-job and per-step breakdowns that make
// sense for the scope. Step scope gets none: the sample already *is* one step.
func addTimingBreakdowns(analysis *TimingAnalysis, scope string, jobsByRun map[int64][]*Job, focusRunID int64, jobName string) {
	switch scope {
	case timingScopeWorkflow:
		analysis.JobBreakdown, analysis.JobBreakdownOmitted = buildJobBreakdown(jobsByRun, focusRunID)
		analysis.StepBreakdown, analysis.StepBreakdownOmitted = buildStepBreakdown(jobsByRun, focusRunID, jobName)
	case timingScopeJob:
		analysis.StepBreakdown, analysis.StepBreakdownOmitted = buildStepBreakdown(jobsByRun, focusRunID, jobName)
	}
}

func (c *Client) listWorkflowRunsForTiming(ctx context.Context, workflowID int64, branch string, limit int) ([]*WorkflowRun, error) {
	perPage := c.perPageLimit
	target := limit * 3
	if target < 20 {
		target = 20
	}
	if perPage < target {
		perPage = target
	}
	if perPage > 100 {
		perPage = 100
	}
	if perPage <= 0 {
		perPage = 50
	}

	opts := &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: perPage},
	}
	if branch != "" {
		opts.Branch = branch
	}

	runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, c.owner, c.repo, workflowID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs for workflow %d: %w", workflowID, err)
	}

	result := make([]*WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		result = append(result, workflowRunFromGitHub(run))
	}
	return result, nil
}

func matchesTimingRun(run *WorkflowRun, conclusion string) bool {
	if run == nil || run.Status != "completed" || run.DurationSeconds <= 0 {
		return false
	}
	if conclusion != "" && run.Conclusion != conclusion {
		return false
	}
	return true
}

func appendTimingRunIfMissing(runs []*WorkflowRun, focus *WorkflowRun) []*WorkflowRun {
	for _, run := range runs {
		if run.ID == focus.ID {
			return runs
		}
	}
	return append(runs, focus)
}

func limitTimingRuns(runs []*WorkflowRun, limit int, focusRunID int64) []*WorkflowRun {
	if limit <= 0 || len(runs) <= limit {
		return runs
	}
	if focusRunID == 0 {
		return runs[:limit]
	}

	focusIndex := -1
	for i, run := range runs {
		if run.ID == focusRunID {
			focusIndex = i
			break
		}
	}
	if focusIndex == -1 || focusIndex < limit {
		return runs[:limit]
	}

	limited := append([]*WorkflowRun{}, runs[:limit-1]...)
	limited = append(limited, runs[focusIndex])
	sort.Slice(limited, func(i, j int) bool {
		return limited[i].RunNumber > limited[j].RunNumber
	})
	return limited
}

// timingSampleForScope reduces one run to the single duration the scope asks for.
// A nil sample (with a nil error) means "this run has nothing to contribute":
// the job or step is absent, or its duration is unknown.
func timingSampleForScope(run *WorkflowRun, jobs []*Job, scope, jobName, stepName string) (*TimingSample, error) {
	switch scope {
	case timingScopeWorkflow:
		return newTimingSample(run, run.DurationSeconds), nil
	case timingScopeJob:
		job := findJobByName(jobs, jobName)
		if job == nil || job.DurationSeconds <= 0 {
			return nil, nil
		}
		return newTimingSample(run, job.DurationSeconds), nil
	case timingScopeStep:
		job := findJobByName(jobs, jobName)
		if job == nil {
			return nil, nil
		}
		step := findStepByName(job.Steps, stepName)
		if step == nil || step.DurationSeconds <= 0 {
			return nil, nil
		}
		return newTimingSample(run, step.DurationSeconds), nil
	default:
		return nil, fmt.Errorf("unknown timing scope %q", scope)
	}
}

func newTimingSample(run *WorkflowRun, duration float64) *TimingSample {
	return &TimingSample{
		RunID:           run.ID,
		RunNumber:       run.RunNumber,
		CreatedAt:       run.CreatedAt,
		Conclusion:      run.Conclusion,
		DurationSeconds: duration,
	}
}

func findJobByName(jobs []*Job, name string) *Job {
	normalized := normalizeTimingName(name)
	for _, job := range jobs {
		if normalizeTimingName(job.Name) == normalized {
			return job
		}
	}
	return nil
}

func findStepByName(steps []*Step, name string) *Step {
	normalized := normalizeTimingName(name)
	for _, step := range steps {
		if normalizeTimingName(step.Name) == normalized {
			return step
		}
	}
	return nil
}

func normalizeTimingName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
