package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/denysvitali/gh-actions-mcp/github"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The helpers in this file build every CallToolResult this package returns. The
// exact bytes they produce are part of the MCP wire contract and are pinned by
// results_test.go.

// jsonResult returns a successful JSON response.
//
// The parameter stays untyped on purpose: whatever is marshalled here is also
// handed to the SDK as StructuredContent, which is itself an `any`, so a type
// parameter would constrain callers without buying any safety.
func jsonResult(data any) (*sdkmcp.CallToolResult, error) {
	d, err := json.Marshal(data)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(d)}},
		StructuredContent: data,
	}, nil
}

// jsonResultPretty returns a successful JSON response with pretty formatting
func jsonResultPretty(data any) (*sdkmcp.CallToolResult, error) {
	d, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal: %v", err)), nil
	}
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(d)}},
		StructuredContent: data,
	}, nil
}

// resultForFormat renders value as pretty JSON for the "full" and "pretty"
// formats and as compact JSON for everything else, including the empty format.
// The comparison is case-sensitive: "FULL" is not "full".
func resultForFormat(value any, format string) *sdkmcp.CallToolResult {
	if format == "full" || format == "pretty" {
		result, _ := jsonResultPretty(value)
		return result
	}
	result, _ := jsonResult(value)
	return result
}

// textResult returns a simple text response
func textResult(msg string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: msg}}}
}

// errorResult returns an error response
func errorResult(msg string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// truncateLogResult auto-truncates log output to the last defaultLines lines
// when the caller hasn't applied any explicit limiting parameters.
// Appends a banner with total line count and usage hints for the AI agent.
func truncateLogResult(logs string, defaultLines int, callerLimited bool) *sdkmcp.CallToolResult {
	if callerLimited || logs == "" {
		return textResult(logs)
	}

	lines := strings.Split(logs, "\n")
	total := len(lines)

	// Trim trailing empty line from final newline
	if total > 0 && lines[total-1] == "" {
		total--
		lines = lines[:total]
	}

	if total <= defaultLines {
		return textResult(logs)
	}

	truncated := lines[total-defaultLines:]
	banner := fmt.Sprintf(
		"\n--- [showing last %d of %d lines] ---\nUse head/tail/offset/search/search_regex/section/file_pattern to refine.\nExample: tail=%d, or search=\"error\"",
		defaultLines, total, min(total, defaultLines*4),
	)
	return textResult(strings.Join(truncated, "\n") + banner)
}

// formatWorkflowStatusSummary renders the human-readable get_check_status
// summary. At most 20 check runs are listed; the remainder is reported as a
// count. Conclusion counts are emitted in sorted key order so the output is
// stable across map iterations.
func formatWorkflowStatusSummary(ref string, status *github.CombinedCheckStatus, filterMode string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Workflow Status for %s\n", ref)
	fmt.Fprintf(&sb, "Overall: %s\n", status.State)
	fmt.Fprintf(&sb, "Workflows: %d\n", status.TotalCount)
	fmt.Fprintf(&sb, "Filter Mode: %s\n", filterMode)

	if len(status.ByConclusion) > 0 {
		keys := make([]string, 0, len(status.ByConclusion))
		for k := range status.ByConclusion {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("By Conclusion:\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "  %s: %d\n", k, status.ByConclusion[k])
		}
	}

	if len(status.CheckRuns) == 0 {
		sb.WriteString("No matching workflow statuses found.\n")
		sb.WriteString("Tip: try filter=\"all\" or provide a different ref (branch name or commit SHA).\n")
		return sb.String()
	}

	sb.WriteString("Workflow Details:\n")
	maxItems := len(status.CheckRuns)
	if maxItems > 20 {
		maxItems = 20
	}
	for i := 0; i < maxItems; i++ {
		r := status.CheckRuns[i]
		conclusion := r.Conclusion
		if conclusion == "" {
			conclusion = "-"
		}
		fmt.Fprintf(&sb, "  - %s: %s/%s (id: %d)\n", r.Name, r.Status, conclusion, r.ID)
	}
	if len(status.CheckRuns) > maxItems {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(status.CheckRuns)-maxItems)
	}

	return sb.String()
}
