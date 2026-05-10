package task

import (
	"strings"
	"testing"
)

func TestUnsafeCheckpointCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		want    string // substring of the reason; "" means the command is accepted
	}{
		{"accepted simple", "go test ./...", ""},
		{"accepted with relative dir token", "go test ./internal/foo/...", ""},
		{"accepted stderr-to-stdout redirect", "go vet ./... 2>&1", ""},
		{"empty", "   ", "command is empty"},
		{"newline", "go test ./...\nrm -rf .", "single line"},
		{"control char", "go test\x01", "control character"},
		{"command substitution dollar", "echo $(whoami)", "disallowed shell feature"},
		{"command substitution brace", "echo ${HOME}", "disallowed shell feature"},
		{"process substitution", "diff <(a) <(b)", "disallowed shell feature"},
		{"backtick", "echo `id`", "disallowed shell feature"},
		{"absolute path token", "/usr/bin/true", "absolute path token"},
		{"traversal token", "go test ../other", "parent-directory traversal token"},
		{"embedded traversal token", "cat ./a/../../etc/passwd", "parent-directory traversal token"},
		{"redirect to absolute path token", "go test > /tmp/out.log", "absolute path token"},
		{"redirect outside worktree", "go test ./... 2>>../outside.log", "redirect"},
		{"dangerous shutdown", "shutdown -h now", "forbidden pattern"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := UnsafeCheckpointCommand(tc.command)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("UnsafeCheckpointCommand(%q) = %q; want accepted", tc.command, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("UnsafeCheckpointCommand(%q) = %q; want substring %q", tc.command, got, tc.want)
			}
		})
	}
}
