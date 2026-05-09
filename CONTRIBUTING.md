# Contributing

Galley is early-stage software. Small, focused changes are easiest to review.

## Development Setup

Requirements:

- Go version from `go.mod`
- `git`
- `python3` for JSON validation used by CI
- `claude`, `codex`, and `gh` only for workflows that exercise those integrations

Run the local checks before opening a PR:

```sh
gofmt -w ./cmd ./internal
go test ./...
go build ./cmd/galley
./scripts/smoke-local.sh
```

Validate examples when changing task or profile formats:

```sh
galley task validate examples/afk-task.yaml
galley profile validate --kind quality examples/quality-default.yaml
galley profile validate --kind environment examples/environment-local.yaml
```

Regenerate the skill-bundled task schema when changing the task YAML contract:

```sh
go run ./cmd/galley schema generate
go run ./cmd/galley schema check
```

Validate plugin metadata when changing plugin or skill files:

```sh
claude plugin validate plugins/galley
claude plugin validate .
```

## Change Guidelines

- Keep daemon behavior conservative: preserve evidence, avoid silent data loss, and make retry or escalation state visible.
- Keep task/profile schemas backward-compatible where practical; document breaking changes in `CHANGELOG.md`.
- Keep prompts provider-specific when behavior depends on the model or CLI path.
- Keep README focused on onboarding. Put detailed task/profile behavior in `docs/`.

## Release Notes

Add a short entry to `CHANGELOG.md` for user-visible CLI, task YAML, profile, prompt, daemon, plugin, or security changes.

## Releases

Create releases from the GitHub UI. When a GitHub Release is published, `.github/workflows/release.yml` runs GoReleaser and attaches macOS, Linux, and Windows archives to the release.

The installer downloads those release assets by default. `go install github.com/shinpr/galley/cmd/galley@latest` remains available for Go users.
