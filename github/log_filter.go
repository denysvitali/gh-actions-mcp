package github

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// maxCachedRegexes bounds the compiled-pattern cache. Patterns come from
// user-supplied tool arguments, so an unbounded map would let a caller pin
// memory forever. When the cache fills it is dropped wholesale rather than
// evicted one entry at a time: log filtering is bursty and repeated within a
// single request, so a cheap "clear when full" policy keeps the common case
// (one or two patterns per request) a hit without any LRU bookkeeping.
const maxCachedRegexes = 256

// regexCacheMutex guards regexCache and nothing else. Critical sections contain
// only a map read, a map write, and (on a miss) a regexp.Compile — all bounded
// and non-blocking, so no lock ordering is needed: no other lock in this package
// is ever acquired while it is held.
var (
	regexCacheMutex sync.RWMutex //nolint:forbidigo // Guards regexCache only; it is a leaf lock with no lock ordering.
	regexCache      = make(map[string]*regexp.Regexp)
)

// LogFilterOptions selects which log lines survive filtering. Filter and
// FilterRegex are mutually exclusive in effect: when both are set, FilterRegex
// wins. A zero value means "no filtering".
type LogFilterOptions struct {
	Filter       string // Case-insensitive substring match
	FilterRegex  string // Regular expression pattern
	ContextLines int    // Lines of context around matches (like grep -C)
}

// isActive reports whether these options would filter anything. A nil receiver
// is inactive, so callers need not nil-check first.
func (o *LogFilterOptions) isActive() bool {
	return o != nil && (o.Filter != "" || o.FilterRegex != "")
}

// logLine is a log line tagged with the "=== file ===" section it belongs to, so
// that context expansion can stop at file boundaries.
type logLine struct {
	content     string
	isHeader    bool   // True for "=== filename ===" lines
	fileSection string // The current file section this line belongs to
}

// headerPattern matches the "=== name ===" separators that formatLogFiles emits.
// The name must be non-empty.
var headerPattern = regexp.MustCompile(`^=== .+ ===$`)

// getCachedRegex compiles pattern, memoising the result. Repeated calls with the
// same pattern return the identical *regexp.Regexp until the cache is dropped
// (see maxCachedRegexes). Compile errors are returned and never cached.
func getCachedRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMutex.RLock()
	re, ok := regexCache[pattern]
	regexCacheMutex.RUnlock()
	if ok {
		return re, nil
	}

	regexCacheMutex.Lock()
	defer regexCacheMutex.Unlock()

	// Another goroutine may have compiled it while the write lock was contended.
	if re, ok = regexCache[pattern]; ok {
		return re, nil
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if len(regexCache) >= maxCachedRegexes {
		regexCache = make(map[string]*regexp.Regexp, maxCachedRegexes)
	}
	regexCache[pattern] = compiled
	return compiled, nil
}

// parseLogLines splits logStr on newlines and tags each line with the most
// recent "=== file ===" header seen. Lines before the first header have an empty
// section; a header belongs to its own section.
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

// filterLogLines keeps the lines matching opts plus opts.ContextLines lines of
// surrounding context, never crossing a file-section boundary, and re-inserts the
// "=== file ===" header of every section that contributes a line.
//
// It returns lines unchanged when opts is inactive, and nil (not an empty slice)
// when the filter matched nothing — callers distinguish "no filter" from "no
// matches" on that nil.
func filterLogLines(lines []logLine, opts *LogFilterOptions) ([]logLine, error) {
	if !opts.isActive() {
		return lines, nil
	}

	matcher, err := logLineMatcher(opts)
	if err != nil {
		return nil, err
	}

	matched := matchingLogLines(lines, matcher)
	if len(matched) == 0 {
		return nil, nil
	}

	return withSectionHeaders(lines, expandLogContext(lines, matched, opts.ContextLines)), nil
}

// logLineMatcher builds the per-line predicate for opts. FilterRegex takes
// precedence over Filter; the substring match is case-insensitive.
func logLineMatcher(opts *LogFilterOptions) (func(string) bool, error) {
	if opts.FilterRegex != "" {
		re, err := getCachedRegex(opts.FilterRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern %q: %w", opts.FilterRegex, err)
		}
		return re.MatchString, nil
	}

	lowerFilter := strings.ToLower(opts.Filter)
	return func(s string) bool {
		return strings.Contains(strings.ToLower(s), lowerFilter)
	}, nil
}

// matchingLogLines returns the indices of the lines matcher accepts. Section
// headers are never candidates, so a filter cannot match on a file name.
func matchingLogLines(lines []logLine, matcher func(string) bool) map[int]bool {
	matched := make(map[int]bool)
	for i, line := range lines {
		if !line.isHeader && matcher(line.content) {
			matched[i] = true
		}
	}
	return matched
}

// expandLogContext widens each matched index by contextLines in both directions,
// stopping at section headers and at section changes so context never leaks
// between files.
func expandLogContext(lines []logLine, matched map[int]bool, contextLines int) map[int]bool {
	included := make(map[int]bool, len(matched))
	for matchIdx := range matched {
		section := lines[matchIdx].fileSection
		included[matchIdx] = true

		for i := matchIdx - contextLines; i < matchIdx; i++ {
			if i >= 0 && !lines[i].isHeader && lines[i].fileSection == section {
				included[i] = true
			}
		}
		for i := matchIdx + 1; i <= matchIdx+contextLines && i < len(lines); i++ {
			if lines[i].isHeader || lines[i].fileSection != section {
				break
			}
			included[i] = true
		}
	}
	return included
}

// withSectionHeaders emits the included lines in their original order, prefixing
// each run with its section's "=== file ===" header so the output stays
// attributable to a file.
func withSectionHeaders(lines []logLine, included map[int]bool) []logLine {
	var (
		result          []logLine
		lastFileSection string
	)
	for i, line := range lines {
		if !included[i] {
			continue
		}
		if line.fileSection != lastFileSection && line.fileSection != "" {
			result = appendSectionHeader(result, lines, i, line.fileSection)
			lastFileSection = line.fileSection
		}
		result = append(result, line)
	}
	return result
}

// appendSectionHeader scans backwards from index for the header line that opens
// section and appends it to result. A missing header is not an error: some log
// payloads have no headers at all.
func appendSectionHeader(result, lines []logLine, index int, section string) []logLine {
	for j := index; j >= 0; j-- {
		if lines[j].isHeader && lines[j].content == section {
			return append(result, lines[j])
		}
	}
	return result
}

// linesToString joins the lines with newlines, without a trailing newline.
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
