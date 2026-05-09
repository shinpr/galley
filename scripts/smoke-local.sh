#!/bin/sh
set -eu
set -o pipefail 2>/dev/null || true

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
BIN_DIR="$TMP_DIR/bin"
REPO_DIR="$TMP_DIR/repo"
WORKFLOW_DIR="$TMP_DIR/workflow"

mkdir -p "$BIN_DIR" "$WORKFLOW_DIR/tasks/draft"

go build -o "$TMP_DIR/galley" "$ROOT_DIR/cmd/galley"

cat > "$BIN_DIR/claude" <<'SH'
#!/bin/sh
if [ "${GALLEY_CLAUDE_GUARD_MODE:-}" = "supervisor" ]; then
  cat > /dev/null
  echo '{"status":"accepted","summary":"smoke supervisor accepted","acceptance_gaps":[],"reviewed_files":["smoke-output.txt"],"acceptance_evidence":[{"ac_id":"AC1","evidence":["smoke-output.txt exists"]}],"findings":[],"residual_risks":[],"discussion_items":[],"confidence":"high","next_work_order":""}'
  exit 0
fi
cat > /dev/null
echo "smoke" > smoke-output.txt
echo '{"status":"completed","summary":"smoke done","files_modified":["smoke-output.txt"],"acceptance_criteria":[{"id":"AC1","status":"satisfied","evidence":["smoke-output.txt"],"notes":"created smoke output"}],"verification":[{"command":"test -f smoke-output.txt","status":"passed","reason":"file exists","output_excerpt":"ok"}],"decisions":[],"risks":[]}'
SH
chmod +x "$BIN_DIR/claude"

export PATH="$BIN_DIR:$PATH"

git init "$REPO_DIR" >/dev/null
git -C "$REPO_DIR" config user.email smoke@example.com
git -C "$REPO_DIR" config user.name "Galley Smoke"
echo "smoke repo" > "$REPO_DIR/README.md"
git -C "$REPO_DIR" add README.md
git -C "$REPO_DIR" commit -m initial >/dev/null

cat > "$WORKFLOW_DIR/tasks/draft/smoke.yaml" <<YAML
id: "task-smoke"
mode: "afk"
status: "draft"
goal: "Create a smoke output file."
acceptance_criteria:
  - id: "AC1"
    text: "smoke-output.txt exists in the execution worktree."
    verification: "test -f smoke-output.txt"
    status: "pending"
scope:
  cwd: "$REPO_DIR"
  allowed_paths:
    - "."
  forbidden_paths: []
  permission: "sandbox-full-access"
execution_policy:
  loop_budget: 1
  timeout_ms: 120000
  afk_decision_policy: "choose-smallest-reversible"
  stop_on_destructive_operation: true
  stop_on_missing_secret: false
  stop_on_external_service_unavailable: false
worktree:
  enabled: true
  branch: "agent/smoke"
  path: "../worktrees/smoke"
supervisor:
  review_iterations: 0
executor:
  cli: "claude"
  model: "opus"
  effort: "high"
  prompt_profile: "codexized-claude-executor-v1"
  prompt_mode: "replace"
  max_budget_usd: 0
decisions: []
risks: []
attempts: []
verification:
  commands: []
pr:
  url: ""
  status: ""
YAML

"$TMP_DIR/galley" task validate "$WORKFLOW_DIR/tasks/draft/smoke.yaml" >/dev/null
"$TMP_DIR/galley" task queue "$WORKFLOW_DIR/tasks/draft/smoke.yaml" --root "$WORKFLOW_DIR" --reason "local smoke" >/dev/null
"$TMP_DIR/galley" daemon run --once --root "$WORKFLOW_DIR" >/dev/null

DONE_TASK="$WORKFLOW_DIR/tasks/done/smoke.yaml"
test -f "$DONE_TASK"
grep -q 'status: accepted' "$DONE_TASK"
find "$WORKFLOW_DIR/runs" -name supervisor_verdict.json -print -quit | grep -q supervisor_verdict.json

echo "Galley local smoke passed"
echo "workflow_root=$WORKFLOW_DIR"
echo "repo=$REPO_DIR"
