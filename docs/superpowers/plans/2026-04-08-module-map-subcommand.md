# Module Map Subcommand Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace custom HCL evaluation with OpenTofu's `Context.Eval()`, extract module map generation into a standalone `module-map` subcommand, and create a `refactor-to-components` skill.

**Architecture:** Three-stage pipeline (Load → Evaluate → Build) using OpenTofu's `configs.Config` for HCL parsing and `Context.Eval()` for expression evaluation. Provider plugins loaded from `.terraform/providers/` for schema resolution. Module map written as `module-map.json` sidecar.

**Tech Stack:** Go, OpenTofu (`pulumi/opentofu` fork), cobra CLI, terraform-bridge (URN resolution)

**Spec:** `docs/superpowers/specs/2026-04-08-module-map-subcommand-design.md`

**Branch strategy:** Start from `main`, NOT from the `feat/mc-25-component-map-sidecar` feature stack. Delivered as a **stack of PRs** using `git spice`:

| PR | Branch | Contents | Depends on |
|----|--------|----------|------------|
| 0 | (spike) | Fork setup + prototype spike (throwaway, not merged) | — |
| 1 | `feat/module-tree` | Cherry-pick `module_tree.go` + testdata from feature stack | — |
| 2 | `feat/tofu-eval` | `pkg/tofu_eval.go` — config loading, state loading, format detection, Context.Eval() wrapper | PR 1 |
| 3 | `feat/module-map-builder` | `pkg/module_map.go` — types, builder, writer + tests | PR 2 |
| 4 | `feat/module-map-cmd` | `cmd/module_map.go` — CLI subcommand + integration tests | PR 3 |
| 5 | `feat/refactor-to-components-skill` | Skill files (SKILL.md + references/) | PR 4 |

This avoids inheriting ~2500 lines of custom evaluation code that we're replacing. Each PR is independently reviewable and passes `go build` + `go test`.

---

## File Structure

### New files

| File | Responsibility |
|------|---------------|
| `cmd/module_map.go` | Cobra subcommand: parse flags, call `pkg.BuildModuleMap()` |
| `pkg/module_map.go` | `ModuleMap`/`ModuleMapEntry` types, `BuildModuleMap()` orchestrator, `WriteModuleMap()`, `propertyValueToInterface()`, `ctyValueToInterface()` |
| `pkg/module_map_test.go` | Unit + integration tests for module map building |
| `pkg/tofu_eval.go` | `LoadConfig()`, `LoadState()`, `DetectStateFormat()`, `LoadProviders()`, `Evaluate()` — wrappers around OpenTofu APIs |
| `pkg/tofu_eval_test.go` | Tests for config/state loading, format detection, evaluation |
| `docs/superpowers/skills/refactor-to-components/SKILL.md` | Skill workflow |
| `docs/superpowers/skills/refactor-to-components/references/alias-wiring-pattern.md` | Transform-based alias examples |
| `docs/superpowers/skills/refactor-to-components/references/module-map-format.md` | module-map.json schema reference |
| `docs/superpowers/skills/refactor-to-components/references/existing-component-integration.md` | Existing component mapping guide |

### Modified files

| File | Changes |
|------|---------|
| `pkg/state_adapter.go` | Remove `ComponentMapData`, `ComponentMetadata`, `PulumiProviders` from `TranslateStateResult`. Remove module map/schema writing from `TranslateAndWriteState`. Remove component tree building from `convertState()`. |
| `pkg/pulumi_state.go` | Remove `ComponentMetadata` field from `PulumiState` |
| `pkg/state_adapter_test.go` | Remove/update tests that assert on `ComponentMapData` |
| `pkg/e2e_test.go` | Remove tests that assert on `ComponentMapData` or `ComponentMetadata` |
| `go.mod` | Add `replace` directive for local `pulumi/opentofu` fork checkout |

### Files NOT carried over from feature stack

These files exist on `feat/mc-25-component-map-sidecar` but are intentionally not brought to the new branch. They are replaced by the new implementation:

| Feature stack file | Replaced by |
|-------------------|-------------|
| `pkg/hcl/evaluator.go` | `pkg/tofu_eval.go` (Context.Eval) |
| `pkg/hcl/parser.go` | `configs.Config` from fork |
| `pkg/hcl/discovery.go` | `configload.Loader` from fork |
| `pkg/hcl/convert.go` | `ctyValueToInterface()` in `pkg/module_map.go` |
| `pkg/component_populate.go` | `pkg/tofu_eval.go` + `pkg/module_map.go` |
| `pkg/component_metadata.go` | Interface data built from `configs.Config` in `pkg/module_map.go` |
| `pkg/component_schema.go` | Validation deferred to skill |
| `pkg/component_map.go` | Rewritten as `pkg/module_map.go` |

---

## Chunk 0 (Spike — not merged): Fork Setup + Prototype

This chunk resolves the open questions from the spec (Part 9) before TDD implementation begins. It produces throwaway spike code, not production code. No PR created.

### Task 1: Set up local OpenTofu fork with exported packages

**Files:**
- Modify: `go.mod` (add `replace` directive)
- External: local checkout of `pulumi/opentofu` fork

- [ ] **Step 1: Clone the pulumi/opentofu fork locally**

```bash
cd ~/pulumi-repos
git clone git@github.com:pulumi/opentofu.git pulumi-opentofu-fork
cd pulumi-opentofu-fork
```

- [ ] **Step 2: Identify which `internal/` packages need exporting**

Check the fork's current structure. Packages we need:
- `configs` (ModuleCall, Module.Variables, Module.Outputs)
- `configs/configload` (Loader, LoadConfig)
- `tofu` (Context, ContextOpts, Eval)
- Provider plugin loading (likely `providercache` or `plugins`)

```bash
# Check what's already exported (non-internal)
ls -d */ | grep -v internal
# Check what we need from internal
ls internal/configs/ internal/configs/configload/ internal/tofu/
```

- [ ] **Step 3: Create exported wrapper packages in the fork**

For each internal package, create a top-level re-export. Example for `configs`:

```go
// configs/configs.go (new file at top level)
package configs

// Re-export from internal
import internal "github.com/pulumi/opentofu/internal/configs"

type Config = internal.Config
type Module = internal.Module
type ModuleCall = internal.ModuleCall
// ... etc
```

Alternatively, move the packages out of `internal/` if the fork's structure allows it. The exact approach depends on how the fork is currently structured.

- [ ] **Step 4: Add replace directive to go.mod**

```bash
cd /Users/jdavenport/pulumi-repos/pulumi-tool-terraform-migrate
go mod edit -replace github.com/pulumi/opentofu=../pulumi-opentofu-fork
go mod tidy
```

- [ ] **Step 5: Verify imports compile**

```go
// spike/main.go (throwaway)
package main

import (
    "fmt"
    "github.com/pulumi/opentofu/configs"
    "github.com/pulumi/opentofu/configs/configload"
)

func main() {
    fmt.Println(configs.Config{}, configload.Config{})
}
```

```bash
go build ./spike/
```

- [ ] **Step 6: Commit fork changes (on fork branch)**

```bash
cd ~/pulumi-repos/pulumi-opentofu-fork
git checkout -b feat/export-configs-tofu
git add -A
git commit -m "feat: export configs, configload, tofu packages for external use"
```

---

### Task 2: Spike — resolve Context.Eval() open questions

**Files:**
- Create: `spike/eval_spike.go` (throwaway)

This spike answers the 5 open questions from the spec. Each step is a self-contained experiment.

- [ ] **Step 1: Spike config loading**

Test that `configload.NewLoader` + `LoadConfig` works with an existing test fixture.

```go
// spike/eval_spike.go
package main

import (
    "fmt"
    "path/filepath"
    "github.com/pulumi/opentofu/configs/configload"
)

func main() {
    tfDir := "pkg/testdata/tf_indexed_modules"
    loader := configload.NewLoader(&configload.Config{
        ModulesDir: filepath.Join(tfDir, ".terraform/modules"),
    })
    // Try loading — discover the correct LoadConfig signature
    config, diags := loader.LoadConfig(/* ... */)
    fmt.Println(config, diags)

    // Print module calls
    for name, call := range config.Module.ModuleCalls {
        fmt.Printf("Module: %s, Source: %s\n", name, call.SourceAddr)
        attrs, _ := call.Config.JustAttributes()
        for attrName := range attrs {
            fmt.Printf("  Arg: %s\n", attrName)
        }
    }

    // Print variables from child config
    for name, child := range config.Children {
        fmt.Printf("Child: %s\n", name)
        for varName, v := range child.Module.Variables {
            fmt.Printf("  Var: %s (type: %s)\n", varName, v.Type)
        }
    }
}
```

```bash
go run ./spike/
```

**Answers open question 5:** What is the correct `LoadConfig` call signature?

- [ ] **Step 2: Spike state loading with statefile.Read**

Test loading a raw `.tfstate` file (need to capture one first if not in testdata).

```go
// Check if we have a raw tfstate in testdata
// If not, create one:
// cd pkg/testdata/tf_indexed_modules && tofu init -backend=false && tofu apply -auto-approve
// cp terraform.tfstate ../tofu_tfstate_indexed_modules.tfstate
```

```go
import (
    "bytes"
    "os"
    "github.com/pulumi/opentofu/states/statefile"
    "github.com/pulumi/opentofu/encryption"
)

data, _ := os.ReadFile("pkg/testdata/tofu_tfstate_indexed_modules.tfstate")
sf, _ := statefile.Read(bytes.NewReader(data), encryption.StateEncryptionDisabled())
fmt.Printf("State version: %d, resources: %d\n", sf.TerraformVersion, len(sf.State.Resources))
```

- [ ] **Step 3: Spike provider plugin loading**

Discover the exact package path and API for loading provider plugins from `.terraform/providers/`.

```go
// Explore the fork for:
// - providercache package (finds binaries on disk)
// - plugins.Library or similar (creates Factory map)
// - What tofu.ContextOpts.Plugins expects
```

**Answers open question 3:** Exact package paths for provider factory creation.

- [ ] **Step 4: Spike Context.Eval()**

Wire up config + state + providers and call `Eval()`.

```go
import (
    "github.com/pulumi/opentofu/tofu"
    "github.com/pulumi/opentofu/addrs"
)

ctx := tofu.NewContext(&tofu.ContextOpts{
    Plugins: pluginLibrary,
})
scope, diags := ctx.Eval(context.Background(), config, state,
    addrs.RootModuleInstance, &tofu.EvalOpts{})
if diags.HasErrors() {
    fmt.Println("Eval errors:", diags.Err())
}

// Try evaluating a module call-site expression
call := config.Module.ModuleCalls["pet"]
attrs, _ := call.Config.JustAttributes()
for name, attr := range attrs {
    val, evalDiags := scope.EvalExpr(attr.Expr, cty.DynamicPseudoType)
    fmt.Printf("  %s = %s (errors: %v)\n", name, val.GoString(), evalDiags.HasErrors())
}
```

**Answers open question 1:** Does the root scope give access to child module scopes?

- [ ] **Step 5: Spike child module scope access**

Test whether we can evaluate nested module call-site expressions.

```go
// Try Eval() with a child module instance
childScope, diags := ctx.Eval(context.Background(), config, state,
    addrs.RootModuleInstance.Child("pet", addrs.IntKey(0)), &tofu.EvalOpts{})
// If this works, we can eval per-instance
// If not, we need another approach
```

**Answers open question 1 & 2:** Child scopes and count/for_each handling.

- [ ] **Step 6: Spike tfvars loading**

```go
// Check if EvalOpts.SetVariables handles tfvars,
// or if we need to parse them separately
// Try:
// 1. EvalOpts with nil SetVariables (does it auto-load?)
// 2. Manually loading terraform.tfvars and passing values
```

**Answers open question 4:** tfvars loading mechanism.

- [ ] **Step 7: Document findings**

Create `docs/superpowers/spike-findings/2026-04-08-context-eval.md` with answers to all 5 open questions. Include exact import paths, function signatures, and any gotchas discovered. Commit this file.

```bash
git add docs/superpowers/spike-findings/
git commit -m "docs: Context.Eval() spike findings"
```

- [ ] **Step 8: Clean up spike code**

```bash
rm -rf spike/
```

Spike code is throwaway. The findings document is the deliverable.

---

## Chunk 1 (PR 1: `feat/module-tree`): Cherry-pick from Feature Stack

Branch from `main`. Bring over only the pieces we need from the feature stack.

### Task 3: Create branch from main and cherry-pick needed code

**Files:**
- Cherry-pick: `pkg/module_tree.go`, `pkg/module_tree_test.go` (address parsing, tree construction)
- Cherry-pick: testdata fixtures created during feature stack work
- Cherry-pick: `pkg/statefile/` (state file upgrade utilities, if not on main)

- [ ] **Step 1: Create branch from main using git spice**

```bash
git checkout main
git pull upstream main
git spice branch create feat/module-tree
```

- [ ] **Step 2: Identify commits to cherry-pick from feature stack**

```bash
git log --oneline feat/mc-25-component-map-sidecar -- pkg/module_tree.go pkg/module_tree_test.go pkg/testdata/ pkg/statefile/
```

Cherry-pick selectively. For files that have evolved across many commits, it may be cleaner to copy the final version directly:

```bash
# Copy final versions from feature stack
git show feat/mc-25-component-map-sidecar:pkg/module_tree.go > pkg/module_tree.go
git show feat/mc-25-component-map-sidecar:pkg/module_tree_test.go > pkg/module_tree_test.go
```

- [ ] **Step 3: Copy testdata fixtures**

Copy state files and TF source directories needed by tests. These were captured from real tofu operations.

```bash
# List what testdata exists on the feature stack
git show feat/mc-25-component-map-sidecar --stat -- pkg/testdata/

# Copy needed fixtures (adjust list based on what's available)
for f in tofu_state_indexed_modules.json tofu_state_dns_to_db.json tofu_state_multi_resource_module.json tofu_state_complex_expressions.json tofu_state_tfvars_resolution.json; do
    git show feat/mc-25-component-map-sidecar:pkg/testdata/$f > pkg/testdata/$f 2>/dev/null || true
done

# Copy TF source directories (these may be multi-file)
git checkout feat/mc-25-component-map-sidecar -- pkg/testdata/tf_indexed_modules pkg/testdata/tf_dns_to_db pkg/testdata/tf_multi_resource_module pkg/testdata/tf_complex_expressions pkg/testdata/tf_tfvars_resolution 2>/dev/null || true
```

- [ ] **Step 4: Verify module_tree.go compiles on main**

```bash
go build ./pkg/...
```

Fix any import issues — `module_tree.go` should only depend on stdlib and the existing `pkg` package.

- [ ] **Step 5: Run module_tree tests**

```bash
go test ./pkg/ -run "TestBuildComponentTree|TestParseModuleSegments|TestSplitAddressParts" -v -count=1
```

- [ ] **Step 6: Copy the spec and plan docs from current branch**

```bash
git checkout feat/module-map-subcommand -- docs/superpowers/specs/ docs/superpowers/plans/
```

- [ ] **Step 7: Commit**

```bash
git add pkg/module_tree.go pkg/module_tree_test.go pkg/testdata/ docs/superpowers/
git commit -m "feat: cherry-pick module tree + testdata from feature stack

Brings over address parsing, tree construction, and real testdata
fixtures as foundation for the module-map subcommand."
```

---

### Task 4: Capture raw .tfstate testdata from real tofu operations

**Files:**
- Create: `pkg/testdata/tofu_tfstate_indexed_modules.tfstate`
- Create: any other raw `.tfstate` fixtures needed

Raw `.tfstate` format is central to the full-evaluation path. These must be captured from real `tofu` operations, not handcrafted.

- [ ] **Step 1: Capture raw tfstate for indexed modules fixture**

```bash
cd pkg/testdata/tf_indexed_modules
tofu init -backend=false
tofu apply -auto-approve
cp terraform.tfstate ../tofu_tfstate_indexed_modules.tfstate
tofu destroy -auto-approve
```

- [ ] **Step 2: Verify the captured file is valid**

```bash
python3 -c "import json; d=json.load(open('pkg/testdata/tofu_tfstate_indexed_modules.tfstate')); print(f'version={d[\"version\"]}, resources={len(d[\"resources\"])}')"
```

Expected: `version=4, resources=2` (or similar)

- [ ] **Step 3: Commit**

```bash
git add pkg/testdata/tofu_tfstate_indexed_modules.tfstate
git commit -m "testdata: capture raw tfstate for indexed modules from real tofu apply"
```

---

## Chunk 2 (PR 2: `feat/tofu-eval`): OpenTofu Evaluation Wrapper (TDD)

This chunk builds `pkg/tofu_eval.go` — the wrapper around OpenTofu's config loading, state loading, and `Context.Eval()`.

**Important:** The exact API calls depend on spike findings from Task 2. The code below uses the expected signatures; adjust based on what the spike discovered. Code examples are pseudocode showing the expected shape, not final implementations.

### Task 5: State format auto-detection

**Files:**
- Create: `pkg/tofu_eval.go`
- Create: `pkg/tofu_eval_test.go`

- [ ] **Step 1: Write failing test for state format detection**

```go
// pkg/tofu_eval_test.go
package pkg

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestDetectStateFormat_RawTfstate(t *testing.T) {
    t.Parallel()
    format, err := DetectStateFormat("testdata/tofu_tfstate_indexed_modules.tfstate")
    require.NoError(t, err)
    require.Equal(t, StateFormatRaw, format)
}

func TestDetectStateFormat_TofuShowJson(t *testing.T) {
    t.Parallel()
    format, err := DetectStateFormat("testdata/tofu_state_indexed_modules.json")
    require.NoError(t, err)
    require.Equal(t, StateFormatShowJSON, format)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/ -run TestDetectStateFormat -v -count=1
```

Expected: FAIL (function not defined)

- [ ] **Step 3: Implement DetectStateFormat**

```go
// pkg/tofu_eval.go
package pkg

import (
    "encoding/json"
    "fmt"
    "os"
)

type StateFormat int

const (
    StateFormatRaw      StateFormat = iota // Raw terraform.tfstate
    StateFormatShowJSON                     // tofu show -json output
)

// DetectStateFormat peeks at a state file to determine its format.
// Raw tfstate has no "format_version" key. tofu show -json has "format_version".
func DetectStateFormat(path string) (StateFormat, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return 0, fmt.Errorf("reading state file: %w", err)
    }
    var probe struct {
        FormatVersion *string `json:"format_version"`
    }
    if err := json.Unmarshal(data, &probe); err != nil {
        return 0, fmt.Errorf("parsing state file JSON: %w", err)
    }
    if probe.FormatVersion != nil {
        return StateFormatShowJSON, nil
    }
    return StateFormatRaw, nil
}
```

- [ ] **Step 4: Ensure testdata exists**

If `testdata/tofu_tfstate_indexed_modules.tfstate` doesn't exist, capture it:

```bash
cd pkg/testdata/tf_indexed_modules
tofu init -backend=false
tofu apply -auto-approve
cp terraform.tfstate ../tofu_tfstate_indexed_modules.tfstate
tofu destroy -auto-approve
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./pkg/ -run TestDetectStateFormat -v -count=1
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/tofu_eval.go pkg/tofu_eval_test.go pkg/testdata/tofu_tfstate_indexed_modules.tfstate
git commit -m "feat: add state format auto-detection (raw tfstate vs tofu show json)"
```

---

### Task 6: Config loading wrapper

**Files:**
- Modify: `pkg/tofu_eval.go`
- Modify: `pkg/tofu_eval_test.go`

- [ ] **Step 1: Write failing test for config loading**

```go
func TestLoadConfig(t *testing.T) {
    t.Parallel()
    config, err := LoadConfig("testdata/tf_indexed_modules")
    require.NoError(t, err)
    require.NotNil(t, config)

    // Should have module calls
    require.Contains(t, config.Module.ModuleCalls, "pet")

    // Child config should have variables
    petConfig, ok := config.Children["pet"]
    require.True(t, ok)
    require.Contains(t, petConfig.Module.Variables, "prefix")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/ -run TestLoadConfig -v -count=1
```

- [ ] **Step 3: Implement LoadConfig**

```go
// LoadConfig loads a Terraform configuration from a source directory.
// Requires .terraform/modules/ to exist (run tofu init first).
func LoadConfig(tfDir string) (*configs.Config, error) {
    modulesDir := filepath.Join(tfDir, ".terraform/modules")
    loader, err := configload.NewLoader(&configload.Config{
        ModulesDir: modulesDir,
    })
    if err != nil {
        return nil, fmt.Errorf("creating config loader: %w", err)
    }
    // Exact call depends on spike findings — adjust signature as needed
    config, diags := loader.LoadConfig(tfDir)
    if diags.HasErrors() {
        return nil, fmt.Errorf("loading config: %w", diags.Err())
    }
    return config, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/ -run TestLoadConfig -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add pkg/tofu_eval.go pkg/tofu_eval_test.go
git commit -m "feat: add config loading wrapper around configload.Loader"
```

---

### Task 7: Raw state loading wrapper

**Files:**
- Modify: `pkg/tofu_eval.go`
- Modify: `pkg/tofu_eval_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestLoadRawState(t *testing.T) {
    t.Parallel()
    state, err := LoadRawState("testdata/tofu_tfstate_indexed_modules.tfstate")
    require.NoError(t, err)
    require.NotNil(t, state)
    // Should have resources
    require.Greater(t, len(state.Resources), 0)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/ -run TestLoadRawState -v -count=1
```

- [ ] **Step 3: Implement LoadRawState**

```go
func LoadRawState(path string) (*states.State, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading state file: %w", err)
    }
    sf, err := statefile.Read(bytes.NewReader(data), encryption.StateEncryptionDisabled())
    if err != nil {
        return nil, fmt.Errorf("parsing state file: %w", err)
    }
    return sf.State, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/ -run TestLoadRawState -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add pkg/tofu_eval.go pkg/tofu_eval_test.go
git commit -m "feat: add raw tfstate loading via statefile.Read"
```

---

### Task 8: Provider loading + Context.Eval() wrapper

**Files:**
- Modify: `pkg/tofu_eval.go`
- Modify: `pkg/tofu_eval_test.go`

**Note:** This is the most uncertain task — exact APIs depend on spike findings. The test verifies end-to-end evaluation.

- [ ] **Step 1: Write failing test for evaluation**

```go
func TestEvaluate_IndexedModules(t *testing.T) {
    t.Parallel()
    // Skip if .terraform/providers doesn't exist
    if _, err := os.Stat("testdata/tf_indexed_modules/.terraform/providers"); os.IsNotExist(err) {
        t.Skip("requires tofu init on tf_indexed_modules fixture")
    }

    config, err := LoadConfig("testdata/tf_indexed_modules")
    require.NoError(t, err)

    state, err := LoadRawState("testdata/tofu_tfstate_indexed_modules.tfstate")
    require.NoError(t, err)

    scope, err := Evaluate(config, state, "testdata/tf_indexed_modules")
    require.NoError(t, err)
    require.NotNil(t, scope)

    // Evaluate a module call-site expression
    call := config.Module.ModuleCalls["pet"]
    attrs, diags := call.Config.JustAttributes()
    require.False(t, diags.HasErrors())

    prefixAttr, ok := attrs["prefix"]
    require.True(t, ok)

    val, evalDiags := scope.EvalExpr(prefixAttr.Expr, cty.DynamicPseudoType)
    require.False(t, evalDiags.HasErrors())
    // prefix is "test-${count.index}" — for the root call, this may be a template
    // The exact value depends on how count expansion works in the scope
    require.True(t, val.IsKnown())
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/ -run TestEvaluate_IndexedModules -v -count=1
```

- [ ] **Step 3: Implement Evaluate**

```go
// Evaluate creates an OpenTofu evaluation scope for the given config and state.
// Loads provider plugins from tfDir/.terraform/providers/ for schema resolution.
func Evaluate(config *configs.Config, state *states.State, tfDir string) (*lang.Scope, error) {
    // Load providers — exact API from spike findings
    providers, err := loadProviders(config, tfDir)
    if err != nil {
        return nil, fmt.Errorf("loading providers: %w", err)
    }

    ctx, ctxDiags := tofu.NewContext(&tofu.ContextOpts{
        Plugins: providers,
    })
    if ctxDiags.HasErrors() {
        return nil, fmt.Errorf("creating tofu context: %w", ctxDiags.Err())
    }

    // Load tfvars
    inputVars := loadTfvars(tfDir) // exact impl from spike findings

    scope, diags := ctx.Eval(context.Background(), config, state,
        addrs.RootModuleInstance, &tofu.EvalOpts{
            SetVariables: inputVars,
        })
    if diags.HasErrors() {
        return nil, fmt.Errorf("evaluating: %w", diags.Err())
    }
    return scope, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/ -run TestEvaluate_IndexedModules -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add pkg/tofu_eval.go pkg/tofu_eval_test.go
git commit -m "feat: add Context.Eval() wrapper with provider plugin loading"
```

---

## Chunk 3 (PR 3: `feat/module-map-builder`): Module Map Builder (TDD)

This chunk builds the module map JSON output from `configs.Config` + `lang.Scope`.

### Task 9: Module map types and basic builder

**Files:**
- Create: `pkg/module_map.go`
- Create: `pkg/module_map_test.go`

- [ ] **Step 1: Write failing test for module map structure**

```go
// pkg/module_map_test.go
package pkg

import (
    "testing"
    "github.com/stretchr/testify/require"
)

func TestBuildModuleMap_IndexedModules(t *testing.T) {
    t.Parallel()
    if _, err := os.Stat("testdata/tf_indexed_modules/.terraform/providers"); os.IsNotExist(err) {
        t.Skip("requires tofu init on tf_indexed_modules fixture")
    }

    config, err := LoadConfig("testdata/tf_indexed_modules")
    require.NoError(t, err)

    state, err := LoadRawState("testdata/tofu_tfstate_indexed_modules.tfstate")
    require.NoError(t, err)

    scope, err := Evaluate(config, state, "testdata/tf_indexed_modules")
    require.NoError(t, err)

    // Also need tfjson state for resource matching
    tfjsonState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
        StateFilePath: "testdata/tofu_state_indexed_modules.json",
    })
    require.NoError(t, err)

    mm, err := BuildModuleMap(config, scope, tfjsonState, nil, "dev", "test-project")
    require.NoError(t, err)
    require.NotNil(t, mm)

    // Should have pet module entries for count instances
    require.Contains(t, mm.Modules, "pet[0]")
    require.Contains(t, mm.Modules, "pet[1]")

    // Each entry should have resources
    pet0 := mm.Modules["pet[0]"]
    require.Equal(t, "module.pet[0]", pet0.TerraformPath)
    require.Equal(t, "0", pet0.IndexKey)
    require.Equal(t, "count", pet0.IndexType)
    require.Greater(t, len(pet0.Resources), 0)

    // Should have interface with inputs
    require.NotNil(t, pet0.Interface)
    require.Greater(t, len(pet0.Interface.Inputs), 0)

    // prefix input should have evaluatedValue
    var prefixInput *ModuleInterfaceField
    for i := range pet0.Interface.Inputs {
        if pet0.Interface.Inputs[i].Name == "prefix" {
            prefixInput = &pet0.Interface.Inputs[i]
            break
        }
    }
    require.NotNil(t, prefixInput, "should have prefix input")
    require.NotNil(t, prefixInput.EvaluatedValue, "prefix should have evaluatedValue")
    require.NotNil(t, prefixInput.Expression, "prefix should have expression")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/ -run TestBuildModuleMap_IndexedModules -v -count=1
```

- [ ] **Step 3: Implement ModuleMap types and BuildModuleMap**

```go
// pkg/module_map.go
package pkg

import (
    "encoding/json"
    "fmt"
    "os"
    // imports from spike findings
)

type ModuleMap struct {
    Modules map[string]*ModuleMapEntry `json:"modules"`
}

type ModuleMapEntry struct {
    TerraformPath string                     `json:"terraformPath"`
    Source        string                     `json:"source,omitempty"`
    IndexKey      string                     `json:"indexKey,omitempty"`
    IndexType     string                     `json:"indexType,omitempty"`
    Resources     []string                   `json:"resources"`
    Interface     *ModuleInterface           `json:"interface,omitempty"`
    Modules       map[string]*ModuleMapEntry `json:"modules"`
}

type ModuleInterface struct {
    Inputs  []ModuleInterfaceField `json:"inputs"`
    Outputs []ModuleInterfaceField `json:"outputs"`
}

type ModuleInterfaceField struct {
    Name           string      `json:"name"`
    Type           interface{} `json:"type,omitempty"`
    Required       bool        `json:"required,omitempty"`
    Default        interface{} `json:"default,omitempty"`
    Description    string      `json:"description,omitempty"`
    Expression     string      `json:"expression,omitempty"`
    EvaluatedValue interface{} `json:"evaluatedValue,omitempty"`
}

// BuildModuleMap constructs a ModuleMap from config, evaluation scope, and state.
func BuildModuleMap(
    config *configs.Config,
    scope *lang.Scope, // nil if evaluation unavailable
    tfjsonState *tfjson.State, // for resource matching
    pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
    stackName, projectName string,
) (*ModuleMap, error) {
    mm := &ModuleMap{
        Modules: map[string]*ModuleMapEntry{},
    }
    // Build entries from config.Module.ModuleCalls
    for name, call := range config.Module.ModuleCalls {
        entry, err := buildModuleMapEntryFromConfig(name, call, config, scope, tfjsonState, pulumiProviders, stackName, projectName)
        if err != nil {
            return nil, err
        }
        mm.Modules[moduleMapKeyFromCall(name, call)] = entry
    }
    return mm, nil
}

func WriteModuleMap(mm *ModuleMap, path string) error {
    data, err := json.MarshalIndent(mm, "", "  ")
    if err != nil {
        return fmt.Errorf("marshaling module map: %w", err)
    }
    data = append(data, '\n')
    return os.WriteFile(path, data, 0o600)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/ -run TestBuildModuleMap_IndexedModules -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: add ModuleMap types and BuildModuleMap from configs.Config + scope"
```

---

### Task 10: Module map with tofu show JSON (reduced fidelity)

**Files:**
- Modify: `pkg/module_map_test.go`
- Modify: `pkg/module_map.go`

- [ ] **Step 1: Write failing test**

```go
func TestBuildModuleMap_TofuShowJson(t *testing.T) {
    t.Parallel()
    config, err := LoadConfig("testdata/tf_indexed_modules")
    require.NoError(t, err)

    tfjsonState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
        StateFilePath: "testdata/tofu_state_indexed_modules.json",
    })
    require.NoError(t, err)

    // nil scope — no evaluation available
    mm, err := BuildModuleMap(config, nil, tfjsonState, nil, "dev", "test-project")
    require.NoError(t, err)

    // Should still have module structure
    require.Contains(t, mm.Modules, "pet[0]")
    pet0 := mm.Modules["pet[0]"]
    require.Greater(t, len(pet0.Resources), 0)

    // Interface should have declarations but no evaluatedValue
    require.NotNil(t, pet0.Interface)
    for _, input := range pet0.Interface.Inputs {
        require.Nil(t, input.EvaluatedValue, "should not have evaluatedValue without scope")
    }
}
```

- [ ] **Step 2: Run test, implement nil-scope path, verify pass**

```bash
go test ./pkg/ -run TestBuildModuleMap_TofuShowJson -v -count=1
```

- [ ] **Step 3: Commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: support module map building without evaluation scope (tofu show json path)"
```

---

### Task 11: Migrate ctyValueToInterface and propertyValueToInterface

**Files:**
- Modify: `pkg/module_map.go`
- Modify: `pkg/module_map_test.go`

- [ ] **Step 1: Copy ctyValueToInterface tests from old convert_test.go**

Refer to the original `pkg/component_metadata_test.go:TestCtyValueToInterface` (lines 146-167) and migrate to `pkg/module_map_test.go`.

- [ ] **Step 2: Copy propertyValueToInterface tests**

Refer to the original `pkg/component_map.go:propertyValueToInterface` function behavior.

- [ ] **Step 3: Implement both functions in module_map.go**

Copy the functions from the old code (they're pure utility functions with no dependencies on removed code).

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./pkg/ -run "TestCtyValueToInterface|TestPropertyValueToInterface" -v -count=1
```

- [ ] **Step 5: Commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: migrate ctyValueToInterface and propertyValueToInterface to module_map"
```

---

### Task 12: Expression field in module map

**Files:**
- Modify: `pkg/module_map.go`
- Modify: `pkg/module_map_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestBuildModuleMap_ExpressionField(t *testing.T) {
    t.Parallel()
    // Use a fixture where expressions are visible
    // e.g., prefix = "test-${count.index}"
    config, err := LoadConfig("testdata/tf_indexed_modules")
    require.NoError(t, err)

    mm, err := BuildModuleMap(config, nil, nil, nil, "dev", "test-project")
    require.NoError(t, err)

    pet0 := mm.Modules["pet[0]"]
    require.NotNil(t, pet0.Interface)

    var prefixInput *ModuleInterfaceField
    for i := range pet0.Interface.Inputs {
        if pet0.Interface.Inputs[i].Name == "prefix" {
            prefixInput = &pet0.Interface.Inputs[i]
            break
        }
    }
    require.NotNil(t, prefixInput)
    require.NotEmpty(t, prefixInput.Expression, "should have raw HCL expression text")
}
```

- [ ] **Step 2: Implement expression extraction from call.Config attrs**

Use `attr.Expr.Range().SliceBytes(file.Bytes)` or similar HCL API to get raw expression text.

- [ ] **Step 3: Run test, verify pass, commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: include raw HCL expression text in module map inputs"
```

---

## Chunk 4 (PR 4: `feat/module-map-cmd`): CLI Subcommand + Integration

### Task 13: module-map cobra subcommand

**Files:**
- Create: `cmd/module_map.go`

- [ ] **Step 1: Write the subcommand**

```go
// cmd/module_map.go
package cmd

import (
    "fmt"
    "github.com/pulumi/pulumi-tool-terraform-migrate/pkg"
    "github.com/spf13/cobra"
)

func newModuleMapCmd() *cobra.Command {
    var from string
    var stateFile string
    var out string
    var pulumiStack string
    var pulumiProject string

    cmd := &cobra.Command{
        Use:   "module-map",
        Short: "Generate a module map from Terraform sources and state",
        Long: `Generate a module-map.json file describing the Terraform module hierarchy,
resource URNs, and evaluated module interfaces.

Requires 'tofu init' to have been run in the --from directory.

Example:

  pulumi-terraform-migrate module-map \
    --from path/to/terraform-sources \
    --state-file path/to/terraform.tfstate \
    --out /tmp/module-map.json \
    --pulumi-stack dev \
    --pulumi-project my-project
`,
        RunE: func(cmd *cobra.Command, args []string) error {
            return pkg.GenerateModuleMap(cmd.Context(), from, stateFile, out, pulumiStack, pulumiProject)
        },
    }

    cmd.Flags().StringVarP(&from, "from", "f", "", "Path to the Terraform root folder (must have .terraform/ from tofu init)")
    cmd.Flags().StringVar(&stateFile, "state-file", "", "Path to terraform.tfstate or tofu show -json output")
    cmd.Flags().StringVarP(&out, "out", "o", "", "Where to write module-map.json")
    cmd.Flags().StringVar(&pulumiStack, "pulumi-stack", "", "Pulumi stack name (for URN construction)")
    cmd.Flags().StringVar(&pulumiProject, "pulumi-project", "", "Pulumi project name (for URN construction)")

    cmd.MarkFlagRequired("from")
    cmd.MarkFlagRequired("state-file")
    cmd.MarkFlagRequired("out")
    cmd.MarkFlagRequired("pulumi-stack")
    cmd.MarkFlagRequired("pulumi-project")

    return cmd
}

func init() {
    rootCmd.AddCommand(newModuleMapCmd())
}
```

- [ ] **Step 2: Implement GenerateModuleMap orchestrator in pkg**

```go
// pkg/module_map.go (add to existing file)

// GenerateModuleMap is the top-level entry point for the module-map subcommand.
func GenerateModuleMap(ctx context.Context, tfDir, stateFilePath, outputPath, stackName, projectName string) error {
    // 1. Load config
    config, err := LoadConfig(tfDir)
    if err != nil {
        return fmt.Errorf("loading config: %w", err)
    }

    // 2. Detect state format and load
    format, err := DetectStateFormat(stateFilePath)
    if err != nil {
        return fmt.Errorf("detecting state format: %w", err)
    }

    var scope *lang.Scope
    var tfjsonState *tfjson.State

    switch format {
    case StateFormatRaw:
        rawState, err := LoadRawState(stateFilePath)
        if err != nil {
            return fmt.Errorf("loading raw state: %w", err)
        }
        scope, err = Evaluate(config, rawState, tfDir)
        if err != nil {
            // Graceful degradation — continue without evaluation
            fmt.Fprintf(os.Stderr, "Warning: evaluation failed, module map will not include evaluatedValue: %v\n", err)
        }
        // Build tfjson-compatible resource list from *states.State for resource matching.
        // Cannot use tofu.LoadTerraformState here — raw tfstate is a different format.
        // Either: convert *states.State to resource list in-memory,
        // or have BuildModuleMap accept *states.State directly.
        // Exact approach depends on spike findings.
        tfjsonState = convertRawStateToResourceList(rawState) // pseudocode — implement based on spike

    case StateFormatShowJSON:
        tfjsonState, err = tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
            StateFilePath: stateFilePath,
        })
        if err != nil {
            return fmt.Errorf("loading tofu show state: %w", err)
        }
    }

    // 3. Resolve Pulumi providers for URN construction
    var pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata
    if tfjsonState != nil {
        pulumiProviders, err = GetPulumiProvidersForTerraformState(tfjsonState, nil)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Warning: could not resolve Pulumi providers: %v\n", err)
        }
    }

    // 4. Build module map
    mm, err := BuildModuleMap(config, scope, tfjsonState, pulumiProviders, stackName, projectName)
    if err != nil {
        return fmt.Errorf("building module map: %w", err)
    }

    // 5. Write output
    if err := WriteModuleMap(mm, outputPath); err != nil {
        return fmt.Errorf("writing module map: %w", err)
    }

    return nil
}
```

- [ ] **Step 3: Build and verify the command registers**

```bash
go build ./...
go run . module-map --help
```

- [ ] **Step 4: Commit**

```bash
git add cmd/module_map.go pkg/module_map.go
git commit -m "feat: add module-map cobra subcommand"
```

---

### Task 14: Integration test with dns-to-db fixture

**Files:**
- Modify: `pkg/module_map_test.go`

- [ ] **Step 1: Write integration test**

```go
func TestModuleMap_DnsToDb(t *testing.T) {
    t.Parallel()
    if _, err := os.Stat("testdata/tf_dns_to_db/.terraform/modules/modules.json"); os.IsNotExist(err) {
        t.Skip("requires tofu init on tf_dns_to_db fixture")
    }

    config, err := LoadConfig("testdata/tf_dns_to_db")
    require.NoError(t, err)

    ctx := context.Background()
    tfjsonState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
        StateFilePath: "testdata/tofu_state_dns_to_db.json",
    })
    require.NoError(t, err)

    pulumiProviders, err := GetPulumiProvidersForTerraformState(tfjsonState, nil)
    require.NoError(t, err)

    // Without scope (tofu show json path)
    mm, err := BuildModuleMap(config, nil, tfjsonState, pulumiProviders, "dev", "test-project")
    require.NoError(t, err)

    // Should have ~18 top-level module entries
    require.GreaterOrEqual(t, len(mm.Modules), 10, "should have entries for most modules")

    // Count total resources across all modules
    totalResources := 0
    var countResources func(entry *ModuleMapEntry)
    countResources = func(entry *ModuleMapEntry) {
        totalResources += len(entry.Resources)
        for _, child := range entry.Modules {
            countResources(child)
        }
    }
    for _, entry := range mm.Modules {
        countResources(entry)
    }
    require.GreaterOrEqual(t, totalResources, 80, "should have ~83 resource URNs total")
}
```

- [ ] **Step 2: Run test, iterate until passing**

```bash
go test ./pkg/ -run TestModuleMap_DnsToDb -v -count=1
```

- [ ] **Step 3: Commit**

```bash
git add pkg/module_map_test.go
git commit -m "test: add dns-to-db integration test for module map"
```

---

### Task 15: Additional spec-required tests

**Files:**
- Modify: `pkg/module_map_test.go`
- Modify: `pkg/tofu_eval_test.go`

These tests cover behaviors specified in the spec's testing strategy but not yet addressed.

- [ ] **Step 1: TestModuleMap_TfvarsResolution**

Verify that tfvars values flow through to module call-site `evaluatedValue`. Use the `tf_tfvars_resolution` fixture.

- [ ] **Step 2: TestModuleMap_DefaultsNotInEvaluatedValue**

Verify that variable defaults do NOT appear in `evaluatedValue` — only call-site arguments do. This preserves the behavior from the original `TestPopulateComponentsFromHCL_VariableDefaultsNotMerged`.

- [ ] **Step 3: TestModuleMap_NestedModuleEvaluation**

Verify that nested module call-site expressions are evaluated correctly (child module uses parent's var scope). Use `parent_with_nested_module` fixture if available, or capture new testdata.

- [ ] **Step 4: TestModuleMap_NoProviders_GracefulDegradation**

Verify that when `.terraform/providers/` is missing, the command warns and produces a module map without `evaluatedValue` fields (same as tofu-show-json path).

- [ ] **Step 5: TestModuleMap_PerExpressionFailure**

Verify that a single expression evaluation failure doesn't block other fields or modules.

- [ ] **Step 6: Run all new tests**

```bash
go test ./pkg/ -run "TestModuleMap_Tfvars|TestModuleMap_Defaults|TestModuleMap_Nested|TestModuleMap_NoProviders|TestModuleMap_PerExpression" -v -count=1
```

- [ ] **Step 7: Commit**

```bash
git add pkg/module_map_test.go pkg/tofu_eval_test.go
git commit -m "test: add graceful degradation, tfvars, defaults, and nested module tests"
```

---

### Task 16: Full test suite verification

**Files:** None (verification only)

- [ ] **Step 1: Run all tests**

```bash
go test ./pkg/... -count=1
go test ./cmd/... -count=1
```

- [ ] **Step 2: Build and vet**

```bash
go build ./...
go vet ./...
```

- [ ] **Step 3: Manual smoke test**

```bash
go run . module-map \
  --from pkg/testdata/tf_dns_to_db \
  --state-file pkg/testdata/tofu_state_dns_to_db.json \
  --pulumi-stack dev --pulumi-project dns-to-db \
  --out /tmp/module-map-test.json

cat /tmp/module-map-test.json | python3 -m json.tool | head -50

go run . stack \
  --from pkg/testdata/tf_dns_to_db \
  --state-file pkg/testdata/tofu_state_dns_to_db.json \
  --pulumi-stack dev --pulumi-project dns-to-db \
  --to /tmp/stack-test --out /tmp/stack-test/pulumi-state.json

# Verify no module-map.json or component-map.json in /tmp/stack-test/
ls /tmp/stack-test/
```

- [ ] **Step 4: Commit any fixes**

---

## Chunk 5 (PR 5: `feat/refactor-to-components-skill`): Skill

### Task 17: Write SKILL.md

**Files:**
- Create: `docs/superpowers/skills/refactor-to-components/SKILL.md`

- [ ] **Step 1: Write the skill file**

Follow the workflow from spec Part 7. The skill:
1. Reads `module-map.json`
2. Presents inventory table
3. Guides component mapping review
4. Generates component classes per-module
5. Generates main program with transforms
6. Verifies with `pulumi preview`

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/skills/refactor-to-components/SKILL.md
git commit -m "feat: add refactor-to-components skill"
```

---

### Task 18: Write skill reference documents

**Files:**
- Create: `docs/superpowers/skills/refactor-to-components/references/alias-wiring-pattern.md`
- Create: `docs/superpowers/skills/refactor-to-components/references/module-map-format.md`
- Create: `docs/superpowers/skills/refactor-to-components/references/existing-component-integration.md`

- [ ] **Step 1: Write alias-wiring-pattern.md**

Document the transform-based alias pattern with TypeScript and Python examples.

- [ ] **Step 2: Write module-map-format.md**

Document the `module-map.json` schema with field descriptions.

- [ ] **Step 3: Write existing-component-integration.md**

Document how to map TF modules to existing Pulumi components.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/skills/refactor-to-components/references/
git commit -m "feat: add refactor-to-components skill reference documents"
```

---

## Chunk 6: Submit PR Stack

### Task 19: Submit stack with git spice

- [ ] **Step 1: Push all branches**

```bash
git spice stack submit --fill
```

- [ ] **Step 2: Verify all PRs on pulumi-proserv fork**

Each PR should:
- Pass `go build ./...` and `go test ./...` independently
- Have a clear description of what it adds
- Reference the spec

- [ ] **Step 3: File request for pulumi/opentofu fork exports**

Create an issue or reach out to the fork owner with:
- Which packages need exporting: `configs`, `configs/configload`, `tofu`, provider plugin loading
- Why: to use `Context.Eval()` for module call-site expression evaluation
- Current workaround: `replace` directive with local checkout
