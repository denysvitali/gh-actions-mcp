package github

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractSection(t *testing.T) {
	tests := []struct {
		name            string
		logs            string
		sectionPattern  string
		wantErr         bool
		errContains     string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name: "extract single section",
			logs: `##[group]Build
Building project...
Build complete
##[endgroup]
##[group]Test
Running tests...
Tests passed
##[endgroup]`,
			sectionPattern: "Build",
			wantContains:   []string{"Building project...", "Build complete", "##[group]Build"},
		},
		{
			name: "extract section with regex pattern",
			logs: `##[group]Build project
Building...
##[endgroup]
##[group]Test project
Testing...
##[endgroup]`,
			sectionPattern:  "^.*Build.*$",
			wantContains:    []string{"Building...", "##[group]Build project"},
			wantNotContains: []string{"Test project", "Testing..."},
		},
		{
			name: "section not found",
			logs: `##[group]Build
Building...
##[endgroup]`,
			sectionPattern: "Deploy",
			wantErr:        true,
			errContains:    "section matching pattern",
		},
		{
			name:           "empty section pattern returns all logs",
			logs:           "Some log content\nMore content",
			sectionPattern: "",
			wantContains:   []string{"Some log content", "More content"},
		},
		{
			name: "nested groups",
			logs: `##[group]Outer
outer content
##[group]Inner
inner content
##[endgroup]
more outer
##[endgroup]`,
			sectionPattern: "Outer",
			wantContains:   []string{"outer content", "inner content", "more outer"},
		},
		{
			name: "alternative group syntax",
			logs: `::group::Build
Building...
::endgroup::
::group::Test
Testing...
::endgroup::`,
			sectionPattern:  "Build",
			wantContains:    []string{"Building...", "::group::Build"},
			wantNotContains: []string{"Test", "Testing..."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractSection(tt.logs, tt.sectionPattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractSection() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if err == nil || !containsStr(err.Error(), tt.errContains) {
					t.Errorf("extractSection() error = %v, should contain %v", err, tt.errContains)
				}
				return
			}
			for _, want := range tt.wantContains {
				if !containsStr(got, want) {
					t.Errorf("extractSection() result should contain %q, got:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if containsStr(got, notWant) {
					t.Errorf("extractSection() result should NOT contain %q, got:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestExtractSectionInvalidRegex(t *testing.T) {
	_, err := extractSection("some logs", "[invalid")
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func containsStr(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && (s == substr || containsAtStr(s, substr, 0))
}

func containsAtStr(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestExtractSectionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		line   string
		marker string
		want   string
	}{
		{name: "github group marker", line: "##[group]Run tests", marker: "##[group]", want: "Run tests"},
		{name: "workflow command marker", line: "::group::Run tests", marker: "::group::", want: "Run tests"},
		{name: "surrounding whitespace is trimmed", line: "##[group]  Run tests  ", marker: "##[group]", want: "Run tests"},
		{name: "timestamp prefix is kept out of the name", line: "2024-01-01T00:00:00Z ##[group]Build", marker: "##[group]", want: "Build"},
		{name: "marker absent yields empty", line: "no marker here", marker: "##[group]", want: ""},
		{name: "empty name yields empty", line: "##[group]", marker: "##[group]", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, extractSectionName(tt.line, tt.marker))
		})
	}
}

func TestExtractSections(t *testing.T) {
	t.Parallel()

	logs := strings.Join([]string{
		"=== build/1_step.txt ===",
		"##[group]Set up job",
		"setting up",
		"##[endgroup]",
		"::group::Run tests",
		"testing",
		"::endgroup::",
		"=== lint/1_step.txt ===",
		"##[group]Lint",
		"##[group]", // empty name is not reported as a section
		"##[endgroup]",
	}, "\n")

	sections := extractSections(logs)
	require.Len(t, sections, 3)

	assert.Equal(t, "Set up job", sections[0].Name)
	assert.Equal(t, 2, sections[0].Line, "line numbers are 1-based over the whole log string")
	assert.Equal(t, "build/1_step.txt", sections[0].JobName)

	assert.Equal(t, "Run tests", sections[1].Name)
	assert.Equal(t, "build/1_step.txt", sections[1].JobName)

	assert.Equal(t, "Lint", sections[2].Name)
	assert.Equal(t, "lint/1_step.txt", sections[2].JobName, "the job name follows the most recent === header ===")
}

func TestExtractSections_NoSections(t *testing.T) {
	t.Parallel()

	assert.Nil(t, extractSections(""))
	assert.Nil(t, extractSections("just\nsome\nlines"))
}

func TestExtractSection_NestedGroups(t *testing.T) {
	t.Parallel()

	// The matched group closes only when the nesting depth returns to zero, so an
	// inner group's body is captured along with its parent. Note that a nested
	// ##[group] *marker* line is dropped from the output while its matching
	// ##[endgroup] is kept — asymmetric, but that is today's behaviour.
	logs := strings.Join([]string{
		"##[group]Outer",
		"outer-line",
		"##[group]Inner",
		"inner-line",
		"##[endgroup]",
		"still-outer",
		"##[endgroup]",
		"after",
	}, "\n")

	got, err := extractSection(logs, "Outer")
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		"##[group]Outer",
		"outer-line",
		"inner-line",
		"##[endgroup]",
		"still-outer",
		"##[endgroup]",
	}, "\n"), got)
	assert.NotContains(t, got, "after")
}

func TestExtractSection_UnbalancedEndgroupDoesNotUnderflow(t *testing.T) {
	t.Parallel()

	// A stray ##[endgroup] before any group must not drive the depth negative and
	// swallow the following section.
	logs := strings.Join([]string{
		"##[endgroup]",
		"##[group]Build",
		"building",
		"##[endgroup]",
	}, "\n")

	got, err := extractSection(logs, "Build")
	require.NoError(t, err)
	assert.Equal(t, "##[group]Build\nbuilding\n##[endgroup]", got)
}

func TestGetLogSection(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"build/1_step.txt": strings.Join([]string{
			"##[group]Run tests",
			"PASS",
			"FAIL somewhere",
			"##[endgroup]",
			"##[group]Other",
			"noise",
			"##[endgroup]",
		}, "\n"),
	}

	t.Run("extracts the matching section from the run archive", func(t *testing.T) {
		t.Parallel()

		got, err := newRunLogArchiveClient(t, 1, files).GetLogSection(context.Background(), 1, 0, "Run tests", nil)
		require.NoError(t, err)
		assert.Contains(t, got, "PASS")
		assert.NotContains(t, got, "noise")
	})

	t.Run("filter options are applied after extraction", func(t *testing.T) {
		t.Parallel()

		got, err := newRunLogArchiveClient(t, 1, files).GetLogSection(context.Background(), 1, 0, "Run tests", &LogFilterOptions{Filter: "FAIL"})
		require.NoError(t, err)
		assert.Equal(t, "FAIL somewhere", got)
	})

	t.Run("a filter matching nothing yields an empty string", func(t *testing.T) {
		t.Parallel()

		got, err := newRunLogArchiveClient(t, 1, files).GetLogSection(context.Background(), 1, 0, "Run tests", &LogFilterOptions{Filter: "no-such-line"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a missing section is an error", func(t *testing.T) {
		t.Parallel()

		_, err := newRunLogArchiveClient(t, 1, files).GetLogSection(context.Background(), 1, 0, "Deploy", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "section matching pattern")
	})

	t.Run("a log fetch failure is propagated", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		_, err := client.GetLogSection(context.Background(), 1, 0, "Run tests", nil)
		require.Error(t, err)
	})
}

func TestListLogSections(t *testing.T) {
	t.Parallel()

	t.Run("lists sections found in the run archive", func(t *testing.T) {
		t.Parallel()

		client := newRunLogArchiveClient(t, 1, map[string]string{
			"build/1_step.txt": "##[group]Set up job\nx\n##[endgroup]",
		})

		sections, err := client.ListLogSections(context.Background(), 1, 0)
		require.NoError(t, err)
		require.Len(t, sections, 1)
		assert.Equal(t, "Set up job", sections[0].Name)
		assert.Equal(t, "build/1_step.txt", sections[0].JobName)
	})

	t.Run("a log fetch failure is propagated", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("/repos/owner/repo/actions/runs/1/logs", statusHandler(http.StatusNotFound))
		client := newMuxClient(t, mux)

		_, err := client.ListLogSections(context.Background(), 1, 0)
		require.Error(t, err)
	})
}
