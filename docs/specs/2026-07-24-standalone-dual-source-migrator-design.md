# Production-readiness + Standalone Dual-Source Migrator — Design

**Date:** 2026-07-24
**Status:** Approved design, pre-implementation
**Author:** Jonathan Davenport (with Claude)

## Context

`pulumi-tool-terraform-migrate` began as a fork of `pulumi/pulumi-tool-terraform-migrate`
and has diverged significantly. Beyond the upstream `stack` state-translation command,
the proserv fork (`pulumi-proserv/pulumi-tool-terraform-migrate`) adds a full
import-based migration pipeline — `tf-digest`, `import-id-match`, `patch-state`,
`set-secrets` — and, on the in-flight `feat/cfn-migration-tool` branch, a
substantially complete CDK/CloudFormation front-end (`pkg/cfn`, shared `pkg/importid`,
`digest cfn` / `resolve cfn` subcommands, with tests and golden fixtures).

Two goals:

1. **Production readiness** — make the repo something engineers respect at a glance:
   real CI gating, a linter, security scanning, governance files, and a clean root.
2. **Standalone dual-source repo** — de-fork in place, rebrand to `pulumi-tool-migrate`,
   and package the tool honestly as a Terraform **and** CDK/CloudFormation → Pulumi
   migrator for bridged providers (AWS first), rather than a Terraform-only fork.

This design **supersedes Decision #3** of `2026-07-23-cdk-to-pulumi-migration-design.md`
("same repo, keep the `terraform` name, rename is deferrable churn"). The divergence,
the added CFN front-end, and the standalone goal now justify the rename.

### Core insight

The tool is already source-neutral in substance. Both front-ends run one pipeline:

```
source IaC  ──digest──▶  digest JSON  ──resolve──▶  import IDs  ──▶  pulumi import  ──patch-state──▶  zero-diff
 (TF state /              (agent-safe    (shared         (into the same bridged
  CFN stack)               sidecar)       pkg/importid    pulumi-aws provider)
                                          core, keyed on
                                          Pulumi type token)
```

Terraform and CDK/CloudFormation are two **front-ends** onto one **core**
(`pkg/importid` for ID composition; `patch-state` + `aws-import-diff-fields.json`
for zero-diff; `set-secrets`). The rebrand makes the packaging reflect what the code
already is.

### Synergy worth noting

Project memory records that "CI Go Tests always fail because this is a fork." Fork PRs
cannot access the repo's OIDC role / secrets, so the test job cannot authenticate.
**De-forking (Workstream B, Phase 1) is expected to fix the perennial CI failure** —
the hardening in Workstream A only becomes fully green once the fork relationship is
severed.

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Implement Workstream A quick wins now; defer `cmd/` tests + coverage to the plan | User chose "quick wins." The mechanical hygiene is what stops the sneering; test authoring is a larger, separate effort. |
| 2 | De-fork **in place** (keep the existing repo, history, issues, PRs) | Least disruption; preserves continuity. Sever the GitHub fork relationship rather than migrating to a fresh repo. |
| 3 | Rename to **`pulumi-tool-migrate`**; module path → `github.com/pulumi-proserv/pulumi-tool-migrate` | Source-neutral umbrella; also fixes today's module-path (`pulumi/…`) vs. repo-location (`pulumi-proserv/…`) mismatch. |
| 4 | Sequence: **A ships → CFN branch lands on main → B (de-fork + rename)** | The rename rewrites every import path; doing it before CFN lands would create a brutal conflict on ~3,700 LOC. |
| 5 | Keep `pulumi plugin run terraform-migrate` working via back-compat alias | The shipped TF workspace/component skills invoke the old plugin name; renaming must not break them. |
| 6 | AWS-first, extensibility documented not built | "Bridged providers" beyond AWS (azure/gcp) is a documented extension path (provider-keyed diff-fields + import-ID specs), not v1 scope. |

## Workstream A — Production-readiness quick wins

Implemented now, in the `worktree-prod-readiness-investigation` worktree (off `main`),
as one reviewable PR. All changes are low-risk and mechanical.

### A1. CI / build correctness

- **`release.yml`** — replace `go-version: 1.22.x` with `go-version-file: go.mod`
  (currently mismatched against `go 1.25.0`, a latent release break); bump
  `actions/checkout@v2` → `v4`; pin `goreleaser-action` `version:` to a specific
  release instead of `latest` (reproducible builds).
- **`test.yml`** — add a `push` trigger on the default branch (today it runs on
  `pull_request` only, so nothing gates `main`); add a `go vet ./...` step and a
  `gofmt`/`goimports` diff check.
- **New lint job** — run `golangci-lint` (see A2). May live in `test.yml` or a new
  `lint.yml`; single job, cheap, no cloud creds needed (so it stays green even on
  forks, unlike the integration tests).
- **New `govulncheck` job** — Go-native vulnerability scan; lightweight, no creds.

### A2. Dev + governance scaffolding

- **`.golangci.yml`** — enable `govet`, `staticcheck`, `errcheck`, `ineffassign`,
  `gofmt`, `goimports`, `misspell`, and a license-header check (`goheader`). Start
  with a pragmatic ruleset that passes on the current tree (tighten later); this is
  the single most visible "sneer" gap for a Go repo.
- **`Makefile`** — `build`, `test`, `lint`, `fmt`, `tidy` targets. There is currently
  no local dev entrypoint.
- **`.github/dependabot.yml`** — `gomod` + `github-actions` update ecosystems.
- **Governance files** — `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`,
  `CHANGELOG.md` (Keep a Changelog format), `.github/CODEOWNERS`,
  `.github/ISSUE_TEMPLATE/{bug_report,feature_request}.md` + `config.yml`, and
  `.github/PULL_REQUEST_TEMPLATE.md`. All reflect proserv ownership (not upstream
  `pulumi/`).

### A3. Hygiene cleanups

- Replace committed **`.envrc`** (leaks `AWS_PROFILE=devsandbox` / `AWS_REGION`) with
  `.envrc.example`; add `.envrc` to `.gitignore`.
- Add the missing license header to `pkg/tofu/sensitive_obj_to_cty_path.go`.
- Delete the dead commented-out block at `pkg/convert_tf_value_to_pulumi.go:85-87`
  (`// TODO: fix raw state deltas` + `RawStateInjectDelta`).
- Remove leftover `cobra init` boilerplate: the `toggle` flag in `cmd/root.go:44`.

### A4. Branch protection (cannot be committed — needs repo admin)

Branch protection is a GitHub setting, not a file. Deliverable: a `CODEOWNERS` file
(so reviews can be required) plus a documented ruleset and ready-to-run `gh api`
commands for the user to execute (require PR + 1 approval, require the lint/vet status
checks, disallow force-push and direct pushes to `main`). Recorded in `CONTRIBUTING.md`.

### A5. Deferred to the Workstream B plan (not done now)

- Tests for `cmd/` (the CLI flag-wiring surface is entirely untested).
- Coverage reporting in CI.
- Curating `docs/` (the committed `docs/TODO-output-patching.md` and
  `docs/superpowers/` scratch material read as work-in-progress).

## Workstream B — Standalone dual-source repo (phased plan)

Detailed execution goes to a separate implementation plan (writing-plans). This section
is the phase map and the risks each phase must handle.

### Phase 0 — Land `feat/cfn-migration-tool` on `main`

Merge the substantially complete CFN front-end first, so `main` carries both TF and CFN
before any rename. Gates the rest of Workstream B. (Verify the branch builds and tests
pass, and that AWS SDK v2 services it uses are promoted to direct `go.mod` requires.)

### Phase 1 — De-fork

Sever the GitHub fork relationship on `pulumi-proserv/pulumi-tool-terraform-migrate`
(GitHub requires a support request or repo detach; the network relationship to
`pulumi/pulumi-tool-terraform-migrate` is removed). Keep history, issues, PRs. Expected
side effect: CI can now authenticate on PRs (fixes the standing "CI always fails" note).

### Phase 2 — Rename + module path

- GitHub repo rename `pulumi-tool-terraform-migrate` → `pulumi-tool-migrate` (GitHub
  auto-redirects the old URL).
- Module path `github.com/pulumi/pulumi-tool-terraform-migrate` →
  `github.com/pulumi-proserv/pulumi-tool-migrate`, across `go.mod`, every import, and
  the goreleaser ldflags path (`.goreleaser.yml:24`).
- Binary + goreleaser `project_name`/`binary`/`build.id` → `pulumi-tool-migrate`.
- Cobra root command `Use` (`cmd/root.go:25`) + `Short`/`Long` → source-neutral.
- **Back-compat (Decision #5):** keep the `terraform-migrate` plugin invocation working
  so the shipped skills don't break — via a plugin alias / a documented transition, or
  by coordinating a skill update. This is the highest-risk item and must be explicit in
  the plan.

### Phase 3 — Rebrand docs

Rewrite `README.md` source-neutral ("Migrate Terraform **and** AWS CDK/CloudFormation to
Pulumi"); drop "This is a fork of…" framing. Add an architecture doc showing the
front-end/shared-core pipeline (the diagram above).

### Phase 4 — Light restructure for the dual-source story

Move `aws-import-diff-fields.json` out of repo root into a `data/` (or `assets/`) dir;
confirm the package layout reads as "two front-ends, one core" (largely already true via
`pkg/cfn` + `pkg/importid`). No large refactor — YAGNI.

### Phase 5 — Extensibility framing (AWS-first, documented not built)

Document how a second bridged provider plugs in: provider-keyed `aws-import-diff-fields`
(→ `<provider>-import-diff-fields`) and provider-keyed `pkg/importid` specs. Future-proofs
the "bridged providers" ambition without building it now.

### Phase 6 — Standalone release

First tagged release under the new name; CHANGELOG entry; plugin publishing under the new
identity, with the back-compat alias documented.

## Non-goals / deferred

- C# program output (TypeScript-first, per the CFN design).
- Native resolvers beyond the API Gateway family (per the CFN design).
- Actually building a second (non-AWS) bridged-provider front-end.
- Migrating to a brand-new empty repo (Decision #2 keeps history in place).
- Writing `cmd/` tests and coverage reporting in the Workstream A PR (Decision #1).

## Open questions

None blocking. Phase 1 (de-fork) and Phase 2 (repo rename + branch protection) require
GitHub org-admin actions the tool cannot perform; the plan surfaces the exact commands /
requests for a human with admin rights.
