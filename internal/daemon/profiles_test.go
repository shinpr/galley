package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/task"
)

func TestResolveProfileFilesUsesRepoProfilesWhenNoOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	key, qualityPath, environmentPath, err := galleyhome.RepoProfilePaths(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(qualityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qualityPath, []byte("quality"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentPath, []byte("environment"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveProfileFiles(Options{Root: root}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RepoKey != key || resolved.QualityProfileFile != qualityPath || resolved.EnvironmentProfileFile != environmentPath {
		t.Fatalf("resolved got %#v", resolved)
	}
}

func TestResolveProfileFilesKeepsExplicitOverride(t *testing.T) {
	t.Parallel()
	resolved, err := resolveProfileFiles(Options{
		Root:               t.TempDir(),
		QualityProfileFile: "/tmp/quality.yaml",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resolved.QualityProfileFile != "/tmp/quality.yaml" {
		t.Fatalf("quality override got %q", resolved.QualityProfileFile)
	}
}

func TestRunOnceBranchesNewWorktreeFromEnvironmentPRBaseOriginRef(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	baseSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "push", "origin", "HEAD:refs/heads/feature-base")
	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", "advance.txt")
	runDaemonGit(t, repo, "commit", "-m", "advance")
	headSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	if baseSHA == headSHA {
		t.Fatal("setup failed: baseSHA and headSHA should differ")
	}
	writeRepoEnvironmentProfile(t, root, repo, "feature-base")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != baseSHA {
		t.Fatalf("worktree HEAD got %q, want origin/feature-base SHA %q (source HEAD=%q)", got, baseSHA, headSHA)
	}
	matches, err := filepath.Glob(filepath.Join(root, "runs", "*", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 profiles.json, got %d", len(matches))
	}
	var payload struct {
		Bundle struct {
			Environment *struct {
				PR struct {
					Base string `json:"base,omitempty"`
				} `json:"pr"`
			} `json:"environment,omitempty"`
		} `json:"bundle"`
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode profiles.json: %v", err)
	}
	if payload.Bundle.Environment == nil || payload.Bundle.Environment.PR.Base != "feature-base" {
		t.Fatalf("profiles.json bundle.environment.pr.base got %#v", payload.Bundle)
	}
}

func TestRunOnceBranchesNewWorktreeFromLocalHeadsFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	baseSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "branch", "feature-local")
	if err := os.WriteFile(filepath.Join(repo, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, repo, "add", "advance.txt")
	runDaemonGit(t, repo, "commit", "-m", "advance")
	headSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	if baseSHA == headSHA {
		t.Fatal("setup failed: baseSHA and headSHA should differ")
	}
	writeRepoEnvironmentProfile(t, root, repo, "feature-local")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != baseSHA {
		t.Fatalf("worktree HEAD got %q, want refs/heads/feature-local SHA %q", got, baseSHA)
	}
}

func TestRunOnceFailsWhenPRBaseRefMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	writeRepoEnvironmentProfile(t, root, repo, "does-not-exist")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo should-not-run\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected pr.base resolution failure")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatalf("expected a workspace failure attempt: %#v", failedTask)
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil || last.Error.Phase != "workspace" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	if !strings.Contains(last.Error.Message, "refs/remotes/origin/does-not-exist") || !strings.Contains(last.Error.Message, "refs/heads/does-not-exist") {
		t.Fatalf("attempt error message missing both attempted refs: %q", last.Error.Message)
	}
}

func TestRunOnceFailsWhenStaleOriginRefAndFetchFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	bogusRemote := filepath.Join(t.TempDir(), "does-not-exist.git")
	runDaemonGit(t, repo, "remote", "add", "origin", bogusRemote)
	staleSHA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "update-ref", "refs/remotes/origin/feature-stale", staleSHA)

	writeRepoEnvironmentProfile(t, root, repo, "feature-stale")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo should-not-run\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	})
	if err == nil {
		t.Fatal("expected workspace failure when origin fetch fails and stale origin/<base> exists")
	}
	failedTask, err := task.Load(filepath.Join(root, "tasks", "failed", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failedTask.Attempts) == 0 {
		t.Fatalf("expected a workspace failure attempt: %#v", failedTask)
	}
	last := failedTask.Attempts[len(failedTask.Attempts)-1]
	if last.Error == nil || last.Error.Phase != "workspace" {
		t.Fatalf("attempt error got %#v", last.Error)
	}
	for _, want := range []string{repo, "feature-stale", "fetch", "refs/remotes/origin/feature-stale"} {
		if !strings.Contains(last.Error.Message, want) {
			t.Fatalf("attempt error message missing %q: %q", want, last.Error.Message)
		}
	}
	doneTask := filepath.Join(root, "tasks", "done", "task.yaml")
	if _, statErr := os.Stat(doneTask); statErr == nil {
		t.Fatalf("expected no done task, but found %s", doneTask)
	}
	stillStale := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if stillStale != staleSHA {
		t.Fatalf("stale origin/feature-stale unexpectedly changed; got %q want %q", stillStale, staleSHA)
	}
}

func TestRunOnceRefreshesStaleOriginRefBeforeWorktreeCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agent-workflow")
	repo := initDaemonGitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runDaemonGit(t, t.TempDir(), "init", "--bare", remote)
	runDaemonGit(t, repo, "remote", "add", "origin", remote)
	runDaemonGit(t, repo, "push", "origin", "HEAD:refs/heads/feature-stale")
	shaA := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "HEAD")))
	runDaemonGit(t, repo, "update-ref", "refs/remotes/origin/feature-stale", shaA)
	publisher := filepath.Join(t.TempDir(), "publisher")
	runDaemonGit(t, t.TempDir(), "clone", remote, publisher)
	runDaemonGit(t, publisher, "config", "user.email", "test@example.com")
	runDaemonGit(t, publisher, "config", "user.name", "Test User")
	runDaemonGit(t, publisher, "checkout", "-b", "feature-stale", "origin/feature-stale")
	if err := os.WriteFile(filepath.Join(publisher, "remote-advance.txt"), []byte("remote-advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemonGit(t, publisher, "add", "remote-advance.txt")
	runDaemonGit(t, publisher, "commit", "-m", "remote-advance")
	shaB := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", publisher, "rev-parse", "HEAD")))
	runDaemonGit(t, publisher, "push", "origin", "feature-stale")
	if shaA == shaB {
		t.Fatal("setup failed: shaA and shaB should differ")
	}
	cached := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if cached != shaA {
		t.Fatalf("setup failed: cached origin/feature-stale got %q, want stale SHA %q", cached, shaA)
	}

	writeRepoEnvironmentProfile(t, root, repo, "feature-stale")

	promptPath, schemaPath := writeDaemonPromptFiles(t)
	claudeBin := writeFakeClaude(t, "echo change > daemon-output.txt\necho '{\"status\":\"completed\",\"summary\":\"done\",\"files_modified\":[\"daemon-output.txt\"],\"acceptance_criteria\":[{\"id\":\"AC1\",\"status\":\"satisfied\",\"evidence\":[\"diff\"],\"notes\":\"done\"}],\"verification\":[],\"scope_expansions\":[],\"decisions\":[],\"risks\":[]}'\n")
	taskPath := filepath.Join(root, "tasks", "queued", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDaemonTask(t, taskPath, repo)

	if err := runTestDaemon(context.Background(), Options{
		Root:               root,
		SystemPromptFile:   promptPath,
		JSONSchemaFile:     schemaPath,
		Supervisor:         "claude",
		ClaudeBin:          claudeBin,
		Once:               true,
		MaxConcurrentTasks: 1,
	}); err != nil {
		t.Fatal(err)
	}
	doneTask, err := task.Load(filepath.Join(root, "tasks", "done", "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := taskWorktreePath(repo, doneTask.Worktree.Path)
	got := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", worktreePath, "rev-parse", "HEAD")))
	if got != shaB {
		t.Fatalf("worktree HEAD got %q, want refreshed origin/feature-stale tip %q (stale was %q)", got, shaB, shaA)
	}
	refreshed := strings.TrimSpace(string(mustCommandOutput(t, "git", "-C", repo, "rev-parse", "refs/remotes/origin/feature-stale")))
	if refreshed != shaB {
		t.Fatalf("refs/remotes/origin/feature-stale not refreshed; got %q want %q", refreshed, shaB)
	}
}

func writeRepoEnvironmentProfile(t *testing.T, root, repo, base string) {
	t.Helper()
	_, _, environmentPath, err := galleyhome.RepoProfilePaths(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(environmentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "id: env-test\n" +
		"cwd: " + strconv.Quote(repo) + "\n" +
		"commands: {}\n" +
		"constraints:\n" +
		"  network: approval_required\n" +
		"  secrets_policy: never_read_env_files\n" +
		"  destructive_commands: deny\n" +
		"pr:\n" +
		"  enabled: false\n" +
		"  base: " + base + "\n"
	if err := os.WriteFile(environmentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
