# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Production-readiness scaffolding: `golangci-lint` config, `Makefile`, Dependabot
  config, CI lint / `go vet` / gofmt / `govulncheck` jobs, and community/governance
  files (`CONTRIBUTING`, `SECURITY`, `CODE_OF_CONDUCT`, issue/PR templates,
  `CODEOWNERS`).

### Changed
- CI now runs on pushes to the default branch (previously `pull_request` only).

### Fixed
- Release workflow now derives the Go toolchain from `go.mod` instead of a pinned
  (and stale) version, and uses `actions/checkout@v4`.

### Removed
- Committed `.envrc` containing a developer-specific AWS profile (replaced with
  `.envrc.example`).
