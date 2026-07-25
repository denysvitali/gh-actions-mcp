package github

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// regexCache caches compiled regex patterns
var (
	regexCache      = make(map[string]*regexp.Regexp)
	regexCacheMutex sync.RWMutex
)

// LogFilterOptions contains parameters for filtering log output
type LogFilterOptions struct {
	Filter       string // Case-insensitive substring match
	FilterRegex  string // Regular expression pattern
	ContextLines int    // Lines of context around matches (like grep -C)
}

// logLine represents a line with metadata for filtering
type logLine struct {
	content     string
	isHeader    bool   // True for "=== filename ===" lines
	fileSection string // The current file section this line belongs to
}

// Pre-compiled regex for detecting file headers
var headerPattern = regexp.MustCompile(`^=== .+ ===$`)

// getCachedRegex returns a cached compiled regex or compiles and caches a new one
func getCachedRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMutex.RLock()
	re, ok := regexCache[pattern]
	regexCacheMutex.RUnlock()

	if ok {
		return re, nil
	}

	regexCacheMutex.Lock()
	defer regexCacheMutex.Unlock()

	// Double-check after acquiring write lock
	re, ok = regexCache[pattern]
	if ok {
		return re, nil
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexCache[pattern] = compiled
	return compiled, nil
}

// parseLogLines converts raw log string into structured logLine slice
func parseLogLines(logStr string) []logLine {
	rawLines := strings.Split(logStr, "\n")
	result := make([]logLine, 0, len(rawLines))

	currentFileSection := ""

	for _, raw := range rawLines {
		isHeader := headerPattern.MatchString(raw)
		if isHeader {
			currentFileSection = raw
		}

		result = append(result, logLine{
			content:     raw,
			isHeader:    isHeader,
			fileSection: currentFileSection,
		})
	}

	return result
}

// filterLogLines applies filter/regex matching with context to parsed log lines
func filterLogLines(lines []logLine, opts *LogFilterOptions) ([]logLine, error) {
	if opts == nil || (opts.Filter == "" && opts.FilterRegex == "") {
		return lines, nil
	}

	var matcher func(string) bool
	var matcherErr error

	if opts.FilterRegex != "" {
		re, err := getCachedRegex(opts.FilterRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern %q: %w", opts.FilterRegex, err)
		}
		matcher = func(s string) bool {
			return re.MatchString(s)
		}
		matcherErr = err
	} else {
		lowerFilter := strings.ToLower(opts.Filter)
		matcher = func(s string) bool {
			return strings.Contains(strings.ToLower(s), lowerFilter)
		}
	}

	// Check for matcher compilation errors
	if matcherErr != nil {
		return nil, matcherErr
	}

	// First pass: find all matching lines (excluding headers)
	matchedIndices := make(map[int]bool)
	for i, line := range lines {
		if !line.isHeader && matcher(line.content) {
			matchedIndices[i] = true
		}
	}

	if len(matchedIndices) == 0 {
		return nil, nil // No matches - return nil to indicate empty result
	}

	// Second pass: expand context, respecting file boundaries
	includedIndices := make(map[int]bool)

	for matchIdx := range matchedIndices {
		matchFileSection := lines[matchIdx].fileSection

		// Add context before (but not crossing file boundaries)
		for i := matchIdx - opts.ContextLines; i < matchIdx; i++ {
			if i >= 0 && !lines[i].isHeader && lines[i].fileSection == matchFileSection {
				includedIndices[i] = true
			}
		}

		// Add the match itself
		includedIndices[matchIdx] = true

		// Add context after (but not crossing file boundaries)
		for i := matchIdx + 1; i <= matchIdx+opts.ContextLines && i < len(lines); i++ {
			if lines[i].isHeader || lines[i].fileSection != matchFileSection {
				break // Stop at file boundary
			}
			includedIndices[i] = true
		}
	}

	// Third pass: build result with necessary headers
	var result []logLine
	var lastFileSection string

	for i, line := range lines {
		if includedIndices[i] {
			// If entering a new file section, include the header
			if line.fileSection != lastFileSection && line.fileSection != "" {
				// Find and include the header for this section
				for j := i; j >= 0; j-- {
					if lines[j].isHeader && lines[j].content == line.fileSection {
						result = append(result, lines[j])
						break
					}
				}
				lastFileSection = line.fileSection
			}
			result = append(result, line)
		}
	}

	return result, nil
}

// linesToString converts logLine slice back to string
func linesToString(lines []logLine) string {
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(line.content)
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
