package github

import (
	"context"
	"fmt"
	"strings"
)

// GetLogSection extracts a specific section from logs by header pattern
// Section headers typically look like "##[group]Section Name" or similar patterns
// If jobID is 0, it fetches logs for the run; otherwise for the specific job
func (c *Client) GetLogSection(ctx context.Context, runID, jobID int64, sectionPattern string, filterOpts *LogFilterOptions) (string, error) {
	var logs string
	var err error

	// Fetch logs based on whether we have a job ID
	if jobID > 0 {
		logs, err = c.GetWorkflowJobLogs(ctx, jobID, 0, 0, 0, false, nil)
	} else {
		logs, err = c.GetWorkflowLogs(ctx, runID, 0, 0, 0, false, nil)
	}

	if err != nil {
		return "", err
	}

	// Extract the section
	section, err := extractSection(logs, sectionPattern)
	if err != nil {
		return "", err
	}

	// Apply additional filtering if specified
	if filterOpts != nil && (filterOpts.Filter != "" || filterOpts.FilterRegex != "") {
		parsedLines := parseLogLines(section)
		filteredLines, err := filterLogLines(parsedLines, filterOpts)
		if err != nil {
			return "", err
		}
		if filteredLines == nil {
			return "", nil
		}
		section = linesToString(filteredLines)
	}

	return section, nil
}

// extractSection parses logs and extracts content between section markers
// GitHub Actions logs use ##[group]Section Name and ##[endgroup] markers
func extractSection(logs string, sectionPattern string) (string, error) {
	if sectionPattern == "" {
		return logs, nil
	}

	lines := strings.Split(logs, "\n")
	var result []string
	inSection := false
	sectionDepth := 0

	// Compile regex for matching section headers
	re, err := getCachedRegex(sectionPattern)
	if err != nil {
		return "", fmt.Errorf("invalid section pattern %q: %w", sectionPattern, err)
	}

	for _, line := range lines {
		// Check for group start (various formats)
		// GitHub Actions uses: ##[group]Section Name
		// Also handle: ::group::Section Name
		isGroupStart := strings.Contains(line, "##[group]") || strings.Contains(line, "::group::")
		isGroupEnd := strings.Contains(line, "##[endgroup]") || strings.Contains(line, "::endgroup::")

		if isGroupStart {
			sectionDepth++
			// Check if this is the section we're looking for
			if !inSection && re.MatchString(line) {
				inSection = true
				result = append(result, line)
			}
		} else if isGroupEnd {
			if inSection {
				result = append(result, line)
				sectionDepth--
				if sectionDepth == 0 {
					inSection = false
				}
			} else {
				sectionDepth--
				if sectionDepth < 0 {
					sectionDepth = 0
				}
			}
		} else if inSection {
			result = append(result, line)
		}
	}

	if len(result) == 0 {
		return "", fmt.Errorf("section matching pattern %q not found", sectionPattern)
	}

	return strings.Join(result, "\n"), nil
}

// LogSection represents a section found in workflow logs
type LogSection struct {
	Name    string `json:"name"`
	Line    int    `json:"line"`
	JobName string `json:"job_name,omitempty"`
}

// ListLogSections extracts all section headers from workflow logs
// Returns a list of sections with their names and line numbers
func (c *Client) ListLogSections(ctx context.Context, runID, jobID int64) ([]*LogSection, error) {
	var logs string
	var err error

	// Fetch logs based on whether we have a job ID
	if jobID > 0 {
		logs, err = c.GetWorkflowJobLogs(ctx, jobID, 0, 0, 0, false, nil)
	} else {
		logs, err = c.GetWorkflowLogs(ctx, runID, 0, 0, 0, false, nil)
	}

	if err != nil {
		return nil, err
	}

	return extractSections(logs), nil
}

// extractSections parses logs and returns all section headers found
// GitHub Actions logs use ##[group]Section Name and ::group::Section Name markers
func extractSections(logs string) []*LogSection {
	lines := strings.Split(logs, "\n")
	var sections []*LogSection
	currentJob := ""

	for i, line := range lines {
		// Check for job header (=== filename ===)
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			currentJob = strings.TrimPrefix(strings.TrimSuffix(line, " ==="), "=== ")
			continue
		}

		// Check for group start markers
		var sectionName string
		if strings.Contains(line, "##[group]") {
			sectionName = extractSectionName(line, "##[group]")
		} else if strings.Contains(line, "::group::") {
			sectionName = extractSectionName(line, "::group::")
		}

		if sectionName != "" {
			sections = append(sections, &LogSection{
				Name:    sectionName,
				Line:    i + 1, // 1-based line number
				JobName: currentJob,
			})
		}
	}

	return sections
}

// extractSectionName extracts the section name after a marker
func extractSectionName(line, marker string) string {
	idx := strings.Index(line, marker)
	if idx == -1 {
		return ""
	}
	name := line[idx+len(marker):]
	return strings.TrimSpace(name)
}
