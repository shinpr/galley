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

- `auto`: Windows uses Git Bash when discoverable, otherwise `cmd.exe`.
- `sh`: Allowed when `/bin/sh` is known to exist on the Windows host; prefer `bash` for Git Bash environments.
- `bash`: Use for checks that rely on POSIX tools or syntax such as `grep`, `find`, `xargs`, `test`, `$()`, POSIX pipelines using POSIX commands, or single-quoted shell strings.
- `cmd`: Use for commands written for Command Prompt.
- `powershell`: Use for commands written for Windows PowerShell.
- `pwsh`: Use for commands written for PowerShell 7, which is separate from Windows PowerShell and may require separate installation.

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
