package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestEvidenceChunkBudgets(t *testing.T) {
	require.Equal(t, "one\ntwo\n", evidenceChunk("one\ntwo\nthree", 100, 2))
	require.Equal(t, "a", evidenceChunk("a世z", 3, 100))
	require.True(t, utf8.ValidString(evidenceChunk(strings.Repeat("世", 100), 256, 100)))
	require.Len(t, evidenceChunk(strings.Repeat("x", 10000), 256, 100), 256)
}

func TestFilteredLogEvidenceRemainsBoundedAndResumes(t *testing.T) {
	server := newTestServer(t, nil)
	req := runElementRequest{owner: "o", repo: "r", input: getRunInput{RunID: 42, Search: "test", evidenceInput: evidenceInput{MaxBytes: 256, MaxLines: 2}}}
	logs := strings.Repeat("test output\n", 7)
	var reconstructed string
	for {
		result, err := server.boundedLogResult(logs, req)
		require.NoError(t, err)
		evidence := result.StructuredContent.(logEvidence)
		require.LessOrEqual(t, evidence.ReturnedLines, 2)
		require.LessOrEqual(t, evidence.ReturnedBytes, 256)
		reconstructed += evidence.Text
		if evidence.NextCursor == "" {
			require.False(t, evidence.Truncated)
			break
		}
		require.True(t, evidence.Truncated)
		req.input.Cursor = evidence.NextCursor
	}
	require.Equal(t, logs, reconstructed)
}

func TestEvidenceCursorRejectsChangedTargetOrContent(t *testing.T) {
	server := newTestServer(t, nil)
	req := runElementRequest{owner: "o", repo: "r", input: getRunInput{RunID: 42, evidenceInput: evidenceInput{MaxBytes: 256}}}
	logs := strings.Repeat("x", 1000)
	result, err := server.boundedLogResult(logs, req)
	require.NoError(t, err)
	req.input.Cursor = result.StructuredContent.(logEvidence).NextCursor
	_, err = server.boundedLogResult(logs+"y", req)
	require.Error(t, err)
	req.input.RunID++
	_, err = server.boundedLogResult(logs, req)
	require.Error(t, err)
}
