"""Exercise the diagnostic index against mixed failure artifacts."""

import pathlib
import subprocess
import sys
import tempfile

SCRIPT = pathlib.Path(__file__).resolve().parents[5] / "plugins/galley/skills/galley/scripts/diagnose_run.py"


def main():
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        names = ["attempt-1/executor_terminal.json", "attempt-1/run_result.json",
                 "attempt-1/supervisor_error.json", "setup_result.json",
                 "environment_update.json", "attempt-1/codex_supervisor_stderr.log",
                 "attempt-1/claude_supervisor_stderr.log", "attempt-1/grok_supervisor_stderr.log",
                 "setup_executor.stderr.log"]
        for name in names:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('{"error":"provider failed"}' if path.suffix == ".json" else "provider failed")
        result = subprocess.run([sys.executable, str(SCRIPT), str(root)], capture_output=True, text=True)
        assert result.returncode == 0, result.stderr
        for name in names:
            assert name in result.stdout, f"missing {name}: {result.stdout}"
        assert "provider failed" in result.stdout
    print("diagnose_run: passed")


if __name__ == "__main__":
    main()
