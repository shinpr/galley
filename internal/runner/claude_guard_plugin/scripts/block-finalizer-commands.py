#!/usr/bin/env python3
import json
import re
import shlex
import sys

BLOCKED = [
    re.compile(r"(^|[;&|()\s])git\s+commit(\s|$)"),
    re.compile(r"(^|[;&|()\s])git\s+push(\s|$)"),
    re.compile(r"(^|[;&|()\s])git\s+tag(\s|$)"),
    re.compile(r"(^|[;&|()\s])gh\s+pr\s+create(\s|$)"),
    re.compile(r"(^|[;&|()\s])gh\s+pr\s+edit(\s|$)"),
    re.compile(r"(^|[;&|()\s])gh\s+pr\s+merge(\s|$)"),
    re.compile(r"(^|[;&|()\s])gh\s+pr\s+close(\s|$)"),
]

BLOCKED_ARGV = {
    ("git", "commit"),
    ("git", "push"),
    ("git", "tag"),
    ("gh", "pr", "create"),
    ("gh", "pr", "edit"),
    ("gh", "pr", "merge"),
    ("gh", "pr", "close"),
}


def deny(reason):
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }))


def command_text_matches(command):
    for pattern in BLOCKED:
        if pattern.search(command):
            return True
    return False


def argv_matches(tokens):
    for i in range(len(tokens)):
        for blocked in BLOCKED_ARGV:
            if tuple(tokens[i:i + len(blocked)]) == blocked:
                return True
        if tokens[i] in {"bash", "sh", "zsh"} and i + 2 < len(tokens) and tokens[i + 1] == "-c":
            if command_matches(tokens[i + 2]):
                return True
        if tokens[i] == "eval" and i + 1 < len(tokens):
            if command_matches(" ".join(tokens[i + 1:])):
                return True
    return False


def command_matches(command):
    if command_text_matches(command):
        return True
    tokens = shlex.split(command, posix=True)
    return argv_matches(tokens)


def main():
    try:
        payload = json.load(sys.stdin)
    except Exception:
        deny(
            "The command guard could not read the tool request.\n\n"
            "Pause this tool call and report the guard input parsing failure in the structured JSON result."
        )
        return

    tool_input = payload.get("tool_input") or payload.get("toolInput") or {}
    if not isinstance(tool_input, dict):
        deny(
            "The command guard could not identify the shell command.\n\n"
            "Pause this tool call and report the guard input shape mismatch in the structured JSON result."
        )
        return
    command = tool_input.get("command", "")
    if not isinstance(command, str):
        deny(
            "The command guard could not read a shell command string.\n\n"
            "Pause this tool call and report the guard input shape mismatch in the structured JSON result."
        )
        return
    try:
        blocked = command_matches(command)
    except ValueError:
        deny(
            "The command guard could not parse the shell command safely.\n\n"
            "Pause this tool call and report the command parsing failure in the structured JSON result."
        )
        return
    if blocked:
        deny(
            "The calling orchestrator handles commit, push, and pull request creation after review approval.\n\n"
            "Keep your completed changes in the worktree. Continue with implementation and verification, "
            "then return the required structured JSON result with changed files, acceptance evidence, "
            "verification evidence, decisions, and risks.\n\n"
            "If finalization seems necessary for the task, record that need as a risk in the JSON result "
            "so the orchestrator can handle it."
        )
        return


if __name__ == "__main__":
    main()
