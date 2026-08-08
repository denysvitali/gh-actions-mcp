package config

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine running. This
// package starts none of its own, so the check is a tripwire: it makes the "no
// goroutines here" claim in doc.go enforced rather than aspirational.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
