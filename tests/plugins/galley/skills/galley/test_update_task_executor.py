#!/usr/bin/env python3
"""Focused regression checks for update_task_executor.py."""

from __future__ import annotations

import pathlib
import subprocess
import sys
import tempfile


REPO_ROOT = pathlib.Path(__file__).resolve().parents[5]
UPDATER = REPO_ROOT / "plugins" / "galley" / "skills" / "galley" / "scripts" / "update_task_executor.py"


def run_updater(task_path: pathlib.Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(UPDATER), str(task_path), *args],
        capture_output=True,
        text=True,
        check=False,
    )


def write_task(path: pathlib.Path, content: str, newline: str | None = None) -> None:
    with path.open("w", encoding="utf-8", newline=newline) as task_file:
        task_file.write(content)


def test_adds_executor_block_without_interpreting_task_status(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    write_task(
        task_path,
        "id: task-1\nstatus: running\ngoal: demo\npreflight: {}\ndecisions: []\n",
    )

    result = run_updater(
        task_path,
        "--cli",
        "codex",
        "--model",
        "gpt-5.4",
        "--effort",
        "high",
    )

    if result.returncode != 0:
        raise AssertionError(result.stderr)
    expected = (
        "id: task-1\n"
        "status: running\n"
        "goal: demo\n"
        "executor:\n"
        '  cli: "codex"\n'
        '  model: "gpt-5.4"\n'
        '  effort: "high"\n'
        "preflight: {}\n"
        "decisions: []\n"
    )
    if task_path.read_text(encoding="utf-8") != expected:
        raise AssertionError("executor block was not inserted before preflight")


def test_updates_only_requested_executor_fields(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    original = (
        "id: task-1\n"
        "executor:\n"
        '  cli: "claude"\n'
        '  model: "claude-opus-4-6"\n'
        '  effort: "high"\n'
        "risks: []\n"
    )
    write_task(task_path, original)

    result = run_updater(task_path, "--cli", "grok", "--unset-model")

    if result.returncode != 0:
        raise AssertionError(result.stderr)
    expected = (
        "id: task-1\n"
        "executor:\n"
        '  cli: "grok"\n'
        '  effort: "high"\n'
        "risks: []\n"
    )
    if task_path.read_text(encoding="utf-8") != expected:
        raise AssertionError("unrequested executor fields were not preserved")


def test_removes_empty_executor_block(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    write_task(task_path, "id: task-1\nexecutor:\n  cli: codex\nrisks: []\n")

    result = run_updater(task_path, "--unset-cli")

    if result.returncode != 0:
        raise AssertionError(result.stderr)
    if task_path.read_text(encoding="utf-8") != "id: task-1\nrisks: []\n":
        raise AssertionError("empty executor block was not removed")


def test_preserves_crlf_line_endings(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    write_task(task_path, "id: task-1\r\nexecutor:\r\n  cli: claude\r\nrisks: []\r\n", newline="")

    result = run_updater(task_path, "--cli", "codex")

    if result.returncode != 0:
        raise AssertionError(result.stderr)
    content = task_path.read_bytes()
    if b"\n" in content.replace(b"\r\n", b""):
        raise AssertionError("updater introduced non-CRLF line endings")


def test_preserves_bom_before_first_executor_block(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    original = "\ufeffexecutor:\r\n  cli: claude\r\nid: task-1\r\nrisks: []\r\n"
    write_task(task_path, original, newline="")

    result = run_updater(task_path, "--cli", "codex")

    if result.returncode != 0:
        raise AssertionError(result.stderr)
    content = task_path.read_bytes()
    if not content.startswith(b"\xef\xbb\xbf"):
        raise AssertionError("UTF-8 BOM was not preserved")
    decoded = content.decode("utf-8-sig")
    if decoded.count("executor:") != 1:
        raise AssertionError("executor block was duplicated")
    if '  cli: "codex"\r\n' not in decoded:
        raise AssertionError("executor block was not updated with CRLF preserved")


def test_rejects_unsupported_inline_executor_without_writing(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    original = "id: task-1\nexecutor: {cli: claude}\nrisks: []\n"
    write_task(task_path, original)

    result = run_updater(task_path, "--cli", "codex")

    if result.returncode == 0:
        raise AssertionError("inline executor mapping must be rejected")
    if "block mapping" not in result.stderr:
        raise AssertionError(f"missing actionable error: {result.stderr!r}")
    if task_path.read_text(encoding="utf-8") != original:
        raise AssertionError("task changed after rejected input")


def test_requires_a_requested_change(tmpdir: pathlib.Path) -> None:
    task_path = tmpdir / "task.yaml"
    original = "id: task-1\nrisks: []\n"
    write_task(task_path, original)

    result = run_updater(task_path)

    if result.returncode == 0:
        raise AssertionError("updater must reject an empty change request")
    if task_path.read_text(encoding="utf-8") != original:
        raise AssertionError("task changed after empty request")


def main() -> int:
    tests = (
        test_adds_executor_block_without_interpreting_task_status,
        test_updates_only_requested_executor_fields,
        test_removes_empty_executor_block,
        test_preserves_crlf_line_endings,
        test_preserves_bom_before_first_executor_block,
        test_rejects_unsupported_inline_executor_without_writing,
        test_requires_a_requested_change,
    )
    for test in tests:
        with tempfile.TemporaryDirectory() as tmp:
            test(pathlib.Path(tmp))
    print("update_task_executor.py contracts hold")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
