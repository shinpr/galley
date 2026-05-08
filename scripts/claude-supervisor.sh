#!/bin/sh
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REQUEST="$(mktemp "${TMPDIR:-/tmp}/galley-claude-supervisor-request.XXXXXX.json")"
PROMPT="$(mktemp "${TMPDIR:-/tmp}/galley-claude-supervisor-prompt.XXXXXX.md")"
trap 'rm -f "$REQUEST" "$PROMPT"' EXIT

cat >"$REQUEST"

SYSTEM_PROMPT="$(cat "$ROOT/prompts/claude-supervisor-review.md")"
SCHEMA="$(cat "$ROOT/schemas/supervisor-verdict.schema.json")"
cat "$REQUEST" >"$PROMPT"

DEBUG_ARGS=""
if [ -n "${GALLEY_SUPERVISOR_ARTIFACT_DIR:-}" ]; then
  mkdir -p "$GALLEY_SUPERVISOR_ARTIFACT_DIR"
  DEBUG_ARGS="--debug-file $GALLEY_SUPERVISOR_ARTIFACT_DIR/claude_supervisor_debug.log"
fi

claude -p \
  --bare \
  --no-session-persistence \
  --permission-mode plan \
  --tools default \
  --allowedTools "Read,Grep,Glob,Bash(pwd),Bash(ls *),Bash(rg *),Bash(sed *),Bash(git diff *),Bash(git status *),Bash(go test *),Bash(pnpm test *),Bash(pnpm typecheck *),Bash(npm test *)" \
  --output-format text \
  --system-prompt "$SYSTEM_PROMPT" \
  --json-schema "$SCHEMA" \
  $DEBUG_ARGS \
  <"$PROMPT"
