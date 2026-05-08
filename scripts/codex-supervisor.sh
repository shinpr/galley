#!/bin/sh
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ -n "${GALLEY_SUPERVISOR_ARTIFACT_DIR:-}" ]; then
  mkdir -p "$GALLEY_SUPERVISOR_ARTIFACT_DIR"
  REQUEST="$GALLEY_SUPERVISOR_ARTIFACT_DIR/codex_supervisor_request.json"
  PROMPT="$GALLEY_SUPERVISOR_ARTIFACT_DIR/codex_supervisor_prompt.md"
  OUT="$GALLEY_SUPERVISOR_ARTIFACT_DIR/codex_supervisor_last_message.json"
  EVENTS="$GALLEY_SUPERVISOR_ARTIFACT_DIR/codex_supervisor_events.jsonl"
else
  REQUEST="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-request.XXXXXX.json")"
  PROMPT="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-prompt.XXXXXX.md")"
  OUT="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-out.XXXXXX.json")"
  EVENTS="/dev/null"
  trap 'rm -f "$REQUEST" "$PROMPT" "$OUT"' EXIT
fi

cat >"$REQUEST"

cat "$ROOT/prompts/codex-supervisor-review.md" >"$PROMPT"
printf '\n# Evidence JSON\n\n' >>"$PROMPT"
cat "$REQUEST" >>"$PROMPT"

codex exec \
  --cd "${GALLEY_SUPERVISOR_WORKDIR:-$PWD}" \
  --sandbox read-only \
  --json \
  --output-schema "$ROOT/schemas/supervisor-verdict.schema.json" \
  --output-last-message "$OUT" \
  - <"$PROMPT" >"$EVENTS"

cat "$OUT"
