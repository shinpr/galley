package setup

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/profile"
	"github.com/shinpr/galley/internal/task"
)

func normalizeResult(res *Result) {
	if res == nil {
		return
	}
	res.ReadinessEvidence = truncateString(res.ReadinessEvidence, maxResultTextLength)
	res.RepairGuidance = truncateString(res.RepairGuidance, maxResultTextLength)
	res.Error = truncateString(res.Error, maxResultTextLength)
	if len(res.Commands) > maxResultCommands {
		res.Commands = res.Commands[:maxResultCommands]
	}
	for i := range res.Commands {
		res.Commands[i].Run = truncateString(res.Commands[i].Run, profile.MaxSetupCommandRunLength)
		res.Commands[i].Why = truncateString(res.Commands[i].Why, profile.MaxSetupCommandWhyLength)
		res.Commands[i].StdoutExcerpt = truncateExcerpt(res.Commands[i].StdoutExcerpt)
		res.Commands[i].StderrExcerpt = truncateExcerpt(res.Commands[i].StderrExcerpt)
	}
	if len(res.SuccessfulCommands) > maxResultCommands {
		res.SuccessfulCommands = res.SuccessfulCommands[:maxResultCommands]
	}
	if len(res.InspectedFiles) > maxResultFiles {
		res.InspectedFiles = res.InspectedFiles[:maxResultFiles]
	}
	for i := range res.InspectedFiles {
		res.InspectedFiles[i] = truncateString(res.InspectedFiles[i], 512)
	}
}

func setupCommandTimeout(t task.Task) time.Duration {
	if t.ExecutionPolicy.TimeoutMS > 0 {
		return time.Duration(t.ExecutionPolicy.TimeoutMS) * time.Millisecond
	}
	return 30 * time.Minute
}

func truncateExcerpt(s string) string {
	s = strings.TrimSpace(s)
	return truncateString(s, maxResultExcerptLength)
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	const marker = "..."
	if max <= len(marker) {
		return s[:max]
	}
	remaining := max - len(marker)
	head := remaining / 2
	tail := remaining - head
	return s[:head] + marker + s[len(s)-tail:]
}

// DiscoverRepositorySignals returns a small set of repository setup signal
// paths (manifests, lockfiles, setup docs) the daemon surfaces to the setup
// executor. The list is intentionally bounded so the work order payload stays
// small.
func DiscoverRepositorySignals(workDir string) []string {
	candidates := []string{
		"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock",
		"go.mod", "go.sum",
		"pyproject.toml", "poetry.lock", "requirements.txt", "Pipfile.lock",
		"Cargo.toml", "Cargo.lock",
		"Gemfile", "Gemfile.lock",
		"build.gradle", "build.gradle.kts", "pom.xml",
		"Makefile", "Taskfile.yml",
		".tool-versions", "mise.toml", ".nvmrc",
		"README.md", "CONTRIBUTING.md", "docs/setup.md",
	}
	out := make([]string, 0, 8)
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(workDir, name)); err == nil {
			out = append(out, name)
		}
	}
	if scripts, err := os.ReadDir(filepath.Join(workDir, "scripts")); err == nil {
		for _, entry := range scripts {
			if len(out) >= maxResultFiles {
				break
			}
			if !entry.IsDir() {
				out = append(out, filepath.Join("scripts", entry.Name()))
			}
		}
	}
	return out
}
