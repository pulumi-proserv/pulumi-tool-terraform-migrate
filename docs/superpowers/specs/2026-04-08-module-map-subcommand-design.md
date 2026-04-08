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
    |  - module-schemas.json     |
    +----------------------------+
```

### Command separation

| Command | Produces | Concerns |
|---------|----------|----------|
| `stack` | `pulumi-state.json`, `required-plugins.json` (optional) | State translation only |
| `module-map` | `module-map.json`, `module-schemas.json` | Module mapping only |

The two commands share provider loading code but are otherwise independent.

---

## Part 2: Load Stage

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
        val, _ := scope.EvalExpr(attr.Expr, cty.DynamicPseudoType)
        // val is the evaluated cty.Value -> goes into evaluatedValue
    }
}
```

For nested modules, we need the child module's scope. Open question to verify during prototyping: does `Context.Eval()` with `RootModuleInstance` make child scopes accessible, or do we need to call `Eval()` per module instance?

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
- `module-schemas.json` — component interface metadata (types, defaults, descriptions) for code generation

### module-map.json schema

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
          {"name": "domain_name", "type": "string", "evaluatedValue": "example.com"}
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
- No `--to` (Pulumi project dir not needed)
- No `--module-source-map` (config loader resolves all sources via `.terraform/modules/`)
- No `--module-schema` (validation deferred to `refactor-to-components` skill)

---

## Part 6: Changes to `stack` Command

Remove from `TranslateAndWriteState`:
- `buildComponentMap()` call and file writing
- `ComponentMapData` and `PulumiProviders` fields from `TranslateStateResult`
- `component-schemas.json` writing
- `ComponentMetadata` field from `PulumiState`

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

- `pkg/hcl/evaluator.go` — replaced by `Context.Eval()`
- `pkg/component_populate.go` — entire custom orchestration
- `pkg/component_metadata.go` — rebuilds from `configs.Config` directly
- `pkg/component_schema.go` — validation moves to skill
- `pkg/component_map.go` — renamed/rewritten
- Associated test files

### Modified

- `pkg/state_adapter.go` — remove `ComponentMapData`, `ComponentMetadata`, `PulumiProviders` from `TranslateStateResult`. Remove module map writing from `TranslateAndWriteState`.
- `pkg/pulumi_state.go` — remove `ComponentMetadata` field
- `pkg/module_tree.go` — keep address parsing and tree construction; may simplify since `configs.Config` provides module hierarchy natively

### Added

- `cmd/module_map.go` — new subcommand
- `pkg/module_map.go` — module map builder using `configs.Config` + `lang.Scope`
- `pkg/module_schemas.go` — schema metadata builder from `configs.Module.Variables/Outputs`
- `pkg/tofu_eval.go` — wrapper around `Context.Eval()` setup (config loading, state loading, provider loading, scope creation)

### Net effect

Remove ~2000 lines of custom evaluation. Add ~500 lines of OpenTofu integration wiring.

---

## Part 9: Open Questions for Prototyping

These will be resolved during implementation with a local `replace` directive on the `pulumi/opentofu` fork:

1. **Child module scopes** — Does `Context.Eval()` with `RootModuleInstance` make child module scopes accessible? Or do we need `Eval()` per module instance?
2. **Count/for_each instances** — How are per-instance evaluated values represented in the scope?
3. **Provider plugin loading** — Exact package paths for creating provider factories from `.terraform/providers/` cache.
4. **tfvars loading** — How to populate `SetVariables` from `terraform.tfvars` + `*.auto.tfvars`. May be handled by config loading or need separate parsing.
5. **`configs.RootModuleCallForTesting()`** — Is this the right entry point for `LoadConfig`, or is there a production-oriented call?

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
