package github

import (
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for log filtering functionality

func TestParseLogLines(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectedCount   int
		expectedHeaders int
	}{
		{
			name:            "Single file with content",
			input:           "=== job/step.txt ===\nline1\nline2",
			expectedCount:   3,
			expectedHeaders: 1,
		},
		{
			name:            "Multiple files",
			input:           "=== file1.txt ===\ncontent1\n=== file2.txt ===\ncontent2",
			expectedCount:   4,
			expectedHeaders: 2,
		},
		{
			name:            "Empty input",
			input:           "",
			expectedCount:   1, // splits to [""]
			expectedHeaders: 0,
		},
		{
			name:            "Only content no headers",
			input:           "line1\nline2\nline3",
			expectedCount:   3,
			expectedHeaders: 0,
		},
		{
			name:            "Header pattern must be exact",
			input:           "=== not closed\n=== valid.txt ===\ncontent",
			expectedCount:   3,
			expectedHeaders: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLogLines(tt.input)
			assert.Equal(t, tt.expectedCount, len(result))

			headerCount := 0
			for _, line := range result {
				if line.isHeader {
					headerCount++
				}
			}
			assert.Equal(t, tt.expectedHeaders, headerCount)
		})
	}
}

func TestFilterLogLines(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		filter        string
		filterRegex   string
		context       int
		expectMatch   bool
		expectError   bool
		expectedLines int
	}{
		{
			name:          "Simple substring match",
			input:         "=== test.txt ===\nline1\nERROR here\nline3",
			filter:        "error",
			context:       0,
			expectMatch:   true,
			expectedLines: 2, // header + match
		},
		{
			name:          "Case insensitive match",
			input:         "=== test.txt ===\nERROR\nerror\nError",
			filter:        "ERROR",
			context:       0,
			expectMatch:   true,
			expectedLines: 4, // header + 3 matches
		},
		{
			name:          "Regex match",
			input:         "=== test.txt ===\nerror: code 123\nwarning: code 456",
			filterRegex:   "code \\d+",
			context:       0,
			expectMatch:   true,
			expectedLines: 3, // header + 2 matches
		},
		{
			name:        "Invalid regex",
			input:       "=== test.txt ===\nline",
			filterRegex: "[invalid",
			expectError: true,
		},
		{
			name:        "No matches",
			input:       "=== test.txt ===\nline1\nline2",
			filter:      "notfound",
			expectMatch: false,
		},
		{
			name:          "Context lines",
			input:         "=== test.txt ===\nline1\nline2\nERROR\nline4\nline5",
			filter:        "error",
			context:       1,
			expectMatch:   true,
			expectedLines: 4, // header + line2 + ERROR + line4
		},
		{
			name:          "Context stops at file boundary",
			input:         "=== file1.txt ===\nline1\nERROR\n=== file2.txt ===\nline2",
			filter:        "error",
			context:       2,
			expectMatch:   true,
			expectedLines: 3, // header + line1 + ERROR (stops at boundary)
		},
		{
			name:        "Header not matched",
			input:       "=== ERROR.txt ===\nline1",
			filter:      "error",
			expectMatch: false, // "error" is only in header, not in content
		},
		{
			name:          "Multiple matches with overlapping context",
			input:         "=== test.txt ===\nline1\nERROR1\nline3\nERROR2\nline5",
			filter:        "error",
			context:       1,
			expectMatch:   true,
			expectedLines: 6, // header + all content (context overlaps)
		},
		{
			name:          "Matches across multiple files",
			input:         "=== file1.txt ===\nok\nERROR1\n=== file2.txt ===\nok\nERROR2",
			filter:        "error",
			context:       0,
			expectMatch:   true,
			expectedLines: 4, // 2 headers + 2 matches
		},
		{
			name:          "Empty filter returns all lines",
			input:         "=== test.txt ===\nline1\nline2",
			filter:        "",
			context:       0,
			expectMatch:   true,
			expectedLines: 3,
		},
		{
			name:          "Nil options returns all lines",
			input:         "=== test.txt ===\nline1\nline2",
			filter:        "",
			filterRegex:   "",
			context:       0,
			expectMatch:   true,
			expectedLines: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := parseLogLines(tt.input)

			var opts *LogFilterOptions
			if tt.filter != "" || tt.filterRegex != "" {
				opts = &LogFilterOptions{
					Filter:       tt.filter,
					FilterRegex:  tt.filterRegex,
					ContextLines: tt.context,
				}
			}

			result, err := filterLogLines(lines, opts)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			if !tt.expectMatch {
				assert.Nil(t, result)
				return
			}

			assert.NotNil(t, result)
			assert.Equal(t, tt.expectedLines, len(result))
		})
	}
}

func TestLinesToString(t *testing.T) {
	tests := []struct {
		name     string
		lines    []logLine
		expected string
	}{
		{
			name:     "Empty slice",
			lines:    []logLine{},
			expected: "",
		},
		{
			name: "Single line",
			lines: []logLine{
				{content: "hello"},
			},
			expected: "hello",
		},
		{
			name: "Multiple lines",
			lines: []logLine{
				{content: "line1"},
				{content: "line2"},
				{content: "line3"},
			},
			expected: "line1\nline2\nline3",
		},
		{
			name: "With header",
			lines: []logLine{
				{content: "=== test.txt ===", isHeader: true},
				{content: "content"},
			},
			expected: "=== test.txt ===\ncontent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := linesToString(tt.lines)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterIntegration(t *testing.T) {
	// Test the full flow: parse -> filter -> convert back to string
	input := `=== job1/step1.txt ===
2024-01-01 10:00:00 Starting build...
2024-01-01 10:00:01 ERROR: compilation failed
2024-01-01 10:00:02 Build failed
=== job1/step2.txt ===
2024-01-01 10:00:03 Running tests...
2024-01-01 10:00:04 All tests passed`

	tests := []struct {
		name     string
		filter   string
		regex    string
		context  int
		contains []string
		excludes []string
	}{
		{
			name:     "Filter for ERROR",
			filter:   "error",
			contains: []string{"ERROR: compilation failed", "=== job1/step1.txt ==="},
			excludes: []string{"Starting build", "Running tests", "=== job1/step2.txt ==="},
		},
		{
			name:     "Filter for ERROR with context",
			filter:   "error",
			context:  1,
			contains: []string{"Starting build", "ERROR: compilation failed", "Build failed"},
			excludes: []string{"Running tests"},
		},
		{
			name:     "Regex for timestamps",
			regex:    "\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2}",
			contains: []string{"Starting build", "ERROR", "Running tests"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := parseLogLines(input)
			opts := &LogFilterOptions{
				Filter:       tt.filter,
				FilterRegex:  tt.regex,
				ContextLines: tt.context,
			}

			filtered, err := filterLogLines(lines, opts)
			assert.NoError(t, err)
			assert.NotNil(t, filtered)

			result := linesToString(filtered)

			for _, s := range tt.contains {
				assert.Contains(t, result, s, "should contain: %s", s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, result, s, "should not contain: %s", s)
			}
		})
	}
}

func TestGetCachedRegex(t *testing.T) {
	t.Parallel()

	// A pattern is compiled once and the identical *Regexp is returned again.
	first, err := getCachedRegex(`^cached-(\d+)$`)
	require.NoError(t, err)
	second, err := getCachedRegex(`^cached-(\d+)$`)
	require.NoError(t, err)
	assert.Same(t, first, second, "second call must return the cached instance")

	// An invalid pattern reports the compile error and is not cached.
	_, err = getCachedRegex(`[unterminated`)
	require.Error(t, err)
	_, err = getCachedRegex(`[unterminated`)
	require.Error(t, err)
}

func TestGetCachedRegex_Concurrent(t *testing.T) {
	t.Parallel()

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]*regexp.Regexp, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			re, err := getCachedRegex(`^concurrent-[a-z]+$`)
			assert.NoError(t, err)
			results[i] = re
		}(i)
	}
	wg.Wait()

	for i := 1; i < goroutines; i++ {
		assert.Same(t, results[0], results[i], "all goroutines must observe one compiled regex")
	}
}

func TestFilterLogLines_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		opts     *LogFilterOptions
		wantNil  bool
		wantStr  string
		wantErr  string
		wantSame bool // returns the input slice untouched
	}{
		{
			name:     "nil options returns input unchanged",
			input:    "a\nb",
			opts:     nil,
			wantSame: true,
		},
		{
			name:     "empty filter and regex returns input unchanged",
			input:    "a\nb",
			opts:     &LogFilterOptions{ContextLines: 5},
			wantSame: true,
		},
		{
			name:    "no match returns nil, not an empty slice",
			input:   "alpha\nbeta",
			opts:    &LogFilterOptions{Filter: "gamma"},
			wantNil: true,
		},
		{
			name:    "invalid regex is reported",
			input:   "alpha",
			opts:    &LogFilterOptions{FilterRegex: "[unterminated"},
			wantErr: "invalid regex pattern",
		},
		{
			name:    "regex takes precedence over substring filter",
			input:   "alpha\nbeta",
			opts:    &LogFilterOptions{Filter: "alpha", FilterRegex: "^beta$"},
			wantStr: "beta",
		},
		{
			name:    "substring filter is case-insensitive",
			input:   "ALPHA\nbeta",
			opts:    &LogFilterOptions{Filter: "alpha"},
			wantStr: "ALPHA",
		},
		{
			name:    "context does not cross a file header boundary",
			input:   "=== a.txt ===\nhit\n=== b.txt ===\nmiss",
			opts:    &LogFilterOptions{Filter: "hit", ContextLines: 3},
			wantStr: "=== a.txt ===\nhit",
		},
		{
			name:    "header lines are never matched by the filter",
			input:   "=== hit.txt ===\nbody",
			opts:    &LogFilterOptions{Filter: "hit"},
			wantNil: true,
		},
		{
			name:    "negative context behaves like zero",
			input:   "before\nhit\nafter",
			opts:    &LogFilterOptions{Filter: "hit", ContextLines: -2},
			wantStr: "hit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := parseLogLines(tt.input)
			got, err := filterLogLines(lines, tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantSame {
				assert.Equal(t, lines, got)
				return
			}
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.wantStr, linesToString(got))
		})
	}
}

func TestParseLogLines_FileSectionTracking(t *testing.T) {
	t.Parallel()

	lines := parseLogLines("preamble\n=== a.txt ===\nbody\n=== b.txt ===\nmore")
	require.Len(t, lines, 5)

	assert.Empty(t, lines[0].fileSection, "lines before the first header have no section")
	assert.False(t, lines[0].isHeader)
	assert.True(t, lines[1].isHeader)
	assert.Equal(t, "=== a.txt ===", lines[1].fileSection, "a header belongs to its own section")
	assert.Equal(t, "=== a.txt ===", lines[2].fileSection)
	assert.Equal(t, "=== b.txt ===", lines[4].fileSection)
}

func TestHeaderPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		line string
		want bool
	}{
		{line: "=== a.txt ===", want: true},
		{line: "=== deep/path/1_Step.txt ===", want: true},
		{line: "===  ===", want: false}, // the name must be non-empty
		{line: "=== x ===", want: true},
		{line: "======", want: false},
		{line: "=== a.txt === trailing", want: false},
		{line: "leading === a.txt ===", want: false},
		{line: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, headerPattern.MatchString(tt.line))
		})
	}
}
