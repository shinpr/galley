# Changelog

All notable changes to Galley are documented here.

This project follows semantic versioning once tagged releases begin. Until then, `Unreleased` describes the current `main` branch.

## Unreleased

### Added

- Local Galley CLI with task validation, queueing, daemon execution, profile validation, and PR automation.
- Claude executor flow with structured evidence and supervisor review.
- Claude and Codex supervisor adapters with provider-specific review prompts.
- File-backed daemon queue with run evidence, stale claim handling, graceful shutdown, and conservative worktree cleanup.
- Agent Skill and plugin packaging for Claude Code and Codex.
- MIT license.
- GitHub Release workflow and GoReleaser configuration for prebuilt CLI archives.

### Changed

- `scripts/install.sh` now installs prebuilt GitHub Release assets by default, with `--local` and `--go-install` as explicit alternatives.
