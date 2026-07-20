# Windows Guidance

Use this reference with the active flow when Galley runs on Windows.

## Installation

For native Windows PowerShell installation, use `scripts/install.ps1`. For Git Bash, MSYS, Cygwin, or WSL, use `scripts/install.sh`.

PowerShell release install:

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.ps1 -OutFile install.ps1 -UseBasicParsing
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

Git Bash/MSYS/Cygwin release install:

```sh
curl -fsSL https://raw.githubusercontent.com/shinpr/galley/main/scripts/install.sh | sh
```

## Required-Check Shell

Galley runs `quality.required_checks` itself after executor attempts. Choose the required-check command text and `environment.required_checks.shell` together for the Windows host that executes those checks.

Shell values:

- `auto`: Galley tries standard Git for Windows Bash locations (`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files\Git\usr\bin\bash.exe`, the `(x86)` equivalents, or Git Bash inferred from a discoverable `git.exe`). If no usable Git for Windows Bash is found, Galley falls back to `cmd.exe`.
  - Auto does not select other PATH-discovered `bash.exe` entries such as the WSL launcher (`C:\Windows\System32\bash.exe`), a `WindowsApps` shim, MSYS2, Cygwin, Scoop, or Chocolatey-managed Bash.
  - Use `required_checks.shell_path` to opt into a non-standard shell executable.
- `sh`: Allowed when `/bin/sh` is known to exist on the Windows host; prefer `bash` for Git Bash environments.
- `bash`: Use for checks that rely on POSIX tools or syntax such as `grep`, `find`, `xargs`, `test`, `$()`, POSIX pipelines using POSIX commands, or single-quoted shell strings.
- `cmd`: Use for commands written for Command Prompt.
- `powershell`: Use for commands written for Windows PowerShell.
- `pwsh`: Use for commands written for PowerShell 7, which is separate from Windows PowerShell and may require separate installation.

Responsibility split between Galley and the operator:

- Galley auto-detects only the standard Git for Windows install layouts above and Git Bash inferred from a discoverable `git.exe`. Exhaustively guessing arbitrary Bash layouts (Scoop, Chocolatey, MSYS2, Cygwin, WSL distros, managed corporate machines, custom portable layouts that cannot be inferred from `git.exe`) is brittle, so Galley does not attempt it.
- When the user provides an exact shell executable path, prefer `required_checks.shell_path`. If the executable name is recognizable (`bash`, `sh`, `cmd`, `powershell`, or `pwsh`, with optional `.exe`, case-insensitive, and either path separator), leave `required_checks.shell` unset; Galley infers the shell kind.
- When both fields are set, `shell_path` takes precedence. `required_checks.shell` acts as fallback metadata only when the executable name is not recognizable. If the name is unrecognizable and `shell` is unset, profile validation rejects the profile.
- On Windows, `required_checks.shell: bash` without `shell_path` also prefers standard Git for Windows Bash discovery and does not auto-select WSL launchers, WindowsApps shims, MSYS2, Cygwin, Scoop, or Chocolatey-managed Bashes. If no standard Git Bash is found, Galley fails with an error that asks for `shell_path`.

During profile authoring, infer `required_checks.shell` and `required_checks.shell_path` from user and repository evidence:

- If user or repository evidence shows standard Git for Windows is installed, or shows a Git for Windows/portable Git layout where `git.exe` can infer Git Bash, keep `required_checks.shell` unset or propose `auto`; do not set `shell_path`.
- If the user or repository evidence names an exact non-standard shell executable path with a recognizable name, set only `required_checks.shell_path`.
- If the exact executable path is a renamed wrapper such as `custom-bash`, set both `required_checks.shell` and `required_checks.shell_path`.
- If evidence indicates MSYS2, Cygwin, portable Git, Scoop, Chocolatey, custom PowerShell, WSL, Nix, or Homebrew Bash but no exact executable path is known, ask the user for the path before setting `shell_path`.
- If POSIX checks need Bash and no evidence identifies a standard Git for Windows install or an exact non-standard shell path, ask the user which Bash executable should run the checks before setting `shell` or `shell_path`.
- If all required checks use `cmd.exe` syntax, propose `cmd`.
- If all required checks use Windows PowerShell syntax, propose `powershell`.
- If all required checks use PowerShell 7 syntax, propose `pwsh`.
- If checks mix POSIX syntax with cmd or PowerShell syntax, ask the user which shell to standardize on or which check to rewrite.
- For non-POSIX or shell-neutral checks, if shell evidence is unclear, keep `required_checks.shell` unset so Galley uses `auto`; keep `required_checks.shell_path` unset unless the user provides an exact executable path.

## Troubleshooting

If an unset/auto POSIX-style required check falls back to `cmd.exe`:

- Install Git for Windows in a standard location and keep `required_checks.shell` unset/auto.
- For a known non-standard Bash executable named `bash` or `bash.exe`, set only `required_checks.shell_path` to the exact executable path.
- For a renamed Bash wrapper, set `required_checks.shell: bash` with `required_checks.shell_path`.

Windows profile snippet for standard Git for Windows auto-detection:

```yaml
required_checks:
  shell: "auto"
```

Windows profile snippet for a non-standard Bash install whose basename is `bash.exe` (for example MSYS2 outside `C:\Program Files\Git`); Galley infers `bash` from the executable name:

```yaml
required_checks:
  shell_path: "C:\\msys64\\usr\\bin\\bash.exe"
```

Windows profile snippet for a renamed Bash wrapper (basename not recognized); pair `shell` with `shell_path`:

```yaml
required_checks:
  shell: "bash"
  shell_path: "C:\\opt\\galley\\custom-bash.exe"
```
