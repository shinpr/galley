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

- `auto`: Galley auto-detects a standard Git for Windows Bash install (`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files\Git\usr\bin\bash.exe`, the `(x86)` equivalents, or Git Bash inferred from a discoverable `git.exe`). Any other PATH-discovered `bash.exe` — including the WSL launcher (`C:\Windows\System32\bash.exe`), a `WindowsApps` shim, MSYS2, Cygwin, Scoop, or Chocolatey-managed Bashes — is not auto-selected. If no usable Git for Windows Bash is found, Galley falls back to `cmd.exe`. Use `required_checks.shell` plus `required_checks.shell_path` to opt into a non-standard Bash explicitly.
- `sh`: Allowed when `/bin/sh` is known to exist on the Windows host; prefer `bash` for Git Bash environments.
- `bash`: Use for checks that rely on POSIX tools or syntax such as `grep`, `find`, `xargs`, `test`, `$()`, POSIX pipelines using POSIX commands, or single-quoted shell strings.
- `cmd`: Use for commands written for Command Prompt.
- `powershell`: Use for commands written for Windows PowerShell.
- `pwsh`: Use for commands written for PowerShell 7, which is separate from Windows PowerShell and may require separate installation.

Responsibility split between Galley and the operator:

- Galley auto-detects only the standard Git for Windows install layouts above. Exhaustively guessing every possible Bash layout (portable Git outside `Program Files`, Scoop, Chocolatey, MSYS2, Cygwin, WSL distros, managed corporate machines) is brittle, so Galley does not attempt it.
- For non-standard Bash installs (MSYS2, Cygwin, portable Git outside `C:\Program Files\Git`), custom PowerShell installs, intentionally WSL-based setups, or Unix environments that need a pinned shell such as Nix or Homebrew Bash, set both `required_checks.shell` (the concrete shell kind) and `required_checks.shell_path` (the explicit executable path). Galley uses the configured path verbatim, skips auto-discovery, and does not infer whether the executable name matches the selected shell kind.
- `required_checks.shell_path` requires an explicit `required_checks.shell` kind. Profile validation rejects `shell_path` when `shell` is empty or `auto`, because there is no shell kind to associate the override with.

During profile authoring, infer the shell from repository evidence:

- If all required checks use POSIX tools or syntax, propose `bash`.
- If all required checks use `cmd.exe` syntax, propose `cmd`.
- If all required checks use Windows PowerShell syntax, propose `powershell`.
- If all required checks use PowerShell 7 syntax, propose `pwsh`.
- If checks mix POSIX syntax with cmd or PowerShell syntax, ask the user which shell to standardize on or which check to rewrite.
- If evidence is unclear, keep `required_checks.shell` unset so Galley uses `auto`.

## Troubleshooting

If a required check fails under `cmd.exe` with a POSIX-tool error, set `environment.required_checks.shell: bash` or install Git for Windows.

Windows profile snippet for POSIX-style checks:

```yaml
required_checks:
  shell: "bash"
```

Windows profile snippet for a non-standard Bash install (for example MSYS2 outside `C:\Program Files\Git`):

```yaml
required_checks:
  shell: "bash"
  shell_path: "C:\\msys64\\usr\\bin\\bash.exe"
```
