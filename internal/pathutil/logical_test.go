package pathutil

import "testing"

func TestInsideAnyLogicalPathNormalizesSeparatorsAndCaseByOS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		path     string
		prefixes []string
		goos     string
		want     bool
	}{
		{name: "slash descendant", path: "internal/task/file.go", prefixes: []string{"internal"}, goos: "linux", want: true},
		{name: "backslash descendant", path: `internal\task\file.go`, prefixes: []string{"internal/task"}, goos: "linux", want: true},
		{name: "sibling", path: "internal-task/file.go", prefixes: []string{"internal"}, goos: "linux", want: false},
		{name: "mac case fold", path: "Secrets/key.go", prefixes: []string{"secrets"}, goos: "darwin", want: true},
		{name: "windows case fold", path: "Secrets/key.go", prefixes: []string{"secrets"}, goos: "windows", want: true},
		{name: "linux case sensitive", path: "Secrets/key.go", prefixes: []string{"secrets"}, goos: "linux", want: false},
		{name: "root", path: "anything", prefixes: []string{"."}, goos: "linux", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := InsideAnyLogicalPathForOS(tc.path, tc.prefixes, tc.goos); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
