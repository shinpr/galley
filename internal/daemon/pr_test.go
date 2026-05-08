package daemon

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shinpr/galley/internal/task"
)

func TestPRTitleTruncatesRunes(t *testing.T) {
	t.Parallel()
	title := prTitle(task.Task{Goal: strings.Repeat("界", 80)})
	if len([]rune(title)) != 72 {
		t.Fatalf("rune length got %d", len([]rune(title)))
	}
	if !utf8.ValidString(title) {
		t.Fatalf("title is invalid UTF-8: %q", title)
	}
}
