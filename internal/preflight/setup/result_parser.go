package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// ResolveExecutorResult parses the setup executor's structured result
// from the provider's canonical output surface. Codex emits the final JSON
// message through `--output-last-message`; Claude streams the JSON through
// stdout JSONL.
func ResolveExecutorResult(opts Options, stdoutTail string) (*Result, error) {
	if task.ExecutorProvider(opts.Task) == "codex" {
		lastMessagePath := filepath.Join(opts.RunDir, runner.CodexLastMessageFilename)
		if data, err := os.ReadFile(lastMessagePath); err == nil {
			if res, ok := parseResultText(string(data)); ok {
				return res, nil
			}
		}
	}
	if res, ok := parseResultText(stdoutTail); ok {
		return res, nil
	}
	for _, line := range strings.Split(strings.TrimSpace(stdoutTail), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			for _, key := range []string{"result", "response", "message"} {
				raw, ok := event[key]
				if !ok {
					continue
				}
				var text string
				if err := json.Unmarshal(raw, &text); err == nil {
					if res, ok := parseResultText(text); ok {
						return res, nil
					}
				}
				if res, ok := parseResultRaw(raw); ok {
					return res, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("setup executor result JSON not found")
}

func parseResultText(text string) (*Result, bool) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, false
	}
	return parseResultRaw([]byte(text[start : end+1]))
}

func parseResultRaw(data []byte) (*Result, bool) {
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, false
	}
	if res.Status == "" || res.Commands == nil {
		return nil, false
	}
	normalizeResult(&res)
	return &res, true
}
