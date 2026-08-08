package tests

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine running. This file
// carries no build tag on purpose: the tests in this package are behind the
// `integration` and `live` tags, and the leak check must apply to whichever of
// them is compiled in.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
