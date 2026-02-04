package github

import (
	"testing"
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
			sectionPattern: "^.*Build.*$",
			wantContains:   []string{"Building...", "##[group]Build project"},
			wantNotContains: []string{"Test project", "Testing..."},
		},
		{
			name: "section not found",
			logs: `##[group]Build
Building...
##[endgroup]`,
			sectionPattern:  "Deploy",
			wantErr:         true,
			errContains:     "section matching pattern",
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
			sectionPattern: "Build",
			wantContains:   []string{"Building...", "::group::Build"},
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
