package skeleton

import (
	"runtime"
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

func TestPathInsideEffectiveCaseFolding(t *testing.T) {
	inside := pathInsideEffective("Secrets/key.go", []string{"secrets"})
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !inside {
			t.Fatal("on case-insensitive filesystems Secrets must be treated as inside secrets")
		}
	} else if inside {
		t.Fatal("on case-sensitive filesystems case must not be folded")
	}
}
