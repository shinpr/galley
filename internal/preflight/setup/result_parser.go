package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/runartifact"
	"github.com/shinpr/galley/internal/runner"
	"github.com/shinpr/galley/internal/task"
)

// ResolveExecutorResult parses the setup executor's structured result
// from the provider's canonical output surface. Codex emits the final JSON
// message through `--output-last-message`; Claude streams the JSON through
// stdout JSONL.
// ResolveExecutorResult extracts the setup executor's result: Grok from its
// envelope, Codex from a last-message file, every transport from stdout as well.
func ResolveExecutorResult(opts Options, stdoutTail string) (*Result, error) {
	switch task.ExecutorTransport(opts.Task) {
	case provider.TransportGrok:
		return resolveGrokSetupResult(opts, stdoutTail)
	case provider.TransportCodex:
		if res, ok := resolveCodexSetupResult(opts); ok {
			return res, nil
		}
	case provider.TransportClaude:
	}
	if res, ok := parseResultText(stdoutTail); ok {
		return res, nil
	}
	if res, ok := scanStdoutForResult(stdoutTail); ok {
		return res, nil
	}
	return nil, fmt.Errorf("setup executor result JSON not found")
}

func resolveGrokSetupResult(opts Options, stdoutTail string) (*Result, error) {
	data, readErr := os.ReadFile(runartifact.Path(opts.RunDir, runartifact.SetupExecutorStdoutFilename))
	if readErr != nil {
		data = []byte(stdoutTail)
	}
	if err := runner.WriteGrokCompletionMetadata(runartifact.Path(opts.RunDir, runartifact.GrokSetupCompletionFilename), data); err != nil {
		return nil, err
	}
	envelope, err := runner.DecodeGrokEnvelope(data)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(runner.GrokResultPayload(envelope), &result); err != nil {
		return nil, fmt.Errorf("decode grok setup result: %w", err)
	}
	if result.Status == "" || result.Commands == nil {
		return nil, fmt.Errorf("invalid grok setup result")
	}
	normalizeResult(&result)
	return &result, nil
}

func resolveCodexSetupResult(opts Options) (*Result, bool) {
	lastMessagePath := filepath.Join(opts.RunDir, runartifact.SetupCodexDirname, runner.CodexLastMessageFilename)
	data, err := os.ReadFile(lastMessagePath)
	if err != nil {
		return nil, false
	}
	return parseResultText(string(data))
}

// scanStdoutForResult reads each captured stdout line as a JSON event and looks
// for the result payload under the keys the executors use.
func scanStdoutForResult(stdoutTail string) (*Result, bool) {
	for _, line := range strings.Split(strings.TrimSpace(stdoutTail), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if res, ok := resultFromEvent(event); ok {
			return res, true
		}
	}
	return nil, false
}

func resultFromEvent(event map[string]json.RawMessage) (*Result, bool) {
	for _, key := range []string{"result", "response", "message"} {
		raw, ok := event[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			if res, ok := parseResultText(text); ok {
				return res, true
			}
		}
		if res, ok := parseResultRaw(raw); ok {
			return res, true
		}
	}
	return nil, false
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
