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

claude -p \
  --bare \
  --no-session-persistence \
  --permission-mode plan \
  --tools "" \
  --output-format text \
  --system-prompt "$SYSTEM_PROMPT" \
  --json-schema "$SCHEMA" \
  <"$PROMPT"
