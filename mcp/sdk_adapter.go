package mcp

import (
	appgithub "github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolBuilder keeps the existing schema declarations readable while producing
// schemas understood by the official SDK.
type toolBuilder struct{}

type toolDefinition struct {
	tool       *sdkmcp.Tool
	properties map[string]any
	required   []string
}

type toolOption func(any)

func (toolBuilder) NewTool(name string, options ...toolOption) *sdkmcp.Tool {
	d := &toolDefinition{
		tool:       &sdkmcp.Tool{Name: name},
		properties: make(map[string]any),
	}
	for _, option := range options {
		option(d)
	}
	d.tool.InputSchema = map[string]any{
		"type":       "object",
		"properties": d.properties,
	}
	if len(d.required) > 0 {
		d.tool.InputSchema.(map[string]any)["required"] = d.required
	}
	return d.tool
}

func (toolBuilder) WithDescription(description string) toolOption {
	return func(target any) {
		if d, ok := target.(*toolDefinition); ok {
			d.tool.Description = description
		}
	}
}

func (toolBuilder) ReadOnly() toolOption {
	return func(target any) {
		if d, ok := target.(*toolDefinition); ok {
			if d.tool.Annotations == nil {
				d.tool.Annotations = &sdkmcp.ToolAnnotations{}
			}
			d.tool.Annotations.ReadOnlyHint = true
			d.tool.Annotations.DestructiveHint = boolPtr(false)
			d.tool.Annotations.OpenWorldHint = boolPtr(true)
		}
	}
}

func (toolBuilder) Destructive() toolOption {
	return func(target any) {
		if d, ok := target.(*toolDefinition); ok {
			if d.tool.Annotations == nil {
				d.tool.Annotations = &sdkmcp.ToolAnnotations{}
			}
			d.tool.Annotations.DestructiveHint = boolPtr(true)
			d.tool.Annotations.OpenWorldHint = boolPtr(true)
		}
	}
}

func boolPtr(value bool) *bool { return &value }

func (toolBuilder) Description(description string) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["description"] = description
		}
	}
}

func (toolBuilder) Required() toolOption {
	return func(target any) {
		if property, ok := target.(*propertyDefinition); ok {
			property.required = true
		}
	}
}

func (toolBuilder) DefaultString(value string) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["default"] = value
		}
	}
}

func (toolBuilder) DefaultNumber(value float64) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["default"] = value
		}
	}
}

func (toolBuilder) Enum(values ...string) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["enum"] = values
		}
	}
}

func (toolBuilder) Minimum(value float64) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["minimum"] = value
		}
	}
}

func (toolBuilder) Maximum(value float64) toolOption {
	return func(target any) {
		if property, ok := target.(*map[string]any); ok {
			(*property)["maximum"] = value
		}
	}
}

type propertyDefinition struct {
	property map[string]any
	required bool
}

// The input types below are deliberately kept separate from the GitHub client
// models. They are the MCP wire contracts: optional fields remain optional,
// while the official SDK handles decoding and validation before invoking the
// corresponding typed business handler.
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
	WorkflowID   *int64 `json:"workflow_id,omitempty"`
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

func (toolBuilder) property(name, typ string, options ...toolOption) toolOption {
	return func(target any) {
		d, ok := target.(*toolDefinition)
		if !ok {
			return
		}
		property := map[string]any{"type": typ}
		definition := &propertyDefinition{property: property}
		for _, option := range options {
			option(definition)
			option(&property)
		}
		d.properties[name] = property
		if definition.required {
			d.required = append(d.required, name)
		}
	}
}

func (b toolBuilder) WithString(name string, options ...toolOption) toolOption {
	return b.property(name, "string", options...)
}

func (b toolBuilder) WithNumber(name string, options ...toolOption) toolOption {
	return b.property(name, "integer", options...)
}

func (b toolBuilder) WithBoolean(name string, options ...toolOption) toolOption {
	return b.property(name, "boolean", options...)
}

// addTool registers an end-to-end typed SDK handler. The legacy wire request
// is never reconstructed after the SDK has decoded and validated the input.
func (s *MCPServer) addTool(tool *sdkmcp.Tool) {
	switch tool.Name {
	case "list_workflows":
		sdkmcp.AddTool(s.srv, tool, s.listWorkflowsTyped)
	case "list_runs":
		sdkmcp.AddTool(s.srv, tool, s.listRunsTyped)
	case "get_run":
		sdkmcp.AddTool(s.srv, tool, s.getRunTyped)
	case "analyze_timing":
		sdkmcp.AddTool(s.srv, tool, s.analyzeTimingTyped)
	case "get_check_status":
		sdkmcp.AddTool(s.srv, tool, s.getCheckStatusTyped)
	case "wait_for_run":
		sdkmcp.AddTool(s.srv, tool, s.waitForRunTyped)
	case "wait_all":
		sdkmcp.AddTool(s.srv, tool, s.waitAllTyped)
	case "wait_for_commit_checks":
		sdkmcp.AddTool(s.srv, tool, s.waitForCommitChecksTyped)
	case "manage_run":
		sdkmcp.AddTool(s.srv, tool, s.manageRunTyped)
	case "get_artifact":
		sdkmcp.AddTool(s.srv, tool, s.getArtifactTyped)
	case "diagnose_failure":
		sdkmcp.AddTool(s.srv, tool, s.diagnoseFailureTyped)
	case "download_artifact":
		sdkmcp.AddTool(s.srv, tool, s.downloadArtifactTyped)
	default:
		panic("unknown tool: " + tool.Name)
	}
}
