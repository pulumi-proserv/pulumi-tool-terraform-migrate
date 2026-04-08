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

### Step 1: Create evaluation scope

```go
ctx := tofu.NewContext(&tofu.ContextOpts{
    Plugins: pluginLibrary,
})
scope, diags := ctx.Eval(evalCtx, config, state, addrs.RootModuleInstance, &tofu.EvalOpts{
    SetVariables: inputVariables, // from terraform.tfvars + *.auto.tfvars
})
```

Returns `*lang.Scope` with the entire root module namespace populated: variables, locals, resource attributes, module outputs — all resolved in correct dependency order by OpenTofu's DAG walker.

### Step 2: Extract module call-site argument values

For each module call in `config.Module.ModuleCalls`:

```go
for name, call := range config.Module.ModuleCalls {
    attrs, _ := call.Config.JustAttributes()
    for attrName, attr := range attrs {
        val, diags := scope.EvalExpr(attr.Expr, cty.DynamicPseudoType)
        if diags.HasErrors() {
            // Log warning, skip this input — module map entry omits evaluatedValue
            fmt.Fprintf(os.Stderr, "Warning: could not evaluate %s.%s: %s\n", name, attrName, diags.Err())
            continue
        }
        // val is the evaluated cty.Value -> goes into evaluatedValue
    }
}
```

For nested modules, we need the child module's scope. Open question to verify during prototyping: does `Context.Eval()` with `RootModuleInstance` make child scopes accessible, or do we need to call `Eval()` per module instance?

### Error handling strategy

`Context.Eval()` may produce diagnostics (warnings or errors) for various reasons: missing providers, state drift, unresolvable expressions. The strategy is **graceful degradation**:

- **Config load failure** — fatal error, cannot proceed
- **State load failure** — fatal error, cannot proceed
- **Provider load failure** — warning + continue without expression evaluation (same as `tofu show -json` path: module map without `evaluatedValue`)
- **`Context.Eval()` failure** — warning + continue without expression evaluation
- **Per-expression eval failure** — warning, omit `evaluatedValue` for that field, continue with remaining fields
- **Per-module eval failure** — warning, omit all `evaluatedValue` for that module, continue with remaining modules

The module map is always produced. `evaluatedValue` fields are best-effort.

### Step 3: Extract declarations

Directly from `configs.Config`, no evaluation needed:

```go
childConfig := config.Children["vpc"]
childConfig.Module.Variables  // name, type, default, description
childConfig.Module.Outputs    // name, description, expression
```

Replaces `ParseModuleVariables()` and `ParseModuleOutputs()`.

### Step 4: Count/for_each expansion

OpenTofu's `ModuleExpansionTransformer` handles this natively. Each instance gets its own `count.index` or `each.key`/`each.value` in scope. To verify during prototyping: how per-instance values are represented in the scope.

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

Input fields include `expression` (the raw HCL expression text from the call site, e.g., `"var.vpc_cidr"`) alongside `evaluatedValue`. This helps the `refactor-to-components` skill understand how values are derived and produce better code (e.g., using a variable reference instead of a hardcoded value).

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

### Removed

- `pkg/hcl/evaluator.go` — replaced by `Context.Eval()` scope
- `pkg/hcl/parser.go` — replaced by `configs.Config` (ModuleCalls, Variables, Outputs)
- `pkg/hcl/discovery.go` — replaced by `configload.Loader` (module source resolution from cache)
- `pkg/hcl/convert.go` — `CtyValueToPulumiPropertyValue` moves to `pkg/module_map.go`; `PulumiPropertyMapToCtyMap` no longer needed
- `pkg/component_populate.go` — entire custom orchestration
- `pkg/component_metadata.go` — interface data now built directly from `configs.Config` in module map builder
- `pkg/component_schema.go` — schema validation moves to `refactor-to-components` skill
- `pkg/component_map.go` — renamed/rewritten as `pkg/module_map.go`
- Associated test files: `pkg/component_populate_test.go`, `pkg/component_metadata_test.go`, `pkg/component_schema_test.go`, `pkg/hcl/evaluator_test.go`, `pkg/hcl/parser_test.go`, `pkg/hcl/discovery_test.go`, `pkg/hcl/convert_test.go`
- Note: tests for `CtyValueToPulumiPropertyValue` (currently in `pkg/hcl/convert_test.go`) should be migrated to `pkg/module_map_test.go` alongside the moved function

### Modified

- `pkg/state_adapter.go` — remove `ComponentMapData`, `ComponentMetadata`, `PulumiProviders` from `TranslateStateResult`. Remove module map writing from `TranslateAndWriteState`.
- `pkg/pulumi_state.go` — remove `ComponentMetadata` field
- `pkg/module_tree.go` — keep address parsing and tree construction; may simplify since `configs.Config` provides module hierarchy natively

### Added

- `cmd/module_map.go` — new subcommand
- `pkg/module_map.go` — module map builder using `configs.Config` + `lang.Scope`, includes `ModuleMap`/`ModuleMapEntry` types (renamed from `ComponentMap`)
- `pkg/tofu_eval.go` — wrapper around `Context.Eval()` setup (config loading, state loading, provider loading, scope creation)

### Net effect

Remove ~2500 lines of custom evaluation + parsing. Add ~500 lines of OpenTofu integration wiring (plus tests).

---

## Part 9: Open Questions for Prototyping

These will be resolved during implementation with a local `replace` directive on the `pulumi/opentofu` fork:

1. **Child module scopes** — Does `Context.Eval()` with `RootModuleInstance` make child module scopes accessible? Or do we need `Eval()` per module instance?
2. **Count/for_each instances** — How are per-instance evaluated values represented in the scope?
3. **Provider plugin loading** — Exact package paths for creating provider factories from `.terraform/providers/` cache.
4. **tfvars loading** — How to populate `SetVariables` from `terraform.tfvars` + `*.auto.tfvars`. May be handled by config loading or need separate parsing.
5. **`configs.RootModuleCallForTesting()`** — Is this the right entry point for `LoadConfig`, or is there a production-oriented call? This function name strongly suggests test-only usage; the fork may need to export a production equivalent.

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
| `TestStack_NoModuleMapOutput` | Stack command no longer produces component-map.json or component-schemas.json |

### Testdata

Reuse existing fixtures in `pkg/testdata/` — these were captured from real stack operations (`tofu show -json`, real `.tfstate` files, real TF source directories with `.terraform/modules/` caches).

For new tests that need fixtures not in the existing set (e.g., raw `.tfstate` format), capture from real `tofu` operations against real infrastructure or local-only providers (random, null, tls). **No handmade testdata** — all state files and TF source directories must come from actual `tofu init` / `tofu apply` / `tofu show` runs.

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

# Stack command (no module-map.json)
go run . stack \
  --from pkg/testdata/tf_dns_to_db \
  --state-file pkg/testdata/tofu_state_dns_to_db.json \
  --pulumi-stack dev --pulumi-project dns-to-db \
  --to /tmp/stack-test --out /tmp/stack-test/pulumi-state.json
# Verify: only pulumi-state.json, no module-map.json
```
