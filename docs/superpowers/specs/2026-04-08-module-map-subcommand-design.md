# Module Map Subcommand + Context.Eval() Integration

## Context

The `pulumi-terraform-migrate` tool translates Terraform state to Pulumi state. The current implementation includes a custom HCL evaluation engine (~2000 lines) that manually walks expressions, resolves variables, evaluates references, and handles module scoping. This reimplements what OpenTofu's `tofu.Context.Eval()` already does natively.

This spec:
1. Replaces the custom HCL evaluation with OpenTofu's `Context.Eval()` (via the `pulumi/opentofu` fork)
2. Extracts module map generation into a standalone `module-map` subcommand
3. Renames "component-map" to "module-map" throughout
4. Creates a `refactor-to-components` skill that consumes the module map

## Decision Record

### Why `Context.Eval()` instead of custom evaluation

OpenTofu's evaluation graph already handles every problem our custom code solves:

| Custom Code | OpenTofu Built-in |
|-------------|-------------------|
| Two-phase evaluation (leaf modules first, then parents) | DAG walker evaluates in dependency order |
| Scoped resource attributes per module instance | `evaluationStateData.GetResource()` scopes to module |
| Iterative locals resolution (10 passes) | `LocalTransformer` + reference edges resolve correctly |
| Child module output propagation | `OutputTransformer` + `ModuleVariableTransformer` |
| Missing resources (count=0) | `ModuleExpansionTransformer` handles count/for_each |
| Meta-arg context (count.index, each.key) | Built into resource/module node evaluation |

### Why not `tofu console` subprocess

Investigated and rejected because:
- `tofu console` returns module **outputs** only, not call-site **input argument values**
- No expression returns module call arguments (what was passed TO a module)
- Only evaluates in root module scope (can't access nested module scopes)
- Would still require parsing HCL to discover expressions, then piping each individually
- The in-process evaluator already uses the same `lang.Scope` functions

### Why actual provider plugins instead of mock schemas

`Context.Eval()` needs provider schemas to decode state JSON (`ctyjson.Unmarshal(attrsJSON, ty)` requires a concrete `cty.Type`). Options considered:
- **Inferred schemas from state JSON** (`ctyjson.ImpliedType`) — cheap but lossy (list vs set, optional attrs)
- **Actual provider plugins** (chosen) — `tofu init` is already required for module cache, so provider binaries are available in `.terraform/providers/`. Correct schemas with no type mismatches.

### Prerequisite: `tofu init`

The `module-map` subcommand requires `tofu init` to have been run in the `--from` directory. This provides:
- `.terraform/modules/` — module source cache for config loading
- `.terraform/providers/` — provider binaries for schema resolution

This is a harder requirement than the current `stack` command, which works with just a state file. The trade-off is justified: `Context.Eval()` with real providers gives correct, battle-tested evaluation vs our custom code which had known edge cases.

When providers are unavailable (no `.terraform/providers/`), the command should emit a clear error: `"module-map requires 'tofu init' to have been run in the --from directory for provider schemas and module resolution."`

### Fork dependency

The `pulumi/opentofu` fork already exports `lang`, `states`, `addrs`, `encryption`, `states/statefile`. This design requires additional exports:
- `configs` — config parsing, `ModuleCall`, `Module.Variables/Outputs`
- `configs/configload` — loading config from disk + module cache
- `tofu` — `Context`, `ContextOpts`, `Eval()`
- Provider plugin loading packages (exact paths TBD during prototyping)

Since someone else owns the fork, we'll prototype with a `replace` directive pointing at a local checkout, then file a request for the exports.

---

## Part 1: Architecture

### Pipeline

```
TF source dir + state file + .terraform/
         |
    +----------------------------+
    | 1. Load                    |
    |  - configs.Config (HCL)    |
    |  - states.State (tfstate)  |
    |  - Provider plugins        |
    +-------------+--------------+
                  |
    +-------------+--------------+
    | 2. Evaluate                |
    |  - Context.Eval() -> Scope |
    |  - For each ModuleCall:    |
    |    eval expressions        |
    |    against parent scope    |
    +-------------+--------------+
                  |
    +-------------+--------------+
    | 3. Build + Write           |
    |  - module-map.json         |
    +----------------------------+
```

### Command separation

| Command | Produces | Concerns |
|---------|----------|----------|
| `stack` | `pulumi-state.json`, `required-plugins.json` (optional) | State translation only |
| `module-map` | `module-map.json` | Module mapping only |

The two commands share provider loading code but are otherwise independent.

---

## Part 2: Load Stage

### `.terraform/` location

The `.terraform/` directory is expected at `<--from>/.terraform/`. This follows Terraform's default convention. The `TF_DATA_DIR` environment variable is not supported in v1; if needed, a `--tf-data-dir` flag can be added later.

### Config loading

```go
loader := configload.NewLoader(&configload.Config{
    ModulesDir: filepath.Join(tfDir, ".terraform/modules"),
})
config, diags := loader.LoadConfig(ctx, tfDir, configs.RootModuleCallForTesting())
```

Returns `*configs.Config` — a tree where:
- `config.Module.ModuleCalls` -> `map[string]*ModuleCall` with raw `hcl.Expression` arguments
- `config.Module.Variables` -> declared variables with types/defaults
- `config.Module.Outputs` -> output blocks with expressions
- `config.Children` -> child module configs (recursively)

Config loading is pure HCL parsing. No providers required.

### State loading

The `--state-file` flag accepts two formats, auto-detected by peeking at the JSON:

- **Raw `.tfstate`** (has `"version": 4`): Read via `statefile.Read()` -> `*states.State`. Full `Context.Eval()` with expression evaluation. Module map includes `evaluatedValue` fields.
- **`tofu show -json`** (has `"format_version": "1.0"`): Read via `tfjson.State`. Resource listing + URN construction only. Module map produced **without** `evaluatedValue` fields (module tree, resources, variable/output declarations still available from config).

The `tofu show -json` format is lossy (missing `AttrsJSON` as raw bytes, `Private` metadata, proper sensitive paths). Converting to `*states.State` is not feasible, so it operates at reduced fidelity.

Auto-detection heuristic: if the JSON has a `"format_version"` key, it's `tofu show -json` output. Otherwise it's a raw `.tfstate` (which may be version 3 or 4).

### Provider plugin loading

Providers live in `.terraform/providers/` after `tofu init`. The load stage:
1. Discovers required providers from `config.Module.ProviderRequirements`
2. Loads binaries from the local cache
3. Creates `providers.Factory` entries
4. Passes to `tofu.NewContext()`

Exact package paths for provider factory creation TBD during prototyping with the fork.

---

## Part 3: Evaluate Stage

### Spike finding: evaluate `var.*` in child scope, not call-site expressions

The prototype spike revealed that the right approach is **not** to evaluate call-site expressions (e.g., `prefix = "test-${count.index}"`) in the parent scope. Instead:

- Call `Context.Eval()` **per child module instance** (e.g., `addrs.RootModuleInstance.Child("pet", addrs.IntKey(0))`)
- Evaluate `var.<name>` in the child scope to get the already-resolved input value

This works because OpenTofu's evaluation graph already resolves variable assignments through the module call. The child scope contains the final evaluated values.

**Why not call-site expressions:** The root scope cannot evaluate call-site expressions that reference `count.index` or `each.key` because the root scope is not in a counted context. Evaluating `call.Config.JustAttributes()` expressions fails with "Reference to count in non-counted context."

### Step 1: Create evaluation context

```go
tofuCtx, diags := tofu.NewContext(&tofu.ContextOpts{
    Providers: providerFactories, // from providercache
})
```

Provider factories are built from `.terraform/providers/` using `providercache.NewDir()` + `AllAvailablePackages()` + go-plugin GRPC setup. See spike findings for exact wiring.

### Step 2: Evaluate per module instance

For each module instance (accounting for count/for_each expansion):

```go
// For a module with count=2:
for i := 0; i < count; i++ {
    childAddr := addrs.RootModuleInstance.Child("pet", addrs.IntKey(i))
    childScope, diags := tofuCtx.Eval(ctx, config, state, childAddr, &tofu.EvalOpts{})

    // Read evaluated variable values from child scope
    for varName := range childConfig.Module.Variables {
        expr, _ := hclsyntax.ParseExpression([]byte("var."+varName), "<eval>", hcl.Pos{Line: 1, Column: 1})
        val, _ := childScope.EvalExpr(expr, cty.DynamicPseudoType)
        // val is the evaluated input value → goes into evaluatedValue
    }
}
```

For nested modules, use chained addressing: `addrs.RootModuleInstance.Child("vpc", addrs.NoKey).Child("subnets", addrs.IntKey(0))`.

### Step 3: Determine instance count

To know how many instances exist for count/for_each modules, check the state. Resources in state have addresses like `module.pet[0].random_pet.this` — collect unique module instance keys from resource addresses.

Alternatively, evaluate `count` or `for_each` expressions in the root scope to get the count/key set.

### Step 4: Extract declarations

Directly from `configs.Config`, no evaluation needed:

```go
childConfig := config.Children["vpc"]
childConfig.Module.Variables  // name, type, default, description
childConfig.Module.Outputs    // name, description, expression
```

Replaces `ParseModuleVariables()` and `ParseModuleOutputs()`.

### Error handling strategy

`Context.Eval()` may produce diagnostics (warnings or errors) for various reasons: missing providers, state drift, unresolvable expressions. The strategy is **graceful degradation**:

- **Config load failure** — fatal error, cannot proceed
- **State load failure** — fatal error, cannot proceed
- **Provider load failure** — warning + continue without expression evaluation (same as `tofu show -json` path: module map without `evaluatedValue`)
- **`Context.Eval()` per-instance failure** — warning, omit `evaluatedValue` for that instance, continue with remaining instances
- **Per-variable eval failure** — warning, omit `evaluatedValue` for that field, continue with remaining fields

The module map is always produced. `evaluatedValue` fields are best-effort.

---

## Part 4: Build + Write Stage

### Module map construction

Walk `configs.Config` tree + evaluated values to build output:

- **`terraformPath`** — from module call address
- **`source`** — from `configs.ModuleCall.SourceAddr`
- **`indexKey`/`indexType`** — from expanded module instances
- **`resources`** — match state resources to module path, build URNs using `terraform-bridge` for type token resolution
- **`interface.inputs`** — variable declarations from config + `evaluatedValue` from scope
- **`interface.outputs`** — output declarations from config + evaluated values from scope
- **`modules`** — recurse into `config.Children`

### Output files

- `module-map.json` — module hierarchy with resources + evaluated interfaces

`module-schemas.json` from the original plan is **dropped**. Its data (variable types, defaults, descriptions, output declarations) is already embedded in `module-map.json`'s `interface` fields. A separate file added complexity without value — the `refactor-to-components` skill reads everything it needs from `module-map.json`.

### module-map.json schema

Input `evaluatedValue` comes from evaluating `var.<name>` in the child module instance scope. The `expression` field contains the raw HCL expression from the call site (obtained via `call.Config.JustAttributes()`) — this helps the `refactor-to-components` skill understand how values are derived.

Note: `expression` is available from config parsing regardless of evaluation success. `evaluatedValue` requires successful `Context.Eval()` with providers.

```json
{
  "modules": {
    "alb": {
      "terraformPath": "module.alb",
      "source": ".terraform/modules/alb",
      "resources": [
        "urn:pulumi:dev::project::aws:lb/loadBalancer:LoadBalancer::alb_this[0]",
        "urn:pulumi:dev::project::aws:lb/listener:Listener::alb_this[\"my-https-listener\"]"
      ],
      "interface": {
        "inputs": [
          {"name": "domain_name", "type": "string", "expression": "var.domain", "evaluatedValue": "example.com"}
        ],
        "outputs": [
          {"name": "lb_arn", "description": "The ARN of the load balancer"}
        ]
      },
      "modules": {}
    },
    "ec2_private_app1[0]": {
      "terraformPath": "module.ec2_private_app1[0]",
      "indexKey": "0",
      "indexType": "count",
      "resources": ["urn:..."],
      "interface": {},
      "modules": {}
    }
  }
}
```

---

## Part 5: CLI

```
pulumi-terraform-migrate module-map \
  --from <tf-source-dir> \
  --state-file <terraform.tfstate or tofu-show.json> \
  --out <module-map.json> \
  --pulumi-stack <stack-name> \
  --pulumi-project <project-name>
```

- `--from` is required (HCL source essential for module map)
- `--state-file` accepts both raw `.tfstate` and `tofu show -json` (auto-detected)
- `--out` specifies the `module-map.json` file path directly
- No `--to` (Pulumi project dir not needed)
- No `--module-source-map` (config loader resolves all sources via `.terraform/modules/`)
- No `--module-schema` (validation deferred to `refactor-to-components` skill)

---

## Part 6: Changes to `stack` Command

Remove from `TranslateAndWriteState`:
- `buildComponentMap()` call and `component-map.json` writing
- `WriteComponentSchemaMetadata()` call and `component-schemas.json` writing
- `ComponentMapData`, `ComponentMetadata`, and `PulumiProviders` fields from `TranslateStateResult`
- `ComponentMetadata` field from `PulumiState`
- Component tree building and `populateComponentsFromHCL()` call from `convertState()`

`stack` produces only `pulumi-state.json` + optional `required-plugins.json`.

**Note:** Since the implementation branches from `main` (not the feature stack), these removals are not needed — the component-map code was never merged to main. This section documents the intent for completeness.

---

## Part 7: `refactor-to-components` Skill

### Purpose

A Claude Code skill that reads `module-map.json` and guides the user through restructuring flat Pulumi state into component resources.

### Skill structure

```
refactor-to-components/
  SKILL.md
  references/
    alias-wiring-pattern.md
    module-map-format.md
    existing-component-integration.md
```

### Workflow

1. **Load module-map.json** — parse hierarchy, present inventory table
2. **Component mapping review** — default 1:1 TF module to Pulumi component. User can merge modules, keep flat, map to existing components, move resources between groups.
3. **Per-module generation** — for each: propose component class, user approves, generate code using `pulumi-component` skill patterns. Batch mode for 15+ similar modules.
4. **Generate main program** — instantiate components with `evaluatedValue` inputs + migration transforms
5. **Verification** — `pulumi preview` (zero changes), `pulumi up`, remove migration artifacts, `pulumi preview` again

### Alias wiring via transforms

Aliases are applied externally using Pulumi's `transformations` API. The component classes have no migration awareness.

Generated artifacts in the user's project:
1. Component classes (clean, no migration code)
2. `migration-aliases.json` — URN map derived from module-map.json, maps new child resource names to old flat URNs
3. Main program with transforms applied at component instantiation:

```typescript
import aliasMap from "./migration-aliases.json";

const vpc = new Vpc("vpc", { ...inputs }, {
  transformations: [(args) => {
    const oldUrn = aliasMap[args.name];
    if (oldUrn) {
      args.opts.aliases = [...(args.opts.aliases || []), { urn: oldUrn }];
    }
    return args;
  }],
});
```

4. Post-migration: delete `migration-aliases.json` + remove transforms. Component code unchanged.

### Existing component integration

When user maps a module to an existing component (e.g., `@pulumi/awsx:ec2:Vpc`):
- Compare URN types against component's child resources
- Compare module-schemas inputs against component args
- Warn on mismatches, don't block
- Generate instantiation only (no new class)
- Alias wiring still needed via transforms

---

## Part 8: Codebase Changes

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

### Cherry-picked from feature stack

- `pkg/module_tree.go` + `pkg/module_tree_test.go` — address parsing, tree construction
- `pkg/testdata/` — real state files and TF source directories

### New files

| File | Responsibility |
|------|---------------|
| `cmd/module_map.go` | Cobra subcommand |
| `pkg/module_map.go` | Types, builder, writer |
| `pkg/tofu_eval.go` | OpenTofu evaluation wrapper |
| `pkg/module_map_test.go` | Tests |
| `pkg/tofu_eval_test.go` | Tests |

### Net effect

Add ~500 lines of OpenTofu integration wiring (plus tests) on a clean `main` base.

---

## Part 9: Spike Findings (Resolved)

Prototype spike resolved all open questions. See `/tmp/module-map-spike/` for spike code.

1. **Child module scopes** — RESOLVED: Call `Eval()` per module instance. `addrs.RootModuleInstance.Child("pet", addrs.IntKey(0))` returns a scope where `var.prefix` = `"test-0"`. Root scope cannot evaluate call-site expressions with `count.index`.

2. **Count/for_each instances** — RESOLVED: Each instance gets its own scope via `Eval()` with the instance address. Instance count determined from state resource addresses.

3. **Provider plugin loading** — RESOLVED: `providercache.NewDir(tfDir + "/.terraform/providers")` → `AllAvailablePackages()` → `CachedProvider.ExecutableFile()` → go-plugin GRPC client → `providers.Factory`. All packages exported from `pulumi/opentofu` fork at `pulumi-main` branch.

4. **tfvars loading** — PARTIALLY RESOLVED: Spike used `&tofu.EvalOpts{}` (no explicit tfvars) and variable values resolved correctly — OpenTofu's graph handles tfvars loading internally when the config dir contains `terraform.tfvars`. Still needs verification with `*.auto.tfvars`.

5. **`configs.RootModuleCallForTesting()`** — RESOLVED: This is the correct entry point. Signature: `loader.LoadConfig(tfDir, configs.RootModuleCallForTesting())` (2 args, no context).

6. **Fork version** — RESOLVED: Use `github.com/pulumi/opentofu@v0.0.0-20250318202137-3146daceaf73` (branch `pulumi-main`). All needed packages exported: `configs`, `configs/configload`, `tofu`, `providers`, `providercache`, `plugin`, `lang`, `states`, `addrs`, `encryption`. Requires HCL replace directive: `github.com/hashicorp/hcl/v2 => github.com/opentofu/hcl/v2`.

7. **Module dir paths** — RESOLVED: `Dir` paths in `.terraform/modules/modules.json` are relative to the TF root dir. The `configload.NewLoader` `ModulesDir` must point to `.terraform/modules/` and the loader resolves module `Dir` relative to the TF root.

---

## Part 10: Testing Strategy (TDD)

Development follows TDD: testdata first, then tests, then implementation. The existing test suite serves as guidance for what behavior to preserve, adapted to the new approach.

### Existing tests to use as guidance

| Original Test | Tests What | New Equivalent |
|--------------|-----------|----------------|
| `TestConvertWithHCLPopulation` | Module call-site inputs evaluated from HCL | `TestModuleMap_InputsEvaluated` — same fixture, verify `evaluatedValue` in module-map output |
| `TestConvertDnsToDb_WithHCLAndModuleCache` | Real-world 18-module stack with remote modules | `TestModuleMap_DnsToDb` — same fixture, verify 18 modules with resources + interfaces |
| `TestConvertDnsToDb_EvalWarningCount` | Zero eval warnings on complex fixture | `TestModuleMap_DnsToDb_NoWarnings` — same zero-warning target |
| `TestConvertMultiResourceModule_WithHCL` | Multi-resource module inputs/outputs | `TestModuleMap_MultiResourceModule` — verify inputs + outputs in module-map |
| `TestConvertComplexExpressions` | count.index, conditionals, string interpolation | `TestModuleMap_ComplexExpressions` — verify per-instance evaluatedValues |
| `TestConvertTfvarsResolution` | tfvars values flow into module inputs | `TestModuleMap_TfvarsResolution` — verify tfvars-derived evaluatedValue |
| `TestBuildComponentSchemaMetadata` | Variable types, defaults, descriptions in metadata | `TestModuleMap_InterfaceDeclarations` — verify type/default/description from configs.Config |
| `TestPopulateComponentsFromHCL_VariableDefaultsNotMerged` | Defaults NOT in evaluatedValue | `TestModuleMap_DefaultsNotInEvaluatedValue` — only call-site args get evaluatedValue |
| `TestPopulateComponentsFromHCL_OutputValuesEvaluated` | Output expressions evaluated from state | `TestModuleMap_OutputsEvaluated` — verify output evaluatedValues from scope |
| `TestPopulateComponentsFromHCL_NestedCallSiteUsesParentVars` | Nested module uses parent's var scope | `TestModuleMap_NestedModuleEvaluation` — Context.Eval() handles this natively |

### New tests (no original equivalent)

| Test | Purpose |
|------|---------|
| `TestModuleMap_RawTfstate` | Full evaluation with raw `.tfstate` format |
| `TestModuleMap_TofuShowJson` | Reduced fidelity — no evaluatedValue, structure still correct |
| `TestModuleMap_StateFormatAutoDetect` | Correct format detection for both input types |
| `TestModuleMap_NoProviders_GracefulDegradation` | Warning + module map without evaluatedValue when providers unavailable |
| `TestModuleMap_PerExpressionFailure` | Individual expression failure doesn't block other fields |
| `TestModuleMap_ExpressionField` | Raw HCL expression text preserved in output |

### Testdata

Reuse existing fixtures in `pkg/testdata/` — these were captured from real stack operations (`tofu show -json`, real `.tfstate` files, real TF source directories with `.terraform/modules/` caches).

For new tests that need fixtures not in the existing set (e.g., raw `.tfstate` format), capture from real `tofu` operations against real infrastructure or local-only providers (random, null, tls). **No handmade testdata** — all state files and TF source directories must come from actual `tofu init` / `tofu apply` / `tofu show` runs.

---

## Part 11: PR Stack

Implementation is delivered as a stack of PRs, each independently reviewable and mergeable:

| PR | Branch | Contents | Depends on |
|----|--------|----------|------------|
| 1 | `feat/module-tree` | Cherry-pick `module_tree.go` + testdata from feature stack | — |
| 2 | `feat/tofu-eval` | `pkg/tofu_eval.go` — config loading, state loading, format detection, Context.Eval() wrapper | PR 1 |
| 3 | `feat/module-map-builder` | `pkg/module_map.go` — types, builder, writer + tests | PR 2 |
| 4 | `feat/module-map-cmd` | `cmd/module_map.go` — CLI subcommand + integration tests | PR 3 |
| 5 | `feat/refactor-to-components-skill` | Skill files (SKILL.md + references/) | PR 4 |

Use `git spice` for stacking. Each PR should pass `go build ./...` and `go test ./...` independently.

---

## Verification

```bash
# Build
go build ./...
go vet ./...

# Unit tests
go test ./pkg/... -count=1

# Module map subcommand (raw tfstate — full evaluation)
go run . module-map \
  --from pkg/testdata/tf_dns_to_db \
  --state-file pkg/testdata/terraform.tfstate \
  --pulumi-stack dev --pulumi-project dns-to-db \
  --out /tmp/module-map-test/module-map.json
# Verify: 83 resource URNs across 18 modules, evaluatedValue populated

# Module map subcommand (tofu show json — reduced fidelity)
go run . module-map \
  --from pkg/testdata/tf_dns_to_db \
  --state-file pkg/testdata/tofu_state_dns_to_db.json \
  --pulumi-stack dev --pulumi-project dns-to-db \
  --out /tmp/module-map-test/module-map.json
# Verify: same structure, no evaluatedValue fields
```
