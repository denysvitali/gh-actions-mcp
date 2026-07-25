package github

import (
	"github.com/google/go-github/v89/github"
)

type WorkflowRun struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion"`
	Branch          string  `json:"branch"`
	HeadSHA         string  `json:"head_sha,omitempty"`
	Event           string  `json:"event"`
	Actor           string  `json:"actor"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	StartedAt       string  `json:"started_at,omitempty"`
	URL             string  `json:"url"`
	RunNumber       int     `json:"run_number"`
	WorkflowID      int64   `json:"workflow_id"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// WorkflowRunMinimal is a compact workflow run representation for reduced token usage
type WorkflowRunMinimal struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion,omitempty"`
	CreatedAt       string  `json:"created_at"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// WorkflowRunCompact extends Minimal with additional fields
type WorkflowRunCompact struct {
	WorkflowRunMinimal
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Event  string `json:"event,omitempty"`
	Actor  string `json:"actor,omitempty"`
	URL    string `json:"url,omitempty"`
}

// WorkflowRunFull is the complete workflow run representation
type WorkflowRunFull struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion"`
	Branch          string  `json:"branch"`
	Event           string  `json:"event"`
	Actor           string  `json:"actor"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	URL             string  `json:"url"`
	RunNumber       int     `json:"run_number"`
	WorkflowID      int64   `json:"workflow_id"`
	HeadSHA         string  `json:"head_sha"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// Step represents a single step within a workflow job
type Step struct {
	Name            string  `json:"name"`
	Number          int64   `json:"number"`
	Status          string  `json:"status"`
	Conclusion      string  `json:"conclusion,omitempty"`
	StartedAt       string  `json:"started_at,omitempty"`
	CompletedAt     string  `json:"completed_at,omitempty"`
	DurationSeconds float64 `json:"duration,omitempty"`
}

// Job represents a workflow run job
type Job struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Conclusion      string   `json:"conclusion,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"`
	CompletedAt     string   `json:"completed_at,omitempty"`
	DurationSeconds float64  `json:"duration_seconds,omitempty"`
	RunnerName      string   `json:"runner_name,omitempty"`
	RunnerGroup     string   `json:"runner_group,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	WorkflowRunID   int64    `json:"workflow_run_id"`
	Steps           []*Step  `json:"steps,omitempty"`
}

// workflowRunFromGitHub converts a github.WorkflowRun to our WorkflowRun type
func workflowRunFromGitHub(run *github.WorkflowRun) *WorkflowRun {
	updatedAt := run.GetUpdatedAt()
	return &WorkflowRun{
		ID:              run.GetID(),
		Name:            run.GetName(),
		Status:          run.GetStatus(),
		Conclusion:      run.GetConclusion(),
		Branch:          run.GetHeadBranch(),
		HeadSHA:         run.GetHeadSHA(),
		Event:           run.GetEvent(),
		Actor:           run.GetActor().GetLogin(),
		CreatedAt:       run.GetCreatedAt().String(),
		UpdatedAt:       updatedAt.String(),
		StartedAt:       formatTime(run.RunStartedAt),
		URL:             run.GetHTMLURL(),
		RunNumber:       run.GetRunNumber(),
		WorkflowID:      run.GetWorkflowID(),
		DurationSeconds: durationSeconds(run.RunStartedAt, &updatedAt),
	}
}

// formatTime formats a github.Timestamp pointer into an ISO string
func formatTime(t *github.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// durationSeconds returns the elapsed seconds between two timestamps.
// Returns 0 if either timestamp is nil or the duration is negative.
func durationSeconds(start, end *github.Timestamp) float64 {
	if start == nil || end == nil {
		return 0
	}
	d := end.Time.Sub(start.Time).Seconds()
	if d < 0 {
		return 0
	}
	return d
}

// formatTimeValue formats a github.Timestamp value into an ISO string
func formatTimeValue(t github.Timestamp) string {
	return t.String()
}
