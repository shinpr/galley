# Windows Guidance

Use this reference with the active flow when Galley runs `quality.required_checks` on a Windows host.

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
  - Use `required_checks.shell` plus `required_checks.shell_path` to opt into a non-standard shell explicitly.
- `sh`: Allowed when `/bin/sh` is known to exist on the Windows host; prefer `bash` for Git Bash environments.
- `bash`: Use for checks that rely on POSIX tools or syntax such as `grep`, `find`, `xargs`, `test`, `$()`, POSIX pipelines using POSIX commands, or single-quoted shell strings.
- `cmd`: Use for commands written for Command Prompt.
- `powershell`: Use for commands written for Windows PowerShell.
- `pwsh`: Use for commands written for PowerShell 7, which is separate from Windows PowerShell and may require separate installation.

Responsibility split between Galley and the operator:

- Galley auto-detects only the standard Git for Windows install layouts above and Git Bash inferred from a discoverable `git.exe`. Exhaustively guessing arbitrary Bash layouts (Scoop, Chocolatey, MSYS2, Cygwin, WSL distros, managed corporate machines, custom portable layouts that cannot be inferred from `git.exe`) is brittle, so Galley does not attempt it.
- For non-standard Bash installs that are not standard Git for Windows and cannot be inferred from `git.exe` (MSYS2, Cygwin, Scoop, Chocolatey, custom portable layouts), custom PowerShell installs, intentionally WSL-based setups, or Unix environments that need a pinned shell such as Nix or Homebrew Bash, set `required_checks.shell_path` to the explicit executable path. Galley uses the configured path verbatim and skips auto-discovery.
- `required_checks.shell_path` may stand alone when the path's basename is one of `bash`, `sh`, `cmd.exe`, `powershell.exe`, or `pwsh.exe` (case-insensitive, optional `.exe`, either `/` or `\` separators); Galley infers the invocation style from the executable name in that case.
- When both `required_checks.shell` and `required_checks.shell_path` are set, `shell_path` takes precedence as the more specific executable selection. Galley resolves the invocation style from `shell_path`'s basename and will not invoke that executable using an incompatible style from `shell`. The configured `shell` is fallback kind metadata for unrecognized executable names only.
- Profile validation rejects `required_checks.shell_path` only when the basename is not recognized and `required_checks.shell` is empty or `auto` (no usable shell kind can be determined). Set `required_checks.shell` to the matching concrete kind whenever `shell_path` points at a renamed or wrapped shell executable.
- On Windows, `required_checks.shell: bash` without `shell_path` prefers standard Git for Windows Bash discovery. PATH-discovered Bash entries (WSL launcher at `C:\Windows\System32\bash.exe`, `WindowsApps` shim, MSYS2, Cygwin, Scoop, Chocolatey-managed Bashes) are never auto-selected. If no standard Git for Windows Bash is discoverable, Galley fails required-check resolution with an error that names `required_checks.shell_path` rather than launching a bare `bash` whose PATH lookup would silently resolve back to the rejected non-standard entry. Set `required_checks.shell_path` to opt in to one of these non-standard Bashes.

During profile authoring, infer `required_checks.shell` and `required_checks.shell_path` from user and repository evidence:

- If user or repository evidence shows standard Git for Windows is installed, or shows a Git for Windows/portable Git layout where `git.exe` can infer Git Bash, keep `required_checks.shell` unset or propose `auto`; do not set `shell_path`.
- If the user or repository evidence names a non-standard shell executable path whose basename matches `bash`, `sh`, `cmd.exe`, `powershell.exe`, or `pwsh.exe`, set `required_checks.shell_path` to that path and leave `required_checks.shell` unset; Galley infers the invocation style from the executable name.
- If the executable basename is not one of the recognized names (for example, a renamed `custom-bash` wrapper), set both `required_checks.shell` (the concrete shell kind, used as fallback metadata) and `required_checks.shell_path` (the explicit executable path).
- If evidence indicates MSYS2, Cygwin, portable Git, Scoop, Chocolatey, custom PowerShell, WSL, Nix, or Homebrew Bash but no exact executable path is known, ask the user for the path before setting `shell_path`.
- If POSIX checks need Bash and no evidence identifies a standard Git for Windows install or an exact non-standard shell path, ask the user which Bash executable should run the checks before setting `shell` or `shell_path`.
- If all required checks use `cmd.exe` syntax, propose `cmd`.
- If all required checks use Windows PowerShell syntax, propose `powershell`.
- If all required checks use PowerShell 7 syntax, propose `pwsh`.
- If checks mix POSIX syntax with cmd or PowerShell syntax, ask the user which shell to standardize on or which check to rewrite.
- For non-POSIX or shell-neutral checks, if shell evidence is unclear, keep `required_checks.shell` unset so Galley uses `auto`; keep `required_checks.shell_path` unset unless the user provides an exact executable path.

## Troubleshooting

If a POSIX-style required check falls back to `cmd.exe`:

- Install Git for Windows in a standard location and keep `required_checks.shell` unset/auto.
- For a known non-standard Bash executable whose basename is `bash` or `bash.exe`, set just `required_checks.shell_path` to that path. Galley infers Bash invocation from the basename.
- For a renamed or wrapped Bash executable (basename is not `bash`/`bash.exe`), set both `required_checks.shell: bash` and `required_checks.shell_path`.

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
