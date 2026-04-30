# Module Map: Data Sources and Root Resources

## Context

The `module-map` command produces a JSON manifest consumed by an agent to guide Pulumi migration. Currently it only includes managed resources inside modules. This leaves two gaps:

1. **Data sources** — modules and root config may depend on data sources (e.g., `terraform_remote_state`, `aws_caller_identity`). The agent needs to know about these so it can wire values through Pulumi ESC or equivalent lookups, decoupling values from their Terraform source.
2. **Root-level resources** — resources not inside any module are invisible to the module map. The agent has no structured manifest for these; it would need to parse the raw state file separately.

## Design

### Schema Changes

**`ModuleResource` gets a `mode` field:**

```go
type ModuleResource struct {
    Mode             string `json:"mode"` // "managed" or "data"
    TranslatedURN    string `json:"translatedUrn"`
    TerraformAddress string `json:"terraformAddress"`
    ImportID         string `json:"importId"`
}
```

**`ModuleMap` gets a `rootResources` field:**

```go
type ModuleMap struct {
    Modules       map[string]*ModuleMapEntry `json:"modules"`
    RootResources []ModuleResource           `json:"rootResources,omitempty"`
}
```

Rules:
- Managed resources: `mode: "managed"`, `translatedUrn` populated via bridge, `importId` from `id` attribute.
- Data sources: `mode: "data"`, `translatedUrn: ""` (empty — no Pulumi resource equivalent), `importId` from `id` attribute when present, `""` when absent (some data sources like `terraform_remote_state` may lack an `id` attribute).
- The `mode` field appears on every resource entry — root and module alike.
- `RootResources` uses `omitempty` so it is omitted from JSON when no root resources exist.

### Example Output

```json
{
  "modules": {
    "cf_parameters[\"/develop/factory/clf\"]": {
      "terraformPath": "module.cf_parameters[\"/develop/factory/clf\"]",
      "resources": [
        {
          "mode": "managed",
          "translatedUrn": "urn:pulumi:dev::project::aws:ssm/parameter:Parameter::...",
          "terraformAddress": "module.cf_parameters[...].aws_ssm_parameter.ssm_parameters[...]",
          "importId": "/develop/dmvhm/clf/sendgrid_api_key"
        }
      ]
    }
  },
  "rootResources": [
    {
      "mode": "managed",
      "translatedUrn": "urn:pulumi:dev::project::aws:dynamodb/table:Table::dynamodb_count_tables",
      "terraformAddress": "aws_dynamodb_table.dynamodb_count_tables[0]",
      "importId": "table-abc123"
    },
    {
      "mode": "data",
      "translatedUrn": "",
      "terraformAddress": "data.terraform_remote_state.old",
      "importId": ""
    }
  ]
}
```

Note: `modules` appears before `rootResources` in output since Go serializes struct fields in declaration order.

### Implementation Changes

#### `matchResources` — `pkg/module_map.go`

Add `Mode` field from `res.Addr.Resource.Mode`:
- `addrs.ManagedResourceMode` → `"managed"`
- `addrs.DataResourceMode` → `"data"`

For data sources, set `translatedUrn` to `""` instead of calling `buildResourceURN`. This applies uniformly — both module-level and root-level data sources get the same treatment. The `data.` prefix in `terraformAddress` is automatic from `addrs.Resource.String()` when `Mode == DataResourceMode`; no manual prefix needed.

#### `collectRootResources` — `pkg/module_map.go`

Reuse `matchResources(state, nil, ...)` with an empty segments slice to match the root module. The root module's `moduleSegmentsFromAddr` returns `[]moduleSegment{}`, and `buildModulePath([])` returns `""`, which matches correctly. No separate function needed — just call `matchResources` with empty segments.

#### `BuildModuleMap` — `pkg/module_map.go`

After building module entries, call `matchResources(state, nil, ...)` to populate `mm.RootResources`. Set to `nil` (not empty slice) when no root resources exist, so `omitempty` omits the field.

#### `rawStateFromTfjson` — `pkg/generate_module_map.go`

Two fixes:
1. **Set `IncludeDataSources: true`** in the `VisitOptions` passed to `tofuutil.VisitResources`. Without this, data sources are silently skipped by the visitor (default is `false`).
2. **Map resource mode correctly** from `r.Mode`: `tfjson.ManagedResourceMode` → `addrs.ManagedResourceMode`, `tfjson.DataResourceMode` → `addrs.DataResourceMode`. Currently hardcoded to `ManagedResourceMode` for all resources.

#### `getTerraformProvidersForRawState` — `pkg/pulumi_providers.go`

No changes needed — data source providers are already collected.

### Known Limitation

`rawStateFromTfjson` does not handle instance keys (count/for_each) — all resources are created with `addrs.NoKey`. This is a pre-existing bug not in scope for this change. Resources with `count` or `for_each` in the tfjson path will appear as a single entry. This does not affect the raw state path (which preserves instance keys from the statefile reader).

### Test Changes

**Update existing tests (`pkg/module_map_test.go`):**
- All existing `ModuleResource` assertions add `Mode: "managed"` checks.
- `TestWriteModuleMap` serialization test adds `mode` field and `rootResources` round-trip verification.

**New test: `TestBuildModuleMap_DataSources`:**
- Build a `*states.State` programmatically using `states.BuildState()` with a data source inside a module.
- Verify data source appears with `mode: "data"`, `translatedUrn: ""`, correct `terraformAddress` (with automatic `data.` prefix).

**New test: `TestBuildModuleMap_RootResources`:**
- Build a `*states.State` programmatically with root-level managed and data resources.
- Verify `mm.RootResources` contains entries with correct `mode` values.
- Verify managed resources have populated URNs (or raw address fallback) while data sources have `""`.

**New test: `TestRawStateFromTfjson_DataSources`:**
- Build a `*tfjson.State` with data sources.
- Convert via `rawStateFromTfjson`.
- Verify the resulting `*states.State` preserves `DataResourceMode` on data source resources.

## Files to Modify

- `pkg/module_map.go` — `ModuleMap`, `ModuleResource` structs; `matchResources` (mode-aware logic); `BuildModuleMap` (root resources)
- `pkg/generate_module_map.go` — fix `rawStateFromTfjson` mode handling and `IncludeDataSources` flag
- `pkg/module_map_test.go` — update existing tests, add new tests

## What Stays the Same

- `ModuleMapEntry` structure (modules still have `resources`, `interface`, nested `modules`)
- `buildResourceURN` signature and behavior
- `getTerraformProvidersForRawState` — already handles all resource providers
- CLI flags — no new flags needed
