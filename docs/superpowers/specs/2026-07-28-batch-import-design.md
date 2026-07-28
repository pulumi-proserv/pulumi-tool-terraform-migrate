# Batch import command — design

Date: 2026-07-28
Status: approved, pending implementation plan

## Problem

Importing a migrated stack is currently done with `bb batch-import.bb`, a
babashka script shipped alongside the migration skill. It splits a prepared
import file into batches, replicates every `component: true` entry into each
batch, shells out to `pulumi import`, and supports `--resume`.

Three problems:

1. **It's a second toolchain.** Users of a Go binary must also install babashka.
2. **No failure isolation.** `pulumi import` aborts a whole batch on a single
   malformed import ID. The script reports "batch 3 failed" and stops, so a run
   with five bad IDs takes five fix-and-rerun cycles.
3. **Success detection is string matching.** The script decides an import
   succeeded by looking for `"imported"` in stdout and special-casing the
   cosmetic `parse resource provider reference` error. That is fragile, and a new
   cosmetic error means a new special case.

## Goals

- Replace the script with a first-class command in the tool.
- Isolate failures to individual resources and report all of them in one pass.
- Determine success from stack state, not from output strings.
- Keep the invariants the script got right: components in every batch, resume.

## Non-goals

- Removing the `pulumi` CLI dependency. The Automation API shells out to it.
- Generating import files. That stays `resolve` / `import-id-match`.
- Managing credentials. The command inherits the ambient environment; users wrap
  the invocation in `pulumi env run <env> --` exactly as they do for
  `patch-state`.

## Key constraint: `ImportResources` errors on success

`auto.Stack.ImportResources` builds a temp path for generated code, passes
`--out` **only when code generation is enabled**, then reads that file
unconditionally:

```go
generatedCodePath := filepath.Join(tempDir, "generated_code.txt")
if importOpts.GenerateCode != nil && !*importOpts.GenerateCode {
    args = append(args, "--generate-code=false")   // no --out
} else {
    args = append(args, "--out", generatedCodePath)
}
// ...
generatedCode, err := os.ReadFile(generatedCodePath)   // ENOENT
if err != nil {
    return res, fmt.Errorf("failed to read generated code: %w", err)
}
```

Verified present in pulumi/sdk/v3 v3.222.0 (pinned), v3.233.0, and v3.246.0.

Both migration workflows *require* `--generate-code=false` — shipping generated
code defeats the hand-authored deliverable — so every batch we run returns a
non-nil error regardless of outcome.

**Consequence for the design:** the error returned by `ImportResources` is not a
reliable success signal, and neither is its output. We verify against stack
state instead. This also disposes of the cosmetic `parse resource provider
reference` error without a special case. Errors are still captured verbatim and
surfaced in the failure report, but they never *decide* anything.

This should be reported upstream against `pulumi/pulumi`; the workaround is
independent of whether it is fixed.

## Architecture

`pkg/batchimport` holds the orchestration; `cmd/import.go` is a thin cobra
wrapper. This mirrors the existing split — `pkg/import_filler.go`,
`pkg/state_patcher.go` etc. carry the logic and the tests, `cmd/*.go` parse flags.

### Interface

```go
// Importer is the seam that lets the orchestration be tested without a stack.
type Importer interface {
    ImportBatch(ctx context.Context, rs []*optimport.ImportResource, nameTable map[string]string) error
    ExistingResources(ctx context.Context) (map[ResourceKey]bool, error)
}

// ResourceKey identifies a resource independently of its URN's stack/project.
type ResourceKey struct {
    Type string // leaf type token, e.g. "aws:s3/bucket:Bucket"
    Name string // resource name, the URN's last segment
}

type Failure struct {
    Key ResourceKey
    ID  string // the import ID that was attempted
    Err string // verbatim error text, for the report
}

type Result struct {
    Imported []ResourceKey
    Skipped  []ResourceKey // already in state (resume)
    Failed   []Failure
}

type Options struct {
    BatchSize int  // default 100
    Resume    bool // default true
    DryRun    bool
}

// ImportFile is batchimport's own file model, deliberately NOT pkg.ImportFile.
type ImportFile struct {
    NameTable map[string]string           `json:"nameTable,omitempty"`
    Resources []*optimport.ImportResource `json:"resources"`
}

func Run(ctx context.Context, imp Importer, file *ImportFile, opts Options) (*Result, error)
```

**Why a separate file model.** `pkg.ImportEntry` carries seven fields (`type`,
`name`, `id`, `parent`, `provider`, `component`, `version`) while
`optimport.ImportResource` carries eleven — it adds `logicalName`,
`pluginDownloadUrl`, `properties`, and `remote`. Unmarshalling a user's import
file through `pkg.ImportFile` would silently drop those four on any file that
uses them, and `properties` in particular changes what gets imported. Decoding
straight into `optimport.ImportResource` is lossless and removes a conversion
step. `pkg.ImportFile` stays as-is for `import-id-match`, which only needs to
fill IDs.

The production `Importer` wraps an `auto.Stack` obtained from
`auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))` + `auto.SelectStack`.
`ImportBatch` calls `ImportResources` with `Resources`, `NameTable`,
`Protect(false)`, and `GenerateCode(false)`. `ExistingResources` calls
`stack.Export`, unmarshals the deployment, and parses each resource URN into a
`ResourceKey`.

`optimport.ImportResource`'s JSON tags match the import-file entry schema exactly
(`id`, `type`, `name`, `logicalName`, `parent`, `provider`, `version`,
`pluginDownloadUrl`, `properties`, `component`, `remote`), so the file decodes
straight into the type `ImportResources` already expects — no translation layer,
and nothing dropped.

### URN parsing

A URN is `urn:pulumi:<stack>::<project>::<typePath>::<name>`, where `<typePath>`
is `parentType$childType` for parented resources. `ResourceKey` takes the segment
after the final `$` as the type and the final `::` segment as the name. Matching
on **type + name** rather than name alone is deliberate: the CDK migration
guidance has four Pulumi resources (bucket, SSE config, public access block,
ownership controls) deriving names from one CloudFormation logical ID, and
name-only matching conflates them.

## Algorithm

```
entries        := file.Resources
components     := entries where Component == true
importable     := entries where Component == false

existing       := imp.ExistingResources(ctx)
if opts.Resume:
    skipped    := importable ∩ existing
    importable := importable \ existing

if opts.DryRun:
    print plan (batch count, sizes, skipped) and return

for each batch of opts.BatchSize from importable:
    payload := components ++ batch          // components in EVERY batch
    err     := imp.ImportBatch(ctx, payload, file.NameTable)   // err is NOT a verdict

    after   := imp.ExistingResources(ctx)
    landed  := batch ∩ after
    missing := batch \ after

    record landed as Imported
    if missing is empty:
        continue                            // success, whatever err said

    for each r in missing:                  // isolate
        errR  := imp.ImportBatch(ctx, components ++ [r], file.NameTable)
        after := imp.ExistingResources(ctx)
        if r ∈ after: record Imported
        else:         record Failure{r, r.ID, errR}
```

Components are replicated into every batch — including every single-resource
isolation call — because `pulumi import` resolves `parent` references through the
`nameTable`, and a child whose parent is absent fails with
`has no entry in 'nameTable'`. This is an invariant, not a flag.

The isolation pass is quadratic in the worst case (a batch where every resource
fails costs `BatchSize` extra CLI invocations). That is the accepted trade: a
wholly-failing batch is pathological, and the diagnostic value of knowing exactly
which IDs are bad outweighs the wall-clock cost of a run that was going to fail
anyway.

## Resume

On by default; `--no-resume` disables it. Resume is a pre-filter — anything
already in state is dropped from the work list and reported as `Skipped`. Because
success is decided by state membership, resume and verification share one
mechanism.

This is stricter than the script, which matched on name alone.

## CLI surface

```
pulumi-tool-terraform-migrate import \
  --file imports-ready.json \
  --project-dir . \
  --stack <stack-name> \
  [--batch-size 100] \
  [--no-resume] \
  [--dry-run]
```

Provider-agnostic — no `cfn` / `tf` subcommands, because an import file is an
import file regardless of which `resolve` produced it. `Protect(false)` and
`GenerateCode(false)` are always set; they are requirements of the workflow, not
user choices.

Output: per-batch progress to stderr, then a summary — counts for
imported/skipped/failed, and a table of failures (`name`, `type`, `id`, error).
Exit code 1 if any resource failed, 0 otherwise.

## Testing

Unit tests against a fake `Importer` whose `ImportBatch` fails for a designated
set of keys and whose `ExistingResources` accumulates the keys that landed —
letting the whole algorithm run with no cloud, no stack, and no CLI:

1. **Clean run** — every resource imports; one batch per `BatchSize`.
2. **SDK-bug tolerance** — `ImportBatch` returns an error while all resources
   land. Must report success and zero failures. This is the regression test for
   the constraint above.
3. **Isolation** — a batch where one resource fails: the other resources are
   recorded imported, the bad one appears in `Failed` with its ID and error, and
   the run continues to later batches.
4. **Whole-batch failure** — nothing lands; every resource is isolated and
   reported.
5. **Components in every batch** — assert each `ImportBatch` payload contains all
   component entries, including in isolation calls.
6. **Resume** — pre-populated state causes those resources to be skipped, not
   re-imported; `--no-resume` imports them anyway.
7. **Type+name disambiguation** — two resources sharing a name but differing in
   type are tracked independently.
8. **Dry run** — no `ImportBatch` calls at all.

URN parsing gets its own table test, covering parented (`parent$child`) and
bare types, and names containing `::`-adjacent characters such as
`vpc-public[0]`.

No integration test against a live stack; the `auto.Stack` adapter is thin
enough that its risk is the SDK's, which case 2 pins.

## Rollout

1. Implement `pkg/batchimport` + `cmd/import.go` with the tests above.
2. Delete `skills/pulumi-terraform-workspace-migration/scripts/batch-import.bb`
   and the now-empty `scripts/` directory.
3. Update `references/import-mechanics.md` to document the command in place of
   the script, and the SKILL.md "Bundled scripts" section (which then describes
   no scripts and can be removed).
4. Update the README skills table, which currently says the skill bundles
   `batch-import.bb`.
5. Add the command to the README's command list alongside `tf-digest`,
   `import-id-match`, `patch-state`, and `set-secrets`.

## Open items

- File the upstream `ImportResources` issue against `pulumi/pulumi`.
