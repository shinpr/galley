package pathutil

import "testing"

func TestInsideAnyLogicalPathNormalizesSeparatorsAndPreservesCase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		path     string
		prefixes []string
		want     bool
	}{
		{name: "slash descendant", path: "internal/task/file.go", prefixes: []string{"internal"}, want: true},
		{name: "backslash descendant", path: `internal\task\file.go`, prefixes: []string{"internal/task"}, want: true},
		{name: "sibling", path: "internal-task/file.go", prefixes: []string{"internal"}, want: false},
		{name: "case differs", path: "Secrets/key.go", prefixes: []string{"secrets"}, want: false},
		{name: "root", path: "anything", prefixes: []string{"."}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := InsideAnyLogicalPath(tc.path, tc.prefixes); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInsideAnyProtectedPathFoldsCase(t *testing.T) {
	t.Parallel()
	if !InsideAnyProtectedPath("Secrets/key.go", []string{"secrets"}) {
		t.Fatal("protected paths must not be bypassed by case changes")
	}
	if InsideAnyProtectedPath("secrets-old/key.go", []string{"secrets"}) {
		t.Fatal("protected paths must preserve segment boundaries")
	}
}
