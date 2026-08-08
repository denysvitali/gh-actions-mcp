package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunElementRegistryConsistency pins the invariants that let the get_run
// schema enum, the validation error and the dispatch table all be derived from
// runElements: unique names, a handler for every name, and agreement between the
// ordered name list and the lookup map.
func TestRunElementRegistryConsistency(t *testing.T) {
	t.Parallel()

	require.Len(t, runElementNames, len(runElements))
	require.Len(t, runElementHandlers, len(runElements))

	for index, element := range runElements {
		assert.Equal(t, element.name, runElementNames[index], "name list must keep declaration order")
		assert.NotNil(t, element.handle, "element %q has no handler", element.name)
		assert.NotNil(t, runElementHandlers[element.name], "element %q is not dispatchable", element.name)
		assert.True(t, isValidRunElement(element.name))
	}

	assert.False(t, isValidRunElement(""))
	assert.False(t, isValidRunElement("INFO"), "dispatch keys are lower-case; getRunTyped lower-cases first")
	assert.False(t, isValidRunElement("nope"))
}

// TestGetRunTypedRejectsUnknownElement pins the validation error text, which is
// user-facing tool output, and that it lists the allowed values in enum order.
//
// Deliberately not parallel: NewMCPServer calls github.SetLogger, which writes a
// package-level global (audit finding 11), so two servers built concurrently race
// under -race.
func TestGetRunTypedRejectsUnknownElement(t *testing.T) {
	server := newTestServer(t, nil)
	_, _, err := server.getRunTyped(context.Background(), nil, getRunInput{RunID: 1, Element: "  Bogus "})

	require.Error(t, err)
	assert.Equal(t,
		`invalid element "bogus". Allowed values: info, jobs, logs, log_files, log_sections, artifacts, artifact_content`,
		err.Error())
}

func TestIsValidRunElement(t *testing.T) {
	assert.True(t, isValidRunElement("info"))
	assert.True(t, isValidRunElement("logs"))
	assert.True(t, isValidRunElement("artifact_content"))
	assert.False(t, isValidRunElement("log"))
	assert.False(t, isValidRunElement("unknown"))
}
