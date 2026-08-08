package mcp

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/denysvitali/gh-actions-mcp/github"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultText extracts the single text payload of a tool result.
func resultText(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", result.Content[0])
	return text.Text
}

func TestJSONResults(t *testing.T) {
	t.Parallel()

	type payload struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	data := payload{A: 1, B: "x"}

	tests := []struct {
		name     string
		build    func() (*sdkmcp.CallToolResult, error)
		wantText string
	}{
		{
			name:     "jsonResult marshals compactly",
			build:    func() (*sdkmcp.CallToolResult, error) { return jsonResult(data) },
			wantText: `{"a":1,"b":"x"}`,
		},
		{
			name:     "jsonResultPretty indents with two spaces",
			build:    func() (*sdkmcp.CallToolResult, error) { return jsonResultPretty(data) },
			wantText: "{\n  \"a\": 1,\n  \"b\": \"x\"\n}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.build()
			require.NoError(t, err)
			assert.Equal(t, tc.wantText, resultText(t, result))
			assert.False(t, result.IsError)
			assert.Equal(t, data, result.StructuredContent)
		})
	}
}

// TestJSONResultMarshalFailure pins that an unmarshalable payload degrades to an
// error result rather than returning a Go error.
func TestJSONResultMarshalFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		build func() (*sdkmcp.CallToolResult, error)
	}{
		{"jsonResult", func() (*sdkmcp.CallToolResult, error) { return jsonResult(math.Inf(1)) }},
		{"jsonResultPretty", func() (*sdkmcp.CallToolResult, error) { return jsonResultPretty(math.Inf(1)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.build()
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.Nil(t, result.StructuredContent)
			assert.Contains(t, resultText(t, result), "failed to marshal:")
		})
	}
}

func TestTextAndErrorResult(t *testing.T) {
	t.Parallel()

	text := textResult("hello")
	assert.Equal(t, "hello", resultText(t, text))
	assert.False(t, text.IsError)
	assert.Nil(t, text.StructuredContent)

	failure := errorResult("boom")
	assert.Equal(t, "boom", resultText(t, failure))
	assert.True(t, failure.IsError)
}

// TestResultForFormat pins which formats select pretty printing.
func TestResultForFormat(t *testing.T) {
	t.Parallel()

	type payload struct {
		A int `json:"a"`
	}

	tests := []struct {
		format   string
		wantText string
	}{
		{"full", "{\n  \"a\": 1\n}"},
		{"pretty", "{\n  \"a\": 1\n}"},
		{"compact", `{"a":1}`},
		{"", `{"a":1}`},
		{"minimal", `{"a":1}`},
		{"FULL", `{"a":1}`},
	}

	for _, tc := range tests {
		t.Run("format="+tc.format, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantText, resultText(t, resultForFormat(payload{A: 1}, tc.format)))
		})
	}
}

// TestTruncateLogResult pins the auto-truncation banner and the conditions under
// which truncation is skipped entirely.
func TestTruncateLogResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		logs          string
		defaultLines  int
		callerLimited bool
		want          string
	}{
		{
			name:          "caller limited returns logs verbatim",
			logs:          "a\nb\nc\nd",
			defaultLines:  2,
			callerLimited: true,
			want:          "a\nb\nc\nd",
		},
		{
			name:         "empty logs returned verbatim",
			logs:         "",
			defaultLines: 2,
			want:         "",
		},
		{
			name:         "under the limit returns logs verbatim",
			logs:         "a\nb",
			defaultLines: 5,
			want:         "a\nb",
		},
		{
			name:         "exactly at the limit returns logs verbatim",
			logs:         "a\nb\nc",
			defaultLines: 3,
			want:         "a\nb\nc",
		},
		{
			name:         "trailing newline is not counted as a line",
			logs:         "a\nb\nc\n",
			defaultLines: 3,
			want:         "a\nb\nc\n",
		},
		{
			name:         "over the limit keeps the tail and appends a banner",
			logs:         "a\nb\nc\nd\ne",
			defaultLines: 2,
			want: "d\ne" +
				"\n--- [showing last 2 of 5 lines] ---" +
				"\nUse head/tail/offset/search/search_regex/section/file_pattern to refine." +
				"\nExample: tail=5, or search=\"error\"",
		},
		{
			name:         "banner hint is capped at four times the default",
			logs:         strings.Repeat("x\n", 100),
			defaultLines: 2,
			want: "x\nx" +
				"\n--- [showing last 2 of 100 lines] ---" +
				"\nUse head/tail/offset/search/search_regex/section/file_pattern to refine." +
				"\nExample: tail=8, or search=\"error\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := truncateLogResult(tc.logs, tc.defaultLines, tc.callerLimited)
			assert.Equal(t, tc.want, resultText(t, result))
			assert.False(t, result.IsError)
		})
	}
}

// TestFormatWorkflowStatusSummary_Empty pins the no-results branch, including the
// hint line that replaces the detail listing.
func TestFormatWorkflowStatusSummary_Empty(t *testing.T) {
	t.Parallel()

	out := formatWorkflowStatusSummary("abc123", &github.CombinedCheckStatus{
		State:      "pending",
		TotalCount: 0,
	}, "all")

	assert.Equal(t, "Workflow Status for abc123\n"+
		"Overall: pending\n"+
		"Workflows: 0\n"+
		"Filter Mode: all\n"+
		"No matching workflow statuses found.\n"+
		"Tip: try filter=\"all\" or provide a different ref (branch name or commit SHA).\n", out)
}

// TestFormatWorkflowStatusSummary_SortsConclusions pins that the ByConclusion
// block is emitted in sorted key order regardless of map iteration order.
func TestFormatWorkflowStatusSummary_SortsConclusions(t *testing.T) {
	t.Parallel()

	out := formatWorkflowStatusSummary("main", &github.CombinedCheckStatus{
		State:        "failure",
		TotalCount:   3,
		ByConclusion: map[string]int{"success": 1, "failure": 2, "cancelled": 3},
		CheckRuns: []*github.CheckRun{
			{ID: 1, Name: "Build", Status: "completed", Conclusion: "failure"},
		},
	}, "latest")

	assert.Equal(t, "Workflow Status for main\n"+
		"Overall: failure\n"+
		"Workflows: 3\n"+
		"Filter Mode: latest\n"+
		"By Conclusion:\n"+
		"  cancelled: 3\n"+
		"  failure: 2\n"+
		"  success: 1\n"+
		"Workflow Details:\n"+
		"  - Build: completed/failure (id: 1)\n", out)
}

// TestFormatWorkflowStatusSummary_TruncatesAt20 pins the 20-check-run cap and the
// "... and N more" footer.
func TestFormatWorkflowStatusSummary_TruncatesAt20(t *testing.T) {
	t.Parallel()

	runs := make([]*github.CheckRun, 0, 25)
	for i := 1; i <= 25; i++ {
		runs = append(runs, &github.CheckRun{
			ID: int64(i), Name: fmt.Sprintf("check-%d", i), Status: "completed", Conclusion: "success",
		})
	}

	out := formatWorkflowStatusSummary("main", &github.CombinedCheckStatus{
		State: "success", TotalCount: 25, CheckRuns: runs,
	}, "latest")

	assert.Contains(t, out, "  - check-1: completed/success (id: 1)\n")
	assert.Contains(t, out, "  - check-20: completed/success (id: 20)\n")
	assert.NotContains(t, out, "check-21")
	assert.True(t, strings.HasSuffix(out, "  ... and 5 more\n"), "got tail %q", out[len(out)-40:])
	assert.Equal(t, 20, strings.Count(out, "completed/success"))
}

// TestFormatWorkflowStatusSummary_ExactlyAt20 pins that no footer is emitted when
// the count equals the cap.
func TestFormatWorkflowStatusSummary_ExactlyAt20(t *testing.T) {
	t.Parallel()

	runs := make([]*github.CheckRun, 0, 20)
	for i := 1; i <= 20; i++ {
		runs = append(runs, &github.CheckRun{ID: int64(i), Name: "c", Status: "completed", Conclusion: "success"})
	}

	out := formatWorkflowStatusSummary("main", &github.CombinedCheckStatus{
		State: "success", TotalCount: 20, CheckRuns: runs,
	}, "latest")

	assert.NotContains(t, out, "more")
	assert.Equal(t, 20, strings.Count(out, "completed/success"))
}

// TestGetLimitAndLogLines pins the config-or-default resolution.
func TestGetLimitAndLogLines(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultListLimit, newTestServer(t, nil).getLimit())
	assert.Equal(t, DefaultLogLines, newTestServer(t, nil).getLogLines())

	tuned := newTestServer(t, func(c *configOverrides) {
		c.defaultLimit = 3
		c.defaultLogLen = 7
	})
	assert.Equal(t, 3, tuned.getLimit())
	assert.Equal(t, 7, tuned.getLogLines())

	assert.Equal(t, 5, DefaultListLimit)
	assert.Equal(t, 50, DefaultLogLines)
}

func TestFormatWorkflowStatusSummary(t *testing.T) {
	status := &github.CombinedCheckStatus{
		State:      "failure",
		TotalCount: 3,
		ByConclusion: map[string]int{
			"failure":     2,
			"in_progress": 1,
		},
		CheckRuns: []*github.CheckRun{
			{ID: 10, Name: "Build", Status: "completed", Conclusion: "failure"},
			{ID: 11, Name: "Lint", Status: "in_progress", Conclusion: ""},
		},
	}

	out := formatWorkflowStatusSummary("main", status, "latest")
	assert.Contains(t, out, "Workflow Status for main")
	assert.Contains(t, out, "Overall: failure")
	assert.Contains(t, out, "Workflows: 3")
	assert.Contains(t, out, "Filter Mode: latest")
	assert.Contains(t, out, "By Conclusion:")
	assert.Contains(t, out, "failure: 2")
	assert.Contains(t, out, "in_progress: 1")
	assert.Contains(t, out, "- Build: completed/failure (id: 10)")
	assert.Contains(t, out, "- Lint: in_progress/- (id: 11)")
}
