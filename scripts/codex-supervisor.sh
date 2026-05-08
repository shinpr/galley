#!/bin/sh
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REQUEST="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-request.XXXXXX.json")"
PROMPT="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-prompt.XXXXXX.md")"
OUT="$(mktemp "${TMPDIR:-/tmp}/galley-supervisor-out.XXXXXX.json")"
trap 'rm -f "$REQUEST" "$PROMPT" "$OUT"' EXIT

cat >"$REQUEST"

cat "$ROOT/prompts/codex-supervisor-review.md" >"$PROMPT"
printf '\n# Evidence JSON\n\n' >>"$PROMPT"
cat "$REQUEST" >>"$PROMPT"

codex exec \
  --cd "${GALLEY_SUPERVISOR_WORKDIR:-$PWD}" \
  --sandbox read-only \
  --output-schema "$ROOT/schemas/supervisor-verdict.schema.json" \
  --output-last-message "$OUT" \
  - <"$PROMPT" >/dev/null

cat "$OUT"
