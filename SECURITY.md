# Security Policy

Galley is a local automation tool for trusted repositories and trusted task authors.

Task YAML, profiles, and PR rerun comments can influence local execution. Treat them as privileged inputs.

## Supported Versions

Galley is currently in early preview. Security fixes are provided on the `main` branch until versioned releases are established.

## Reporting A Vulnerability

Report security issues privately to the maintainer before opening a public issue.

- Preferred: GitHub private vulnerability reporting at https://github.com/shinpr/galley/security/advisories/new
- If that is unavailable, open a GitHub issue without exploit details and ask for a private contact path.

Include the affected version or commit, the execution mode, the task/profile inputs involved, and the local impact you observed.

## Scope

Galley assumes trusted task authors and trusted repositories. A malicious task YAML or profile causing local command execution is the documented trust model, not a vulnerability by itself.

Security reports are most useful when they show one of these outcomes:

- execution outside the authority the task/profile was meant to grant
- writes outside the expected worktree or allowed paths
- leakage between unrelated tasks, repositories, profiles, or run directories
- PR comment behavior outside the documented `/galley rerun` and `/galley requeue` semantics
- secret exposure caused by Galley rather than by a trusted local command
- incorrect cleanup or state movement that can hide or destroy evidence

## Trust Boundaries

- Run Galley only against repositories you trust.
- Keep secrets out of task-accessible files and generated worktrees.
- Review task YAML before queueing when it came from another user or process.
- Review profile commands before enabling them; runnable checks execute locally through the shell.
- Treat PR rerun comments as instructions that may affect subsequent local execution.

Galley records run evidence for review, but that evidence is not a sandbox boundary. Use isolated worktrees, scoped allowed paths, and local OS or container controls when stronger isolation is required.
