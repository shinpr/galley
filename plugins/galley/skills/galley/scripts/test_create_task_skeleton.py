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
from collections.abc import Callable


SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
GENERATOR = SCRIPT_DIR / "create_task_skeleton.py"

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


def write_environment_default(
    root: pathlib.Path,
    cwd: pathlib.Path,
    cli: str,
    *,
    inline_executor: bool = False,
    effort: str | None = None,
) -> None:
    # Share the production repo-key helper so these tests fail if skeleton
    # resolution drifts from the Galley home profile directory contract.
    env_dir = root / "profiles" / create_task_skeleton.repo_key_for(cwd)
    env_dir.mkdir(parents=True, exist_ok=True)
    if inline_executor:
        fields = [f"default_cli: {cli}"]
        if effort is not None:
            fields.append(f"effort: {effort}")
        executor_lines = [f"executor: {{{', '.join(fields)}}}"]
    else:
        executor_lines = ["executor:", f'  default_cli: "{cli}"']
        if effort is not None:
            executor_lines.append(f'  effort: "{effort}"')
    (env_dir / "environment.yaml").write_text(
        "\n".join(
            [
                'id: "test"',
                f'cwd: "{cwd}"',
                "commands: {}",
                *executor_lines,
                "constraints:",
                '  network: "approval_required"',
                '  secrets_policy: "never_read_env_files"',
                '  destructive_commands: "deny"',
                "",
            ]
        ),
        encoding="utf-8",
    )


def generated_executor_cli(yaml_text: str) -> str:
    match = re.search(r"^executor:\n(?:  .+\n)*?  cli: ([A-Za-z0-9_-]+)$", yaml_text, re.MULTILINE)
    if not match:
        raise SystemExit("regression: generated skeleton is missing executor.cli")
    return match.group(1)


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


def assert_fallback_parser_executor_default(body: str, want: str) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        path = pathlib.Path(tmp) / "environment.yaml"
        path.write_text(body, encoding="utf-8")

        original_command: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_command
        original_loader: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_loader
        create_task_skeleton.executor_default_from_profile_command = lambda _path: None
        create_task_skeleton.executor_default_from_profile_loader = lambda _path: None
        try:
            got = create_task_skeleton.executor_default_from_environment(path)
        finally:
            create_task_skeleton.executor_default_from_profile_command = original_command
            create_task_skeleton.executor_default_from_profile_loader = original_loader

    if got != want:
        raise SystemExit(f"regression: fallback parser got {got!r}, want {want!r}")


def assert_loader_failure_falls_back_to_parser(body: str, want: str) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        path = pathlib.Path(tmp) / "environment.yaml"
        path.write_text(body, encoding="utf-8")

        original_command: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_command
        original_loader: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_loader
        create_task_skeleton.executor_default_from_profile_command = lambda _path: None

        def failing_loader(_path: pathlib.Path) -> str | None:
            raise ValueError("go run profile loader failed before emitting JSON")

        create_task_skeleton.executor_default_from_profile_loader = failing_loader
        try:
            got = create_task_skeleton.executor_default_from_environment(path)
        finally:
            create_task_skeleton.executor_default_from_profile_command = original_command
            create_task_skeleton.executor_default_from_profile_loader = original_loader

    if got != want:
        raise SystemExit(f"regression: loader infrastructure failure fallback got {got!r}, want {want!r}")


def assert_profile_command_precedes_source_loader() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        path = pathlib.Path(tmp) / "environment.yaml"
        path.write_text("executor:\n  default_cli: codex\n", encoding="utf-8")

        original_command: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_command
        original_loader: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_loader
        create_task_skeleton.executor_default_from_profile_command = lambda _path: "claude"
        create_task_skeleton.executor_default_from_profile_loader = lambda _path: "codex"
        try:
            got = create_task_skeleton.executor_default_from_environment(path)
        finally:
            create_task_skeleton.executor_default_from_profile_command = original_command
            create_task_skeleton.executor_default_from_profile_loader = original_loader

    if got != "claude":
        raise SystemExit(f"regression: installed galley command should precede source loader, got {got!r}")


def assert_invalid_executor_default_is_not_masked() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        path = pathlib.Path(tmp) / "environment.yaml"
        path.write_text("executor:\n  default_cli: other\n", encoding="utf-8")

        original_command: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_command
        original_loader: Callable[[pathlib.Path], str | None] = create_task_skeleton.executor_default_from_profile_loader
        create_task_skeleton.executor_default_from_profile_command = lambda _path: None
        create_task_skeleton.executor_default_from_profile_loader = lambda _path: None
        try:
            try:
                create_task_skeleton.executor_default_from_environment(path)
            except ValueError as exc:
                if "executor.default_cli" in str(exc):
                    return
                raise
            raise SystemExit("regression: invalid executor.default_cli should fail instead of falling back")
        finally:
            create_task_skeleton.executor_default_from_profile_command = original_command
            create_task_skeleton.executor_default_from_profile_loader = original_loader


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

    # Default skeleton must not include enabled-only runtime fields.
    assert_not_matches(yaml_text, r"^    outputs:", "outputs[] in default skeleton")
    assert_not_matches(yaml_text, r"^    required:", "required in default skeleton")
    assert_not_matches(yaml_text, r"^    allowed_paths:", "allowed_paths in default skeleton")
    assert_not_matches(yaml_text, r"^    mode:", "mode in default skeleton")
    if generated_executor_cli(yaml_text) != "claude":
        raise SystemExit("regression: unset environment executor default should generate executor.cli: claude")
    assert_not_matches(yaml_text, r"^\s+max_budget_usd:", "executor.max_budget_usd in default skeleton")
    # Effort is resolved at run time from environment.yaml then built-ins; the
    # skeleton must not pin the built-in default or copy environment effort.
    assert_not_matches(yaml_text, r"^\s+effort:", "executor.effort in default skeleton")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude")
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "claude":
        raise SystemExit("regression: environment executor default should generate executor.cli: claude")
    assert_not_matches(yaml_text, r"^\s+effort:", "executor.effort when environment only sets default_cli")

    # Non-default environment effort must not be authored into the task YAML.
    # Runtime resolution still sees the environment value because the skeleton
    # leaves executor.effort unpinned.
    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "codex", effort="medium")
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "codex":
        raise SystemExit(
            "regression: environment default_cli codex with non-default effort "
            "should still generate executor.cli: codex"
        )
    assert_not_matches(
        yaml_text,
        r"^\s+effort:",
        "executor.effort when environment sets a non-default effort",
    )
    if "medium" in yaml_text:
        raise SystemExit(
            "regression: non-default environment effort must not be copied into the generated skeleton"
        )

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude", inline_executor=True)
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "claude":
        raise SystemExit("regression: inline environment executor default should generate executor.cli: claude")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "claude")
        yaml_text = generate_skeleton(tmpdir, "--executor-cli", "codex", root=root)
    if generated_executor_cli(yaml_text) != "codex":
        raise SystemExit("regression: explicit --executor-cli should override environment default")

    # glm is a valid executor backend (Claude binary against GLM's endpoint), so
    # both the environment default and an explicit --executor-cli must generate
    # executor.cli: glm rather than silently falling back to claude.
    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        write_environment_default(root, fake_cwd, "glm")
        yaml_text = generate_skeleton(tmpdir, root=root)
    if generated_executor_cli(yaml_text) != "glm":
        raise SystemExit("regression: environment executor default glm should generate executor.cli: glm")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        yaml_text = generate_skeleton(tmpdir, "--executor-cli", "glm", root=root)
    if generated_executor_cli(yaml_text) != "glm":
        raise SystemExit("regression: explicit --executor-cli glm should generate executor.cli: glm")

    with tempfile.TemporaryDirectory() as tmp:
        tmpdir = pathlib.Path(tmp)
        fake_cwd = tmpdir / "repo"
        fake_cwd.mkdir(parents=True, exist_ok=True)
        root = tmpdir / "galley"
        yaml_text = generate_skeleton(tmpdir, "--executor-cli", "grok", root=root)
    if generated_executor_cli(yaml_text) != "grok":
        raise SystemExit("regression: explicit --executor-cli grok should generate executor.cli: grok")

    assert_fallback_parser_executor_default(
        'id: "test"\ncwd: "/tmp/repo"\ncommands: {}\nexecutor:\n  default_cli: "claude"\n',
        "claude",
    )
    assert_fallback_parser_executor_default(
        'id: "test"\ncwd: "/tmp/repo"\ncommands: {}\nexecutor: {default_cli: claude}\n',
        "claude",
    )
    assert_loader_failure_falls_back_to_parser(
        'id: "test"\ncwd: "/tmp/repo"\ncommands: {}\nexecutor:\n  default_cli: "claude"\n',
        "claude",
    )
    assert_profile_command_precedes_source_loader()
    assert_invalid_executor_default_is_not_masked()

    print("create_task_skeleton.py preflight and executor default contracts hold")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
