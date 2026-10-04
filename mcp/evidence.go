package mcp

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEvidenceBytes = 16 * 1024
	maximumEvidenceBytes = 256 * 1024
	defaultEvidenceLines = 100
	maximumEvidenceLines = 2000
)

func (b toolBuilder) evidenceArguments() toolOption {
	return combine(
		b.WithNumber("max_bytes", b.Description("Maximum content bytes per response after filtering (default 16384; metadata excluded)"), b.DefaultNumber(defaultEvidenceBytes), b.Minimum(256), b.Maximum(maximumEvidenceBytes)),
		b.WithNumber("max_lines", b.Description("Maximum content lines per response after filtering (default 100)"), b.DefaultNumber(defaultEvidenceLines), b.Minimum(1), b.Maximum(maximumEvidenceLines)),
		b.WithString("cursor", b.Description("Opaque evidence continuation cursor; repeat the same selection arguments")),
	)
}

func evidenceBudgets(input evidenceInput) (int, int, error) {
	bytes, lines := input.MaxBytes, input.MaxLines
	if bytes == 0 {
		bytes = defaultEvidenceBytes
	}
	if lines == 0 {
		lines = defaultEvidenceLines
	}
	if bytes < 256 || bytes > maximumEvidenceBytes || lines < 1 || lines > maximumEvidenceLines {
		return 0, 0, fmt.Errorf("max_bytes must be 256..%d and max_lines must be 1..%d", maximumEvidenceBytes, maximumEvidenceLines)
	}
	return bytes, lines, nil
}

// evidenceChunk never splits a UTF-8 rune. Its line cap includes a partial final
// line, so a single very long line cannot defeat the byte budget.
func evidenceChunk(content string, maxBytes, maxLines int) string {
	end := min(len(content), maxBytes)
	for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	lines := 0
	for i := 0; i < end; i++ {
		if content[i] == '\n' {
			lines++
			if lines == maxLines {
				end = i + 1
				break
			}
		}
	}
	return content[:end]
}

func evidenceLineCount(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

type logEvidence struct {
	Text          string `json:"text"`
	RunID         int64  `json:"run_id"`
	JobID         *int64 `json:"job_id,omitempty"`
	ByteOffset    int    `json:"byte_offset"`
	ReturnedBytes int    `json:"returned_bytes"`
	ReturnedLines int    `json:"returned_lines"`
	TotalBytes    int    `json:"total_bytes"`
	Truncated     bool   `json:"truncated"`
	NextCursor    string `json:"next_cursor,omitempty"`
}

func (s *MCPServer) boundedLogResult(logs string, req runElementRequest) (*sdkmcp.CallToolResult, error) {
	bytes, lines, err := evidenceBudgets(req.input.evidenceInput)
	if err != nil {
		return nil, err
	}
	selection := req.input
	selection.evidenceInput = evidenceInput{}
	// Content identity prevents silently skipping or duplicating data when a live
	// log changes between calls. The caller can explicitly restart observation.
	scope := struct {
		Kind, Owner, Repo, APIBaseURL, AuthUsername string
		Selection                                   getRunInput
		Digest                                      [32]byte
	}{"logs", req.owner, req.repo, s.config.APIBaseURL, s.config.AuthUsername, selection, sha256.Sum256([]byte(logs))}
	position, err := decodeCursor(req.input.Cursor, scope, s.cursorKey())
	if err != nil {
		return nil, err
	}
	start := position.Offset
	if position.Page != 1 || start > len(logs) {
		return nil, fmt.Errorf("invalid evidence cursor offset")
	}
	chunk := evidenceChunk(logs[start:], bytes, lines)
	end := start + len(chunk)
	result := logEvidence{Text: chunk, RunID: req.input.RunID, JobID: req.input.JobID, ByteOffset: start, ReturnedBytes: len(chunk), ReturnedLines: evidenceLineCount(chunk), TotalBytes: len(logs), Truncated: end < len(logs)}
	if result.Truncated {
		result.NextCursor, err = encodeCursor(cursorPosition{Page: 1, Offset: end}, scope, s.cursorKey())
		if err != nil {
			return nil, err
		}
	}
	text := chunk
	if result.Truncated {
		text += "\n--- [output bounded; use next_cursor with the same selection to continue] ---"
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}, StructuredContent: result}, nil
}
