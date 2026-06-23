#!/bin/sh
# Galley notification hook sample: Slack incoming webhook.
#
# Configure in daemon.yaml:
#   notifications:
#     enabled: true
#     on: [failed, needs_supervisor_review]
#     command: "/absolute/path/to/notify-slack.sh"
#
# The Slack webhook URL is owned by this script (export SLACK_WEBHOOK_URL in the
# daemon environment); Galley never holds the secret.
#
# Security: the task summary is untrusted. We build the Slack JSON body with jq
# using the raw stdin payload as input (--argjson / pipeline), so the summary is
# encoded as a JSON string value and is never concatenated into a shell command
# or into the JSON by hand. curl posts the body via --data @- (stdin).
set -eu

: "${SLACK_WEBHOOK_URL:?set SLACK_WEBHOOK_URL in the daemon environment}"

# Read Galley's stdin JSON once.
payload="$(cat)"

# Build the Slack message from the payload fields as data (jq string interpolation
# inside jq is safe: values are kept as JSON, not shell-evaluated).
printf '%s' "$payload" \
  | jq '{text: ("Galley task " + .task_id + " is " + .status + "\n" + .summary + "\nrepo: " + .repo + "\n" + .show_hint)}' \
  | curl -sS -X POST -H 'Content-Type: application/json' --data @- "$SLACK_WEBHOOK_URL"
