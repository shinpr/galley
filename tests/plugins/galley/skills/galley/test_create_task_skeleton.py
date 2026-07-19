#!/usr/bin/env python3
"""Focused regression checks for create_task_skeleton.py.

These checks fail if the generated skeleton YAML stops emitting the default
disabled acceptance skeleton preflight gate, or if it accidentally starts
emitting opt-in-only runtime fields such as `outputs` in the default skeleton.

Run directly:

    python3 plugins/galley/skills/galley/scripts/test_create_task_skeleton.py

Exit status 0 means the contract holds; any non-zero exit means the contract
regressed and the message names the missing or unexpected field.
"""

from __future__ import annotations

import importlib.util
import pathlib
import re
import subprocess
import sys
import tempfile


REPO_ROOT = pathlib.Path(__file__).resolve().parents[5]
GENERATOR = REPO_ROOT / "plugins" / "galley" / "skills" / "galley" / "scripts" / "create_task_skeleton.py"

spec = importlib.util.spec_from_file_location("create_task_skeleton", GENERATOR)
if spec is None or spec.loader is None:
    raise RuntimeError(f"unable to load {GENERATOR}")
create_task_skeleton = importlib.util.module_from_spec(spec)
spec.loader.exec_module(create_task_skeleton)


def generate_skeleton(tmpdir: pathlib.Path, *extra_args: str, root: pathlib.Path | None = None) -> str:
    output_dir = tmpdir / "draft"
    output_dir.mkdir(parents=True, exist_ok=True)
    fake_cwd = tmpdir / "repo"
    fake_cwd.mkdir(parents=True, exist_ok=True)
    command = [
        sys.executable,
        str(GENERATOR),
        "demo-skeleton-preflight",
        "--cwd",
        str(fake_cwd),
        "--output-dir",
        str(output_dir),
    ]
    if root is not None:
        command.extend(["--root", str(root)])
    command.extend(extra_args)
    proc = subprocess.run(command, capture_output=True, text=True)
    if proc.returncode != 0:
        raise SystemExit(f"skeleton generation failed: {proc.stderr.strip()}")
    task_path = pathlib.Path(proc.stdout.strip())
    return task_path.read_text(encoding="utf-8")


def generated_executor_cli(yaml_text: str) -> str | None:
    match = re.search(r"^executor:\n(?:  .+\n)*?  cli: ([A-Za-z0-9_-]+)$", yaml_text, re.MULTILINE)
    return match.group(1) if match else None


def assert_contains(yaml_text: str, needle: str, description: str) -> None:
    if needle not in yaml_text:
        raise SystemExit(
            f"regression: generated skeleton is missing {description!r} "
            f"(expected substring: {needle!r})"
        )


def assert_not_matches(yaml_text: str, pattern: str, description: str) -> None:
    if re.search(pattern, yaml_text, re.MULTILINE):
        raise SystemExit(
            f"regression: generated skeleton unexpectedly emits {description!r} "
            f"(pattern: {pattern!r})"
        )


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        yaml_text = generate_skeleton(tmpdir)

    assert_contains(yaml_text, "preflight:\n", "preflight block header")
    assert_contains(
        yaml_text,
        "  acceptance_skeleton:\n    enabled: false\n",
        "preflight.acceptance_skeleton.enabled: false",
    )
    assert_contains(yaml_text, "execution_policy:\n", "execution_policy block")
    assert_contains(yaml_text, "  loop_budget:", "loop_budget")
    assert_contains(yaml_text, "  timeout_ms: 3600000\n", "60-minute timeout_ms baseline")

    assert_not_matches(yaml_text, r"^mode:", "mode in default skeleton")
    assert_not_matches(yaml_text, r"^status:", "status in default skeleton")
    assert_not_matches(yaml_text, r"^supervisor:", "supervisor in default skeleton")
    assert_not_matches(yaml_text, r"^attempts:", "attempts in default skeleton")
    assert_not_matches(yaml_text, r"^verification:", "verification in default skeleton")
    assert_not_matches(yaml_text, r"^pr:", "pr in default skeleton")
    assert_not_matches(yaml_text, r"^worktree:\n(?:  .+\n)*?  enabled:", "worktree.enabled in default skeleton")
    assert_not_matches(yaml_text, r"afk_decision_policy", "afk_decision_policy in default skeleton")
    assert_not_matches(yaml_text, r"stop_on_", "stop_on_* in default skeleton")

    assert_not_matches(yaml_text, r"^    outputs:", "outputs[] in default skeleton")
    assert_not_matches(yaml_text, r"^    required:", "required in default skeleton")
    assert_not_matches(yaml_text, r"^    allowed_paths:", "allowed_paths in default skeleton")
    if generated_executor_cli(yaml_text) is not None:
        raise SystemExit("regression: default skeleton must inherit executor.cli at run time")
    assert_not_matches(yaml_text, r"^\s+max_budget_usd:", "executor.max_budget_usd in default skeleton")
    assert_not_matches(yaml_text, r"^\s+effort:", "executor.effort in default skeleton")

    for cli in ("claude", "codex", "glm", "grok", "kimi"):
        with tempfile.TemporaryDirectory() as tmp:
            yaml_text = generate_skeleton(pathlib.Path(tmp), "--executor-cli", cli)
        if generated_executor_cli(yaml_text) != cli:
            raise SystemExit(f"regression: explicit --executor-cli {cli} was not pinned")

    print("create_task_skeleton.py preflight and executor pinning contracts hold")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
