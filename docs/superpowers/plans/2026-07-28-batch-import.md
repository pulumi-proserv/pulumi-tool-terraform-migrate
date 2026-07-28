# Batch Import Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Replace the `batch-import.bb` babashka script with a `pulumi-tool-terraform-migrate import` command that batches a prepared import file, isolates per-resource failures, and reports every bad import ID in a single pass.

**Architecture:** A `pkg/batchimport` package holds all orchestration behind an `Importer` interface, so batching, resume, and failure isolation are unit-testable with a fake and no cloud access. A thin `cmd/import.go` parses flags, builds the real `auto.Stack`-backed `Importer`, and formats the report. Success is decided by reading stack state after each import, never by inspecting the error or output of `ImportResources`.

**Tech Stack:** Go 1.25.0, `github.com/pulumi/pulumi/sdk/v3` v3.222.0 (`go/auto`, `go/auto/optimport`), `github.com/spf13/cobra`, `github.com/stretchr/testify`.

**Spec:** `docs/superpowers/specs/2026-07-28-batch-import-design.md`

## Global Constraints

- **The error from `ImportResources` is never a verdict.** In SDK v3.222.0 it returns `failed to read generated code: ... no such file or directory` after a *successful* import whenever `GenerateCode(false)` is set. Success and failure are determined solely by whether the resource is present in stack state afterwards. Errors are captured for the report only.
- **Every `ImportBatch` payload includes every component entry**, including single-resource isolation calls. `pulumi import` resolves `parent` through the `nameTable`; a child whose parent is missing fails with `has no entry in 'nameTable'`.
- **Resources are matched on type + name, never name alone.** Four Pulumi resources can share one source logical ID.
- **Always set `Protect(false)` and `GenerateCode(false)`.** These are workflow requirements, not user options — no flags for them.
- **Every new `.go` file starts with the repo's license header**, copied verbatim from `pkg/import_filler.go` lines 1-13 (`// Copyright 2016-2025, Pulumi Corporation.` followed by the Apache 2.0 block).
- **Unit tests use `testify` (`assert`/`require`) and call `t.Parallel()`**, matching `pkg/import_filler_test.go`.
- Default batch size is **100**.

---

### Task 1: ResourceKey and URN parsing

**Files:**
- Create: `pkg/batchimport/key.go`
- Test: `pkg/batchimport/key_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type ResourceKey struct { Type, Name string }`; `func ParseURN(urn string) (ResourceKey, bool)`.

- [x] **Step 1: Write the failing test**

Create `pkg/batchimport/key_test.go` (license header first):

```go
package batchimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseURN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		urn      string
		expected ResourceKey
		ok       bool
	}{
		{
			name:     "bare resource",
			urn:      "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
			expected: ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "my-bucket"},
			ok:       true,
		},
		{
			name:     "component child takes the leaf type",
			urn:      "urn:pulumi:dev::proj::example:index:Vpc$aws:ec2/vpc:Vpc::vpc-main",
			expected: ResourceKey{Type: "aws:ec2/vpc:Vpc", Name: "vpc-main"},
			ok:       true,
		},
		{
			name:     "nested components take the final leaf type",
			urn:      "urn:pulumi:dev::proj::a:index:A$b:index:B$aws:s3/bucket:Bucket::b1",
			expected: ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b1"},
			ok:       true,
		},
		{
			name:     "name containing brackets",
			urn:      `urn:pulumi:dev::proj::aws:ec2/subnet:Subnet::vpc-public[0]`,
			expected: ResourceKey{Type: "aws:ec2/subnet:Subnet", Name: "vpc-public[0]"},
			ok:       true,
		},
		{
			name: "stack resource is still parseable",
			urn:  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
			expected: ResourceKey{
				Type: "pulumi:pulumi:Stack",
				Name: "proj-dev",
			},
			ok: true,
		},
		{name: "too few segments", urn: "urn:pulumi:dev::proj", ok: false},
		{name: "empty", urn: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseURN(tt.urn)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.expected, got)
			}
		})
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run TestParseURN -v`
Expected: FAIL — build error, `undefined: ResourceKey` and `undefined: ParseURN`.

- [x] **Step 3: Write minimal implementation**

Create `pkg/batchimport/key.go` (license header first):

```go
// Package batchimport orchestrates importing a prepared Pulumi import file in
// batches, isolating per-resource failures so a single bad import ID does not
// hide the outcome of the rest of the batch.
package batchimport

import "strings"

// ResourceKey identifies a resource independently of the stack and project
// segments of its URN. Resources are matched on type AND name: several Pulumi
// resources can legitimately share a name when they derive it from one source
// logical ID, so name alone conflates them.
type ResourceKey struct {
	Type string
	Name string
}

// ParseURN extracts the leaf type and name from a Pulumi URN of the form
// urn:pulumi:<stack>::<project>::<typePath>::<name>, where <typePath> is
// parentType$childType for parented resources. It reports false for a URN with
// too few segments.
func ParseURN(urn string) (ResourceKey, bool) {
	parts := strings.SplitN(urn, "::", 4)
	if len(parts) < 4 {
		return ResourceKey{}, false
	}
	typePath, name := parts[2], parts[3]
	if i := strings.LastIndex(typePath, "$"); i >= 0 {
		typePath = typePath[i+1:]
	}
	if typePath == "" || name == "" {
		return ResourceKey{}, false
	}
	return ResourceKey{Type: typePath, Name: name}, true
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/batchimport/ -run TestParseURN -v`
Expected: PASS, 7 subtests.

- [x] **Step 5: Commit**

```bash
git add pkg/batchimport/key.go pkg/batchimport/key_test.go
git commit -m "feat(batchimport): add ResourceKey and URN parsing"
```

---

### Task 2: Import file model

**Files:**
- Create: `pkg/batchimport/file.go`
- Test: `pkg/batchimport/file_test.go`

**Interfaces:**
- Consumes: `ResourceKey` from Task 1.
- Produces: `type ImportFile struct { NameTable map[string]string; Resources []*optimport.ImportResource }`; `func LoadImportFile(path string) (*ImportFile, error)`; `func keyOf(r *optimport.ImportResource) ResourceKey`.

**Why not `pkg.ImportFile`:** `pkg.ImportEntry` has 7 fields; `optimport.ImportResource` has 11, adding `logicalName`, `pluginDownloadUrl`, `properties`, and `remote`. Decoding through `pkg.ImportFile` would silently drop those. `pkg.ImportFile` stays untouched for `import-id-match`.

- [x] **Step 1: Write the failing test**

Create `pkg/batchimport/file_test.go` (license header first):

```go
package batchimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadImportFile_PreservesAllFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "imports.json")
	content := `{
  "nameTable": {"vpc": "urn:pulumi:dev::proj::example:index:Vpc::vpc"},
  "resources": [
    {"type": "example:index:Vpc", "name": "vpc", "component": true},
    {"type": "aws:ec2/vpc:Vpc", "name": "vpc-main", "id": "vpc-abc123",
     "parent": "vpc", "logicalName": "mainVpc", "properties": ["cidrBlock"],
     "version": "7.0.0", "pluginDownloadUrl": "https://example.invalid",
     "remote": true}
  ]
}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	f, err := LoadImportFile(path)
	require.NoError(t, err)

	require.Len(t, f.Resources, 2)
	assert.Equal(t, map[string]string{
		"vpc": "urn:pulumi:dev::proj::example:index:Vpc::vpc",
	}, f.NameTable)

	assert.True(t, f.Resources[0].Component)

	r := f.Resources[1]
	assert.Equal(t, "vpc-abc123", r.ID)
	assert.Equal(t, "vpc", r.Parent)
	assert.Equal(t, "mainVpc", r.LogicalName)
	assert.Equal(t, []string{"cidrBlock"}, r.Properties)
	assert.Equal(t, "7.0.0", r.Version)
	assert.Equal(t, "https://example.invalid", r.PluginDownloadURL)
	assert.True(t, r.Remote)
}

func TestLoadImportFile_Errors(t *testing.T) {
	t.Parallel()

	_, err := LoadImportFile(filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err)

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	_, err = LoadImportFile(bad)
	require.ErrorContains(t, err, "parsing import file")
}

func TestKeyOf(t *testing.T) {
	t.Parallel()

	k := keyOf(&optimport.ImportResource{Type: "aws:s3/bucket:Bucket", Name: "b"})
	assert.Equal(t, ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b"}, k)
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run 'TestLoadImportFile|TestKeyOf' -v`
Expected: FAIL — `undefined: LoadImportFile`, `undefined: keyOf`.

- [x] **Step 3: Write minimal implementation**

Create `pkg/batchimport/file.go` (license header first):

```go
package batchimport

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// ImportFile is the on-disk Pulumi import file. Resources decode directly into
// optimport.ImportResource — the type ImportResources consumes — so no field is
// lost in translation.
type ImportFile struct {
	NameTable map[string]string           `json:"nameTable,omitempty"`
	Resources []*optimport.ImportResource `json:"resources"`
}

// LoadImportFile reads and decodes a prepared import file.
func LoadImportFile(path string) (*ImportFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading import file: %w", err)
	}
	var f ImportFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing import file: %w", err)
	}
	return &f, nil
}

// keyOf builds the ResourceKey identifying an import entry.
func keyOf(r *optimport.ImportResource) ResourceKey {
	return ResourceKey{Type: r.Type, Name: r.Name}
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/batchimport/ -run 'TestLoadImportFile|TestKeyOf' -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add pkg/batchimport/file.go pkg/batchimport/file_test.go
git commit -m "feat(batchimport): add lossless import file model"
```

---

### Task 3: Importer interface, fake, and the happy-path batching loop

**Files:**
- Create: `pkg/batchimport/run.go`
- Create: `pkg/batchimport/fake_test.go`
- Test: `pkg/batchimport/run_test.go`

**Interfaces:**
- Consumes: `ResourceKey`, `ParseURN` (Task 1); `ImportFile`, `keyOf` (Task 2).
- Produces: `type Importer interface`; `type Failure struct`; `type Result struct`; `type Options struct`; `const DefaultBatchSize = 100`; `func Run(ctx context.Context, imp Importer, file *ImportFile, opts Options) (*Result, error)`; and the test-only `fakeImporter`.

- [x] **Step 1: Write the fake**

Create `pkg/batchimport/fake_test.go` (license header first):

```go
package batchimport

import (
	"context"
	"errors"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// fakeImporter simulates pulumi import against an in-memory state.
//
// failKeys are resources that never land. batchErr, when set, is returned from
// every ImportBatch call regardless of outcome — this models SDK v3.222.0
// returning "failed to read generated code" after a successful import.
type fakeImporter struct {
	state    map[ResourceKey]bool
	failKeys map[ResourceKey]bool
	batchErr error

	// recorded for assertions
	payloads  [][]*optimport.ImportResource
	nameTable map[string]string
	callCount int
}

func newFakeImporter() *fakeImporter {
	return &fakeImporter{
		state:    map[ResourceKey]bool{},
		failKeys: map[ResourceKey]bool{},
	}
}

func (f *fakeImporter) ImportBatch(
	_ context.Context,
	rs []*optimport.ImportResource,
	nameTable map[string]string,
) error {
	f.callCount++
	f.payloads = append(f.payloads, rs)
	f.nameTable = nameTable

	failed := false
	for _, r := range rs {
		if r.Component {
			continue
		}
		if f.failKeys[keyOf(r)] {
			failed = true
			continue
		}
		f.state[keyOf(r)] = true
	}
	if f.batchErr != nil {
		return f.batchErr
	}
	if failed {
		return errors.New("import failed: resource does not exist")
	}
	return nil
}

func (f *fakeImporter) ExistingResources(_ context.Context) (map[ResourceKey]bool, error) {
	out := make(map[ResourceKey]bool, len(f.state))
	for k, v := range f.state {
		out[k] = v
	}
	return out, nil
}

// nonComponentPayloads returns each recorded payload's non-component entries.
func (f *fakeImporter) nonComponentPayloads() [][]ResourceKey {
	var out [][]ResourceKey
	for _, p := range f.payloads {
		var keys []ResourceKey
		for _, r := range p {
			if !r.Component {
				keys = append(keys, keyOf(r))
			}
		}
		out = append(out, keys)
	}
	return out
}
```

Note: the fake writes resources to state even on a partially failing batch. Real `pulumi import` is closer to all-or-nothing per batch, but modelling partial success is strictly harder for `Run` to handle correctly, and Task 5 covers the all-or-nothing case explicitly.

- [x] **Step 2: Write the failing test**

Create `pkg/batchimport/run_test.go` (license header first):

```go
package batchimport

import (
	"context"
	"fmt"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFile builds an import file with one component and n bare resources named
// res-0..res-(n-1).
func testFile(n int) *ImportFile {
	f := &ImportFile{
		NameTable: map[string]string{"comp": "urn:pulumi:dev::proj::example:index:Comp::comp"},
		Resources: []*optimport.ImportResource{
			{Type: "example:index:Comp", Name: "comp", Component: true},
		},
	}
	for i := 0; i < n; i++ {
		f.Resources = append(f.Resources, &optimport.ImportResource{
			Type:   "aws:s3/bucket:Bucket",
			Name:   fmt.Sprintf("res-%d", i),
			ID:     fmt.Sprintf("id-%d", i),
			Parent: "comp",
		})
	}
	return f
}

func TestRun_CleanRun(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 5)
	assert.Empty(t, res.Failed)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, 3, res.BatchCount)
	assert.Equal(t, 3, f.callCount, "one ImportBatch per batch, no isolation")
}

func TestRun_ComponentsInEveryBatch(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	_, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	require.Len(t, f.payloads, 3)
	for i, p := range f.payloads {
		var components int
		for _, r := range p {
			if r.Component {
				components++
			}
		}
		assert.Equal(t, 1, components, "batch %d must carry every component entry", i)
	}
	assert.Equal(t, map[string]string{
		"comp": "urn:pulumi:dev::proj::example:index:Comp::comp",
	}, f.nameTable)
}

func TestRun_BatchSizeDefaults(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(3), Options{Resume: true})
	require.NoError(t, err)

	assert.Equal(t, 1, res.BatchCount, "3 resources fit one default-size batch")
	assert.Equal(t, 1, f.callCount)
}
```

- [x] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run TestRun -v`
Expected: FAIL — `undefined: Run`, `undefined: Options`.

- [x] **Step 4: Write minimal implementation**

Create `pkg/batchimport/run.go` (license header first):

```go
package batchimport

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// DefaultBatchSize is the number of importable resources per batch.
const DefaultBatchSize = 100

// Importer performs imports and reports what is currently in stack state.
type Importer interface {
	// ImportBatch imports the given resources. Its error is diagnostic only:
	// callers MUST NOT treat it as the verdict on whether resources landed.
	ImportBatch(ctx context.Context, rs []*optimport.ImportResource, nameTable map[string]string) error
	// ExistingResources returns the resources currently in stack state.
	ExistingResources(ctx context.Context) (map[ResourceKey]bool, error)
}

// Failure is a resource that was not in state after being imported alone.
type Failure struct {
	Key ResourceKey
	ID  string
	Err string
}

// Result summarizes a run.
type Result struct {
	Imported   []ResourceKey
	Skipped    []ResourceKey
	Failed     []Failure
	Planned    []ResourceKey
	BatchCount int
}

// Options configures a run.
type Options struct {
	BatchSize int
	Resume    bool
	DryRun    bool
}

// Run imports file's resources in batches.
//
// Whether a resource imported successfully is determined by reading stack state
// afterwards, never by the error returned from ImportBatch: in pulumi/sdk
// v3.222.0, ImportResources returns an error after a *successful* import
// whenever code generation is disabled, which this tool always disables.
func Run(ctx context.Context, imp Importer, file *ImportFile, opts Options) (*Result, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}

	res := &Result{}

	var components, importable []*optimport.ImportResource
	for _, r := range file.Resources {
		if r.Component {
			components = append(components, r)
			continue
		}
		importable = append(importable, r)
	}

	for _, r := range importable {
		res.Planned = append(res.Planned, keyOf(r))
	}
	res.BatchCount = (len(importable) + opts.BatchSize - 1) / opts.BatchSize

	for start := 0; start < len(importable); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(importable))
		batch := importable[start:end]

		// The error is explicitly discarded: it is not evidence either way.
		// Task 5 captures it for the failure report.
		_ = imp.ImportBatch(ctx, withComponents(components, batch...), file.NameTable)

		after, err := imp.ExistingResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading stack state after batch: %w", err)
		}

		for _, r := range batch {
			if after[keyOf(r)] {
				res.Imported = append(res.Imported, keyOf(r))
			}
		}
	}

	return res, nil
}

// withComponents returns every component entry followed by the given resources.
// Components must accompany every import call so children resolve their parent
// through the nameTable.
func withComponents(
	components []*optimport.ImportResource,
	rs ...*optimport.ImportResource,
) []*optimport.ImportResource {
	out := make([]*optimport.ImportResource, 0, len(components)+len(rs))
	out = append(out, components...)
	out = append(out, rs...)
	return out
}
```

- [x] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/batchimport/ -run TestRun -v`
Expected: PASS — 3 tests.

- [x] **Step 6: Commit**

```bash
git add pkg/batchimport/run.go pkg/batchimport/run_test.go pkg/batchimport/fake_test.go
git commit -m "feat(batchimport): add batching loop with state-verified success"
```

---

### Task 4: Resume and dry run

**Files:**
- Modify: `pkg/batchimport/run.go`
- Test: `pkg/batchimport/run_test.go`

**Interfaces:**
- Consumes: everything from Task 3.
- Produces: no new exported names; `Options.Resume` and `Options.DryRun` become functional, `Result.Skipped` becomes populated.

- [x] **Step 1: Write the failing test**

Append to `pkg/batchimport/run_test.go`:

```go
func TestRun_ResumeSkipsResourcesAlreadyInState(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-2"}] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-0"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-2"},
	}, res.Skipped)
	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-3"},
	}, res.Imported)

	require.Len(t, f.nonComponentPayloads(), 1)
	assert.ElementsMatch(t, []ResourceKey{
		{Type: "aws:s3/bucket:Bucket", Name: "res-1"},
		{Type: "aws:s3/bucket:Bucket", Name: "res-3"},
	}, f.nonComponentPayloads()[0], "skipped resources must not be re-imported")
}

func TestRun_NoResumeImportsEverything(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(2), Options{BatchSize: 10, Resume: false})
	require.NoError(t, err)

	assert.Empty(t, res.Skipped)
	assert.Len(t, res.Imported, 2)
	assert.Len(t, f.nonComponentPayloads()[0], 2)
}

func TestRun_ResumeDistinguishesSameNameDifferentType(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "shared"}] = true

	file := &ImportFile{Resources: []*optimport.ImportResource{
		{Type: "aws:s3/bucket:Bucket", Name: "shared", ID: "b"},
		{Type: "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", Name: "shared", ID: "p"},
	}}

	res, err := Run(context.Background(), f, file, Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	assert.Equal(t, []ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "shared"}}, res.Skipped)
	assert.Equal(t, []ResourceKey{
		{Type: "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", Name: "shared"},
	}, res.Imported)
}

func TestRun_DryRunImportsNothing(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.state[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(3), Options{BatchSize: 2, Resume: true, DryRun: true})
	require.NoError(t, err)

	assert.Equal(t, 0, f.callCount, "dry run must not import")
	assert.Len(t, res.Planned, 2, "res-0 is already in state")
	assert.Equal(t, 1, res.BatchCount)
	assert.Empty(t, res.Imported)
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run TestRun -v`
Expected: FAIL — `TestRun_ResumeSkipsResourcesAlreadyInState` fails with `res.Skipped` empty; `TestRun_DryRunImportsNothing` fails with `f.callCount` = 1.

- [x] **Step 3: Write the implementation**

In `pkg/batchimport/run.go`, replace the block from `for _, r := range importable {` (the `res.Planned` loop) through `res.BatchCount = ...` with:

```go
	existing, err := imp.ExistingResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading stack state: %w", err)
	}

	if opts.Resume {
		remaining := importable[:0:0]
		for _, r := range importable {
			if existing[keyOf(r)] {
				res.Skipped = append(res.Skipped, keyOf(r))
				continue
			}
			remaining = append(remaining, r)
		}
		importable = remaining
	}

	for _, r := range importable {
		res.Planned = append(res.Planned, keyOf(r))
	}
	res.BatchCount = (len(importable) + opts.BatchSize - 1) / opts.BatchSize

	if opts.DryRun {
		return res, nil
	}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/batchimport/ -v`
Expected: PASS — all tests from Tasks 1-4.

- [x] **Step 5: Commit**

```bash
git add pkg/batchimport/run.go pkg/batchimport/run_test.go
git commit -m "feat(batchimport): add resume filtering and dry run"
```

---

### Task 5: Failure isolation and SDK error tolerance

**Files:**
- Modify: `pkg/batchimport/run.go`
- Test: `pkg/batchimport/run_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3-4.
- Produces: `Result.Failed` becomes populated; `func errText(isolationErr, batchErr error) string`.

- [x] **Step 1: Write the failing test**

Append to `pkg/batchimport/run_test.go`:

```go
// The SDK returns an error after a successful --generate-code=false import.
// State, not the error, decides the outcome.
func TestRun_SucceedsDespiteImportBatchError(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.batchErr = errors.New(
		"failed to read generated code: open /tmp/pulumi-import-1/generated_code.txt: no such file or directory")

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 4)
	assert.Empty(t, res.Failed)
	assert.Equal(t, 2, f.callCount, "no isolation pass when everything landed")
}

func TestRun_IsolatesTheFailingResource(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	bad := ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-1"}
	f.failKeys[bad] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 4, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 3)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, bad, res.Failed[0].Key)
	assert.Equal(t, "id-1", res.Failed[0].ID)
	assert.Contains(t, res.Failed[0].Err, "resource does not exist")

	assert.Equal(t, 2, f.callCount, "one batch call plus one isolation call")
	assert.Equal(t, []ResourceKey{bad}, f.nonComponentPayloads()[1],
		"isolation call imports exactly the missing resource")
}

func TestRun_IsolationCallsCarryComponents(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	_, err := Run(context.Background(), f, testFile(1), Options{BatchSize: 10, Resume: true})
	require.NoError(t, err)

	require.Len(t, f.payloads, 2)
	var components int
	for _, r := range f.payloads[1] {
		if r.Component {
			components++
		}
	}
	assert.Equal(t, 1, components, "isolation call must still carry components")
}

func TestRun_WholeBatchFailsAndEveryResourceIsReported(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	for i := 0; i < 3; i++ {
		f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: fmt.Sprintf("res-%d", i)}] = true
	}

	res, err := Run(context.Background(), f, testFile(3), Options{BatchSize: 3, Resume: true})
	require.NoError(t, err)

	assert.Empty(t, res.Imported)
	assert.Len(t, res.Failed, 3)
	assert.Equal(t, 4, f.callCount, "one batch call plus three isolation calls")
}

func TestRun_ContinuesToLaterBatchesAfterAFailure(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	f.failKeys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "res-0"}] = true

	res, err := Run(context.Background(), f, testFile(4), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Failed, 1)
	assert.Len(t, res.Imported, 3, "batches after the failure still run")
}

func TestErrText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "boom", errText(errors.New("boom"), errors.New("batch")))
	assert.Equal(t, "batch", errText(nil, errors.New("batch")))
	assert.Equal(t, "resource not present in stack state after import", errText(nil, nil))
}
```

Add `"errors"` to the test file's import block.

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run 'TestRun_Isolates|TestRun_Succeeds|TestErrText' -v`
Expected: FAIL — `undefined: errText`, and `res.Failed` empty in the isolation tests.

- [x] **Step 3: Write the implementation**

In `pkg/batchimport/run.go`, replace the batch body — from the `_ = imp.ImportBatch(...)` line through the closing brace of the `for _, r := range batch` loop — with:

```go
		// The error is not a verdict; it is captured only for the report.
		batchErr := imp.ImportBatch(ctx, withComponents(components, batch...), file.NameTable)

		after, err := imp.ExistingResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading stack state after batch: %w", err)
		}

		var missing []*optimport.ImportResource
		for _, r := range batch {
			if after[keyOf(r)] {
				res.Imported = append(res.Imported, keyOf(r))
				continue
			}
			missing = append(missing, r)
		}

		// Import each missing resource alone, then read state ONCE. An
		// isolation payload is components + one resource, so only that
		// resource can newly land — a per-resource read would give the same
		// verdicts at O(batch size) subprocess calls.
		isoErrs := make(map[ResourceKey]error, len(missing))
		for _, r := range missing {
			isoErrs[keyOf(r)] = imp.ImportBatch(ctx, withComponents(components, r), file.NameTable)
		}

		afterIso, err := imp.ExistingResources(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading stack state after isolating failures: %w", err)
		}

		for _, r := range missing {
			if afterIso[keyOf(r)] {
				res.Imported = append(res.Imported, keyOf(r))
				continue
			}
			res.Failed = append(res.Failed, Failure{
				Key: keyOf(r),
				ID:  r.ID,
				Err: errText(isoErrs[keyOf(r)], batchErr),
			})
		}
```

Then append to the same file:

```go
// errText describes why a resource is reported as failed. The state fact leads:
// an import error is unreliable evidence — the SDK returns one even after a
// successful import when code generation is disabled — so it is attached only
// as context.
func errText(isolationErr, batchErr error) string {
	const base = "resource not present in stack state after import"
	switch {
	case isolationErr != nil:
		return fmt.Sprintf("%s (last error: %s)", base, isolationErr)
	case batchErr != nil:
		return fmt.Sprintf("%s (last error: %s)", base, batchErr)
	default:
		return base
	}
}
```

- [x] **Step 4: Run the full package test suite**

Run: `go test ./pkg/batchimport/ -v`
Expected: PASS — every test from Tasks 1-5.

- [x] **Step 5: Commit**

```bash
git add pkg/batchimport/run.go pkg/batchimport/run_test.go
git commit -m "feat(batchimport): isolate per-resource failures and tolerate SDK error"
```

---

### Task 6: The `auto.Stack` adapter

**Files:**
- Create: `pkg/batchimport/stack.go`
- Test: `pkg/batchimport/stack_test.go`

**Interfaces:**
- Consumes: `Importer`, `ResourceKey`, `ParseURN`.
- Produces: `func NewStackImporter(ctx context.Context, projectDir, stackName string) (Importer, error)`; `func resourceKeysFromDeployment(raw []byte) (map[ResourceKey]bool, error)`.

The adapter itself is thin and needs a live stack, so only its deployment parsing is unit-tested. Note the shape: `stack.Export` returns an `apitype.UntypedDeployment` whose `.Deployment` field is *already* the inner object (`{"resources": [...]}`) — unlike an exported state *file*, which wraps it in `{"deployment": {...}}`.

- [x] **Step 1: Write the failing test**

Create `pkg/batchimport/stack_test.go` (license header first):

```go
package batchimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceKeysFromDeployment(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"resources":[
		{"urn":"urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev","type":"pulumi:pulumi:Stack"},
		{"urn":"urn:pulumi:dev::proj::example:index:Comp$aws:s3/bucket:Bucket::comp-b1","type":"aws:s3/bucket:Bucket"},
		{"urn":"urn:pulumi:dev::proj::aws:ec2/vpc:Vpc::vpc-main","type":"aws:ec2/vpc:Vpc"}
	]}`)

	keys, err := resourceKeysFromDeployment(raw)
	require.NoError(t, err)

	assert.True(t, keys[ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "comp-b1"}])
	assert.True(t, keys[ResourceKey{Type: "aws:ec2/vpc:Vpc", Name: "vpc-main"}])
	assert.True(t, keys[ResourceKey{Type: "pulumi:pulumi:Stack", Name: "proj-dev"}])
	assert.Len(t, keys, 3)
}

func TestResourceKeysFromDeployment_EmptyAndInvalid(t *testing.T) {
	t.Parallel()

	keys, err := resourceKeysFromDeployment([]byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, keys)

	keys, err = resourceKeysFromDeployment(nil)
	require.NoError(t, err)
	assert.Empty(t, keys)

	_, err = resourceKeysFromDeployment([]byte(`{"resources": "nope"}`))
	require.Error(t, err)
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/batchimport/ -run TestResourceKeysFromDeployment -v`
Expected: FAIL — `undefined: resourceKeysFromDeployment`.

- [x] **Step 3: Write the implementation**

Create `pkg/batchimport/stack.go` (license header first):

```go
package batchimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// stackImporter is the production Importer, backed by the Automation API.
type stackImporter struct {
	stack auto.Stack
}

// NewStackImporter selects an existing stack in projectDir.
func NewStackImporter(ctx context.Context, projectDir, stackName string) (Importer, error) {
	ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(projectDir))
	if err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}
	s, err := auto.SelectStack(ctx, stackName, ws)
	if err != nil {
		return nil, fmt.Errorf("selecting stack %q: %w", stackName, err)
	}
	return &stackImporter{stack: s}, nil
}

// ImportBatch runs pulumi import for the given resources.
//
// Protect and GenerateCode are always disabled: the migration workflow
// hand-authors its program, and protected resources produce a spurious
// ~protect diff. The returned error is diagnostic only — see Run.
func (s *stackImporter) ImportBatch(
	ctx context.Context,
	rs []*optimport.ImportResource,
	nameTable map[string]string,
) error {
	_, err := s.stack.ImportResources(ctx,
		optimport.Resources(rs),
		optimport.NameTable(nameTable),
		optimport.Protect(false),
		optimport.GenerateCode(false),
		optimport.ProgressStreams(os.Stderr),
		optimport.ErrorProgressStreams(os.Stderr),
	)
	return err
}

// ExistingResources reads the stack's current resources.
func (s *stackImporter) ExistingResources(ctx context.Context) (map[ResourceKey]bool, error) {
	dep, err := s.stack.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("exporting stack state: %w", err)
	}
	return resourceKeysFromDeployment(dep.Deployment)
}

// resourceKeysFromDeployment parses the inner deployment object returned by
// Stack.Export — {"resources": [...]}, with no outer "deployment" wrapper.
func resourceKeysFromDeployment(raw []byte) (map[ResourceKey]bool, error) {
	keys := map[ResourceKey]bool{}
	if len(raw) == 0 {
		return keys, nil
	}
	var dep struct {
		Resources []struct {
			URN string `json:"urn"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &dep); err != nil {
		return nil, fmt.Errorf("parsing stack deployment: %w", err)
	}
	for _, r := range dep.Resources {
		if k, ok := ParseURN(r.URN); ok {
			keys[k] = true
		}
	}
	return keys, nil
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/batchimport/ -v`
Expected: PASS.

- [x] **Step 5: Verify the interface is satisfied**

Run: `go build ./...`
Expected: no output. (`NewStackImporter` returns `Importer`, so a missing method is a compile error.)

- [x] **Step 6: Commit**

```bash
git add pkg/batchimport/stack.go pkg/batchimport/stack_test.go
git commit -m "feat(batchimport): add Automation API stack adapter"
```

---

### Task 7: The `import` command

**Files:**
- Create: `cmd/import.go`
- Test: `cmd/import_test.go`

**Interfaces:**
- Consumes: `batchimport.LoadImportFile`, `NewStackImporter`, `Run`, `Options`, `Result`, `Failure`, `DefaultBatchSize`.
- Produces: `func newImportCmd() *cobra.Command`; `func formatResult(res *batchimport.Result, dryRun bool) string`.

- [x] **Step 1: Write the failing test**

Create `cmd/import_test.go` (license header first):

```go
package cmd

import (
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/batchimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatResult_Success(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Imported: []batchimport.ResourceKey{
			{Type: "aws:s3/bucket:Bucket", Name: "b1"},
			{Type: "aws:ec2/vpc:Vpc", Name: "vpc"},
		},
		Skipped:    []batchimport.ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "b0"}},
		BatchCount: 1,
	}, false)

	assert.Contains(t, out, "Imported: 2")
	assert.Contains(t, out, "Skipped:  1")
	assert.Contains(t, out, "Failed:   0")
	assert.NotContains(t, out, "FAILED RESOURCES")
}

func TestFormatResult_Failures(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Imported: []batchimport.ResourceKey{{Type: "aws:ec2/vpc:Vpc", Name: "vpc"}},
		Failed: []batchimport.Failure{{
			Key: batchimport.ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b1"},
			ID:  "bad-id",
			Err: "resource does not exist",
		}},
		BatchCount: 1,
	}, false)

	assert.Contains(t, out, "Failed:   1")
	assert.Contains(t, out, "FAILED RESOURCES")
	assert.Contains(t, out, "b1")
	assert.Contains(t, out, "aws:s3/bucket:Bucket")
	assert.Contains(t, out, "bad-id")
	assert.Contains(t, out, "resource does not exist")
}

func TestFormatResult_DryRun(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Planned:    []batchimport.ResourceKey{{Type: "aws:ec2/vpc:Vpc", Name: "vpc"}},
		Skipped:    []batchimport.ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "b0"}},
		BatchCount: 1,
	}, true)

	assert.Contains(t, out, "Dry run")
	assert.Contains(t, out, "Would import: 1")
	assert.Contains(t, out, "Batches:      1")
	assert.NotContains(t, out, "Imported:")
}

func TestNewImportCmd_Flags(t *testing.T) {
	t.Parallel()

	cmd := newImportCmd()
	require.NotNil(t, cmd.Flags().Lookup("file"))
	require.NotNil(t, cmd.Flags().Lookup("project-dir"))
	require.NotNil(t, cmd.Flags().Lookup("stack"))
	require.NotNil(t, cmd.Flags().Lookup("batch-size"))
	require.NotNil(t, cmd.Flags().Lookup("no-resume"))
	require.NotNil(t, cmd.Flags().Lookup("dry-run"))
	assert.Equal(t, "import", cmd.Use)
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run 'TestFormatResult|TestNewImportCmd' -v`
Expected: FAIL — `undefined: formatResult`, `undefined: newImportCmd`.

- [x] **Step 3: Write the implementation**

Create `cmd/import.go` (license header first):

```go
package cmd

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/batchimport"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var filePath string
	var projectDir string
	var stack string
	var batchSize int
	var noResume bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a prepared import file in batches, isolating failures",
		Long: `Import the resources in a prepared Pulumi import file, in batches, and
report every resource that failed.

A single malformed import ID aborts a whole "pulumi import" batch. When a batch
does not fully land, this command re-imports that batch's resources one at a
time to identify exactly which ones failed, records them, and carries on. One
run therefore surfaces every bad import ID instead of one per run.

Whether a resource imported is determined by reading stack state afterwards, not
by the importer's exit status. Resources already present in state are skipped, so
a run can be repeated after fixing the reported IDs; pass --no-resume to disable.

Every component entry is included in every batch so child resources can resolve
their parent through the nameTable.

Resources are always imported unprotected and without code generation, matching
the hand-authored migration workflow.

Cloud credentials come from the environment. Wrap the command if you use ESC:

  pulumi env run <esc-env> -- pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev

Examples:

  # Inspect the plan without importing
  pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev --dry-run

  # Import, 50 resources per batch
  pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev --batch-size 50
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			file, err := batchimport.LoadImportFile(filePath)
			if err != nil {
				return err
			}

			imp, err := batchimport.NewStackImporter(ctx, projectDir, stack)
			if err != nil {
				return err
			}

			res, err := batchimport.Run(ctx, imp, file, batchimport.Options{
				BatchSize: batchSize,
				Resume:    !noResume,
				DryRun:    dryRun,
			})
			if err != nil {
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), formatResult(res, dryRun))

			if len(res.Failed) > 0 {
				// The printed report is the useful output, so suppress cobra's
				// error and usage noise; Execute() still exits non-zero.
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				return fmt.Errorf("%d resource(s) failed to import", len(res.Failed))
			}
			return nil
		},
	}

	// No backticks in usage strings: pflag's UnquoteUsage treats the first
	// backquoted word as the flag's type placeholder.
	cmd.Flags().StringVar(&filePath, "file", "", "Prepared import file (from the resolve or import-id-match command)")
	cmd.Flags().StringVar(&projectDir, "project-dir", ".", "Pulumi project directory")
	cmd.Flags().StringVar(&stack, "stack", "", "Pulumi stack name")
	cmd.Flags().IntVar(&batchSize, "batch-size", batchimport.DefaultBatchSize, "Resources per batch")
	cmd.Flags().BoolVar(&noResume, "no-resume", false, "Import resources even if already in stack state")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the batch plan without importing")

	cmd.MarkFlagRequired("file")
	cmd.MarkFlagRequired("stack")

	return cmd
}

// formatResult renders the end-of-run summary.
func formatResult(res *batchimport.Result, dryRun bool) string {
	var b strings.Builder

	if dryRun {
		fmt.Fprintf(&b, "\nDry run — nothing imported.\n")
		fmt.Fprintf(&b, "  Would import: %d\n", len(res.Planned))
		fmt.Fprintf(&b, "  Skipped:      %d (already in state)\n", len(res.Skipped))
		fmt.Fprintf(&b, "  Batches:      %d\n", res.BatchCount)
		return b.String()
	}

	fmt.Fprintf(&b, "\nImport summary (%d batches)\n", res.BatchCount)
	fmt.Fprintf(&b, "  Imported: %d\n", len(res.Imported))
	fmt.Fprintf(&b, "  Skipped:  %d (already in state)\n", len(res.Skipped))
	fmt.Fprintf(&b, "  Failed:   %d\n", len(res.Failed))

	if len(res.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED RESOURCES\n")
		for _, f := range res.Failed {
			fmt.Fprintf(&b, "  %s (%s)\n", f.Key.Name, f.Key.Type)
			fmt.Fprintf(&b, "    id:    %s\n", f.ID)
			fmt.Fprintf(&b, "    error: %s\n", strings.TrimSpace(f.Err))
		}
		fmt.Fprintf(&b, "\nFix the import IDs above and re-run; imported resources are skipped.\n")
	}

	return b.String()
}

func init() { rootCmd.AddCommand(newImportCmd()) }
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run 'TestFormatResult|TestNewImportCmd' -v`
Expected: PASS — 4 tests.

- [x] **Step 5: Verify the command is registered**

Run: `go run . import --help`
Expected: the long help text above, listing all six flags.

- [x] **Step 6: Commit**

```bash
git add cmd/import.go cmd/import_test.go
git commit -m "feat(cmd): add import command backed by pkg/batchimport"
```

---

### Task 8: Retire the script and update the docs

**Files:**
- Delete: `skills/pulumi-terraform-workspace-migration/scripts/batch-import.bb`
- Modify: `skills/pulumi-terraform-workspace-migration/SKILL.md` (the "Bundled scripts" section)
- Modify: `skills/pulumi-terraform-workspace-migration/references/import-mechanics.md` (the "Run the import" section)
- Modify: `README.md` (the skills table row and the command bullet list)

**Interfaces:**
- Consumes: the `import` command from Task 7.
- Produces: nothing consumed by later tasks.

- [x] **Step 1: Delete the script and its now-empty directory**

```bash
git rm skills/pulumi-terraform-workspace-migration/scripts/batch-import.bb
```

- [x] **Step 2: Replace the "Bundled scripts" section in SKILL.md**

The section currently reads:

```markdown
## Bundled scripts

**`scripts/batch-import.bb`** (requires [babashka](https://babashka.org)) —
splits a prepared import file into batches and imports them sequentially, putting
**all** `component: true` entries in every batch so parent references resolve.
See `references/import-mechanics.md`.
```

Delete it entirely — the skill no longer bundles scripts.

- [x] **Step 3: Replace the batching instructions in `references/import-mechanics.md`**

Replace everything from `For larger stacks, batch it.` through the closing fence of the third `bb scripts/batch-import.bb` block with:

````markdown
For larger stacks, use the tool's `import` command. It batches the file, puts
**all** `component: true` entries in every batch so parent references resolve,
and — when a batch does not fully land — re-imports that batch's resources
individually to identify exactly which import IDs are bad:

```bash
# Inspect the plan first
pulumi plugin run terraform-migrate -- import \
  --file .import/imports-ready.json \
  --project-dir . --stack <stack-name> --dry-run

# Import (wrap in `pulumi env run <esc-env> --` if you use ESC for credentials)
pulumi plugin run terraform-migrate -- import \
  --file .import/imports-ready.json \
  --project-dir . --stack <stack-name> --batch-size 100
```

The run ends with a summary and, if anything failed, a table naming each failed
resource, the import ID attempted, and the error. Fix those IDs and re-run:
resources already in state are skipped automatically (`--no-resume` disables it).

Success is determined by reading stack state, not by the importer's exit status,
so neither the cosmetic `parse resource provider reference` message nor the
Automation API's spurious `failed to read generated code` error (which it returns
after a *successful* import whenever code generation is off) is mistaken for a
failure.
````

- [x] **Step 4: Update the README**

In the skills table, change the `pulumi-terraform-workspace-migration` row's trailing sentence from `Bundles \`batch-import.bb\`.` to `Uses the \`import\` command for batched, failure-isolating imports.`

In the bullet list of commands near the top, add after the `patch-state` bullet:

```markdown
- **`import`** — Imports a prepared import file in batches, isolating per-resource failures so one run reports every bad import ID
```

- [x] **Step 5: Verify no stale references remain**

Run: `grep -rn "batch-import" skills/ README.md docs/superpowers/skills/ ; echo "exit=$?"`
Expected: no matches from `skills/` or `README.md` (`exit=1`). Matches inside `docs/superpowers/specs/` and `docs/superpowers/plans/` are expected — those are historical documents.

- [x] **Step 6: Verify the whole build and suite still pass**

Run: `go build ./... && go test ./...`
Expected: all packages `ok`.

- [x] **Step 7: Commit**

```bash
git add -A skills README.md
git commit -m "docs: replace batch-import script with the import command"
```

---

## Verification

After Task 8, confirm end to end against a real stack — this is the only step that exercises `stackImporter`, which has no unit test:

- [x] On a scratch Pulumi project with at least one importable resource and a prepared import file, run `import --dry-run` and confirm the plan matches the file.
- [x] Run `import` and confirm: resources land, the summary reports them as imported, and **the exit code is 0** despite the SDK's generated-code error. A non-zero exit here means the state-verification path is not working.
- [x] Re-run the same command and confirm every resource is reported as skipped and no import is attempted.
- [x] Corrupt one import ID in the file, remove that resource from state (`pulumi state delete`), re-run, and confirm the resource is named in the FAILED RESOURCES table with a non-zero exit while the others still import.

## Follow-up (not blocking)

- [x] File an issue against `pulumi/pulumi`: `auto.Stack.ImportResources` returns `failed to read generated code` after a successful import when `optimport.GenerateCode(false)` is set, because `--out` is only passed in the code-generation branch while the read is unconditional (`sdk/go/auto/stack.go`, verified in v3.222.0, v3.233.0, v3.246.0). Include the three-line reproduction and note that the workaround — verifying against stack state — is what `pkg/batchimport` does.
