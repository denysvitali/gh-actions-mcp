package mcp

import (
	"github.com/denysvitali/gh-actions-mcp/github"
)

// Projections of a workflow run onto the three verbosity levels the tools
// expose. list_runs and get_run element=info both render runs, and they must
// render them identically, so the field mapping lives here once. Adding a field
// to one of the github.WorkflowRun* shapes means adding it here, in one place.

// minimalRun keeps only the fields needed to identify a run and see its outcome.
func minimalRun(run *github.WorkflowRun) github.WorkflowRunMinimal {
	return github.WorkflowRunMinimal{
		ID:              run.ID,
		Name:            run.Name,
		Status:          run.Status,
		Conclusion:      run.Conclusion,
		CreatedAt:       run.CreatedAt,
		DurationSeconds: run.DurationSeconds,
	}
}

// compactRun is the default projection: minimal plus the fields needed to locate
// the run in the repository and open it in a browser.
func compactRun(run *github.WorkflowRun) *github.WorkflowRunCompact {
	return &github.WorkflowRunCompact{
		WorkflowRunMinimal: minimalRun(run),
		Branch:             run.Branch,
		SHA:                run.HeadSHA,
		Event:              run.Event,
		Actor:              run.Actor,
		URL:                run.URL,
	}
}

// fullRun is the complete projection. CompletedAt is deliberately UpdatedAt: the
// Actions API exposes no separate completion timestamp on a run, and for a
// finished run its last update is when it finished.
func fullRun(run *github.WorkflowRun) *github.WorkflowRunFull {
	return &github.WorkflowRunFull{
		ID:              run.ID,
		Name:            run.Name,
		Status:          run.Status,
		Conclusion:      run.Conclusion,
		Branch:          run.Branch,
		Event:           run.Event,
		Actor:           run.Actor,
		CreatedAt:       run.CreatedAt,
		UpdatedAt:       run.UpdatedAt,
		URL:             run.URL,
		RunNumber:       run.RunNumber,
		WorkflowID:      run.WorkflowID,
		HeadSHA:         run.HeadSHA,
		StartedAt:       run.StartedAt,
		CompletedAt:     run.UpdatedAt,
		DurationSeconds: run.DurationSeconds,
	}
}
