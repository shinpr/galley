package skeleton

import (
	"testing"
)

func TestPathInsideEffectiveSeparatorBoundary(t *testing.T) {
	if pathInsideEffective("foo-bar/x.go", []string{"foo"}) {
		t.Fatal("foo-bar must not be treated as inside foo")
	}
	if !pathInsideEffective("foo/x.go", []string{"foo"}) {
		t.Fatal("foo/x.go must be inside foo")
	}
	if !pathInsideEffective("anything", []string{"."}) {
		t.Fatal(`"." prefix must match any path`)
	}
}

func TestPathInsideEffectiveIsCaseSensitive(t *testing.T) {
	if pathInsideEffective("Secrets/key.go", []string{"secrets"}) {
		t.Fatal("logical task paths must preserve case")
	}
}

func TestPathInsideProtectedFoldsCase(t *testing.T) {
	if !pathInsideProtected("Secrets/key.go", []string{"secrets"}) {
		t.Fatal("protected paths must not be bypassed by case changes")
	}
}
