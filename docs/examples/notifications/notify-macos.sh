#!/bin/sh
# Galley notification hook sample: macOS desktop notification.
#
# Configure in daemon.yaml:
#   notifications:
#     enabled: true
#     on: [failed, needs_supervisor_review]
#     command: "/absolute/path/to/docs/examples/notifications/notify-macos.sh"
#
# macOS shows osascript notifications under Script Editor. If transient banners
# are easy to miss, set Script Editor's notification style to Alerts.
#
# Galley passes task data as DATA, never as part of the command string:
#   - a JSON object on stdin (task_id, status, repo, summary, run_dir, show_hint)
#   - GALLEY_* environment variables mirroring the same fields
#
# Security: this script reads task content from the environment and passes it to
# osascript as argv values that the AppleScript reads with `item N of argv`, NOT
# concatenated into a shell command or into AppleScript source. Task summaries
# are untrusted and may contain shell metacharacters; keeping them as argv data
# prevents injection.
set -eu

# Drain stdin so Galley's pipe write never blocks even if we only use env vars.
cat >/dev/null || true

title="Galley: ${GALLEY_TASK_STATUS:-task} ${GALLEY_TASK_ID:-}"
body="${GALLEY_SUMMARY:-} (${GALLEY_REPO:-})"

# Pass title/body as argv, read via `item N of argv`, so they stay data.
osascript - "$title" "$body" <<'APPLESCRIPT'
on run argv
  display notification (item 2 of argv) with title (item 1 of argv) sound name "default"
end run
APPLESCRIPT
