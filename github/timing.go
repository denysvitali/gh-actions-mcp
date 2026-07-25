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
	Scope         string                 `json:"scope"`
	WorkflowID    int64                  `json:"workflow_id"`
	WorkflowName  string                 `json:"workflow_name"`
	Branch        string                 `json:"branch,omitempty"`
	JobName       string                 `json:"job_name,omitempty"`
	StepName      string                 `json:"step_name,omitempty"`
	SampleCount   int                    `json:"sample_count"`
	Statistics    *TimingStats           `json:"statistics"`
	Focus         *TimingComparison      `json:"focus"`
	RecentSamples []*TimingSample        `json:"recent_samples"`
	JobBreakdown  []*TimingBreakdownItem `json:"job_breakdown,omitempty"`
	StepBreakdown []*TimingBreakdownItem `json:"step_breakdown,omitempty"`
}

// AnalyzeTiming compares workflow, job, or step durations across recent runs.
func (c *Client) AnalyzeTiming(ctx context.Context, opts *TimingAnalysisOptions) (*TimingAnalysis, error) {
	if opts == nil {
		return nil, fmt.Errorf("timing analysis options are required")
	}
	if opts.StepName != "" && opts.JobName == "" {
		return nil, fmt.Errorf("job_name is required when step_name is provided")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	scope := "workflow"
	if opts.StepName != "" {
		scope = "step"
	} else if opts.JobName != "" {
		scope = "job"
	}

	var (
		focusRun     *WorkflowRun
		workflowID   int64
		workflowName string
		err          error
	)

	workflowSelector := strings.TrimSpace(opts.Workflow)
	if opts.RunID > 0 {
		focusRun, err = c.GetWorkflowRun(ctx, opts.RunID)
		if err != nil {
			return nil, fmt.Errorf("failed to get workflow run %d: %w", opts.RunID, err)
		}
		if focusRun.Status != "completed" {
			return nil, fmt.Errorf("workflow run %d is %s; timing analysis requires a completed run", opts.RunID, focusRun.Status)
		}
		if workflowSelector != "" {
			workflowID, workflowName, err = c.ResolveWorkflowID(ctx, workflowSelector)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve workflow %q: %w", workflowSelector, err)
			}
			if workflowID != focusRun.WorkflowID {
				return nil, fmt.Errorf("run %d belongs to workflow %q (%d), not %q (%d)", opts.RunID, focusRun.Name, focusRun.WorkflowID, workflowName, workflowID)
			}
		} else {
			workflowID = focusRun.WorkflowID
			workflowName = focusRun.Name
		}
		if opts.Conclusion != "" && focusRun.Conclusion != opts.Conclusion {
			return nil, fmt.Errorf("run %d concluded as %s, which does not match conclusion filter %q", opts.RunID, focusRun.Conclusion, opts.Conclusion)
		}
	} else {
		if workflowSelector == "" {
			return nil, fmt.Errorf("workflow is required when run_id is not provided")
		}
		workflowID, workflowName, err = c.ResolveWorkflowID(ctx, workflowSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workflow %q: %w", workflowSelector, err)
		}
	}

	runs, err := c.listWorkflowRunsForTiming(ctx, workflowID, opts.Branch, limit)
	if err != nil {
		return nil, err
	}

	filteredRuns := make([]*WorkflowRun, 0, len(runs)+1)
	for _, run := range runs {
		if !matchesTimingRun(run, opts.Conclusion) {
			continue
		}
		filteredRuns = append(filteredRuns, run)
	}
	if focusRun != nil && matchesTimingRun(focusRun, opts.Conclusion) {
		filteredRuns = appendTimingRunIfMissing(filteredRuns, focusRun)
	}

	sort.Slice(filteredRuns, func(i, j int) bool {
		return filteredRuns[i].RunNumber > filteredRuns[j].RunNumber
	})
	filteredRuns = limitTimingRuns(filteredRuns, limit, opts.RunID)

	if len(filteredRuns) == 0 {
		return nil, fmt.Errorf("no completed runs with timing data found for workflow %q", workflowName)
	}

	if focusRun == nil {
		focusRun = filteredRuns[0]
	}

	jobsByRun := make(map[int64][]*Job, len(filteredRuns))
	for _, run := range filteredRuns {
		jobs, err := c.GetWorkflowJobs(ctx, run.ID, "", 0)
		if err != nil {
			return nil, fmt.Errorf("failed to get jobs for run %d: %w", run.ID, err)
		}
		jobsByRun[run.ID] = jobs
	}

	samples := make([]*TimingSample, 0, len(filteredRuns))
	for _, run := range filteredRuns {
		sample, err := timingSampleForScope(run, jobsByRun[run.ID], scope, opts.JobName, opts.StepName)
		if err != nil {
			return nil, err
		}
		if sample != nil {
			samples = append(samples, sample)
		}
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("no timing samples matched scope %q for workflow %q", scope, workflowName)
	}

	focusIndex := indexOfTimingSample(samples, focusRun.ID)
	if focusIndex == -1 {
		return nil, fmt.Errorf("run %d does not include the requested %s", focusRun.ID, scope)
	}

	baselineSamples := samplesExcludingIndex(samples, focusIndex)
	if len(baselineSamples) == 0 {
		baselineSamples = samples
	}

	analysis := &TimingAnalysis{
		Scope:         scope,
		WorkflowID:    workflowID,
		WorkflowName:  workflowName,
		Branch:        opts.Branch,
		JobName:       opts.JobName,
		StepName:      opts.StepName,
		SampleCount:   len(samples),
		Statistics:    timingStatsFromSamples(samples),
		Focus:         compareTimingSample(samples[focusIndex], timingStatsFromSamples(baselineSamples), previousTimingSample(samples, focusIndex)),
		RecentSamples: samples,
	}

	switch scope {
	case "workflow":
		analysis.JobBreakdown = buildJobBreakdown(jobsByRun, focusRun.ID)
		analysis.StepBreakdown = buildStepBreakdown(jobsByRun, focusRun.ID, opts.JobName)
	case "job":
		analysis.StepBreakdown = buildStepBreakdown(jobsByRun, focusRun.ID, opts.JobName)
	}

	return analysis, nil
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

func timingSampleForScope(run *WorkflowRun, jobs []*Job, scope, jobName, stepName string) (*TimingSample, error) {
	switch scope {
	case "workflow":
		return newTimingSample(run, run.DurationSeconds), nil
	case "job":
		job := findJobByName(jobs, jobName)
		if job == nil {
			return nil, nil
		}
		if job.DurationSeconds <= 0 {
			return nil, nil
		}
		return newTimingSample(run, job.DurationSeconds), nil
	case "step":
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
