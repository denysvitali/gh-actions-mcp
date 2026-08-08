package mcp

import (
	appgithub "github.com/denysvitali/gh-actions-mcp/github"
)

// The input and output types below are deliberately kept separate from the
// GitHub client models. They are the MCP wire contracts: optional fields remain
// optional, while the official SDK handles decoding and validation before
// invoking the corresponding typed business handler. A pointer field means
// "absent is distinguishable from zero"; a value field means the zero value is a
// valid default.
//
// Renaming a json tag here is a wire-contract change and will fail
// tools_schema_test.go.

// repoInput is embedded by every tool input: it carries the optional per-call
// repository override resolved by (*MCPServer).clientFromInput.
type repoInput struct {
	Owner string `json:"owner,omitempty" jsonschema:"repository owner override"`
	Repo  string `json:"repo,omitempty" jsonschema:"repository name override"`
}

type listWorkflowsInput struct {
	repoInput
	Limit  int    `json:"limit,omitempty"`
	Format string `json:"format,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type listWorkflowsOutput struct {
	Workflows  []*appgithub.Workflow `json:"workflows"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type listRunsInput struct {
	repoInput
	WorkflowID   any    `json:"workflow_id,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Status       string `json:"status,omitempty"`
	Conclusion   string `json:"conclusion,omitempty"`
	PerPage      int    `json:"per_page,omitempty"`
	CreatedAfter string `json:"created_after,omitempty"`
	Event        string `json:"event,omitempty"`
	Actor        string `json:"actor,omitempty"`
	Format       string `json:"format,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
}

type listRunsOutput struct {
	Runs       []any  `json:"runs"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// getRunInput is the union of every element's arguments; which fields are
// meaningful depends on Element. See runElements for the dispatch.
type getRunInput struct {
	repoInput
	RunID         int64  `json:"run_id"`
	Element       string `json:"element,omitempty"`
	ArtifactID    *int64 `json:"artifact_id,omitempty"`
	FilePattern   string `json:"file_pattern,omitempty"`
	MaxFileSize   int64  `json:"max_file_size,omitempty"`
	JobID         *int64 `json:"job_id,omitempty"`
	PerJob        bool   `json:"per_job,omitempty"`
	AttemptNumber *int64 `json:"attempt_number,omitempty"`
	Head          *int64 `json:"head,omitempty"`
	Tail          *int64 `json:"tail,omitempty"`
	Offset        *int64 `json:"offset,omitempty"`
	Search        string `json:"search,omitempty"`
	SearchRegex   string `json:"search_regex,omitempty"`
	Context       int    `json:"context,omitempty"`
	NoHeaders     bool   `json:"no_headers,omitempty"`
	Section       string `json:"section,omitempty"`
	Format        string `json:"format,omitempty"`
}

type analyzeTimingInput struct {
	repoInput
	Workflow   string `json:"workflow,omitempty"`
	RunID      *int64 `json:"run_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
	JobName    string `json:"job_name,omitempty"`
	StepName   string `json:"step_name,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type checkStatusInput struct {
	repoInput
	Ref       string `json:"ref,omitempty"`
	CheckName string `json:"check_name,omitempty"`
	Status    string `json:"status,omitempty"`
	Filter    string `json:"filter,omitempty"`
	Format    string `json:"format,omitempty"`
}

// waitRunInput is shared by wait_for_run and wait_all, which take the same
// arguments and differ only in their completion predicate.
type waitRunInput struct {
	repoInput
	RunID          int64 `json:"run_id"`
	TimeoutMinutes int   `json:"timeout_minutes,omitempty"`
}

type waitChecksInput struct {
	repoInput
	Ref            string `json:"ref,omitempty"`
	TimeoutMinutes int    `json:"timeout_minutes,omitempty"`
}

type manageRunInput struct {
	repoInput
	RunID  int64  `json:"run_id"`
	Action string `json:"action"`
}

type artifactInput struct {
	repoInput
	ArtifactID  int64  `json:"artifact_id"`
	FilePattern string `json:"file_pattern,omitempty"`
	MaxFileSize int64  `json:"max_file_size,omitempty"`
}

type diagnoseInput struct {
	repoInput
	RunID          *int64 `json:"run_id,omitempty"`
	CheckFlakiness *bool  `json:"check_flakiness,omitempty"`
	MaxErrorLines  int    `json:"max_error_lines,omitempty"`
}

type downloadArtifactInput struct {
	repoInput
	ArtifactID int64  `json:"artifact_id"`
	OutputPath string `json:"output_path,omitempty"`
	Overwrite  bool   `json:"overwrite,omitempty"`
}
