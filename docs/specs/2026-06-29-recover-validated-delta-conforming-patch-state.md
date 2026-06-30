# Recover-Validated Delta-Conforming Patch-State

## Summary

Enhance the `patch-state` command to:
1. Transform complex output values to match the bridge's `__pulumi_raw_state_delta` structure (recursive delta conforming)
2. Validate every patched resource's outputs against the bridge's `Recover` function
3. Strip deltas that fail validation (warn, don't fail) to prevent provider panics

## Background

The bridge stores a `__pulumi_raw_state_delta` in each resource's outputs that encodes how to reconstruct TF raw state from Pulumi PropertyValues. During Diff, the bridge calls `RawStateDelta.Recover(oldOutputs)` to reconstruct the prior TF state. If the outputs don't match the delta's expected types, Recover returns an error that the provider surfaces as a panic.

Currently, `patch-state` only patches **simple values** (string, bool, number) and **asset/archive sentinels** into outputs. Complex values (arrays, objects) are skipped because they may have been type-mapped by the bridge (e.g., TF TypeSet → Pulumi flattened object). This leaves digest values unpatched in outputs, causing phantom diffs.

## Design

### 1. Bridge Dependency Upgrade

Upgrade `pulumi-terraform-bridge/v3` from `v3.121.0` to `v3.130.0` to match the runtime AWS provider version. This ensures Recover validation matches actual runtime behavior.

### 2. Sentinel-Aware PropertyValue Deserialization

Add a production function `propertyValueFromState(v interface{}) resource.PropertyValue` that converts JSON-deserialized state values into properly typed PropertyValues. This recognizes sentinel maps:

- Asset sentinel (`sig == "c44067f5952c0a294b673a41bacd8c17"`) → `resource.NewAssetProperty`
- Archive sentinel (`sig == "0def7320c3a5731c473e5ecbe6d01bc7"`) → `resource.NewArchiveProperty`
- Secret sentinel (`sig == "1b47061264138c4ac30d75fd1eb44270"`) → `resource.MakeSecret`

Uses `resource.DeserializeAsset` and `resource.DeserializeArchive` from the Pulumi SDK. Recurses into arrays and objects. Required because `resource.NewPropertyValue` treats sentinel maps as plain objects, causing `pv.IsAsset()` etc. to return false.

### 3. Recursive Delta-Conforming Output Transform

Replace the current `isSimpleValue || isAssetOrArchiveSentinel` guard on output patching with a recursive `conformValueToDelta` function. When patching a complex output value, read the field's delta entry from `__pulumi_raw_state_delta.obj.ps[field]` and transform the digest value to match:

```
func conformValueToDelta(val interface{}, delta map[string]interface{}) interface{}
```

| Delta variant | Transform on digest value |
|---|---|
| `plu` (Pluralize) | `[x]` → transform `x` with inner delta; `[]` → nil |
| `arr` (ArrayOrSet) | Keep as array; recurse into element deltas for object elements |
| `obj` (Obj) | CamelCase keys; apply `renamed` map; recurse into `ps` entries |
| `map` | CamelCase keys; recurse into element deltas |
| `asset` | Pass through (already a sentinel) |
| `num` | Pass through (already a string) |
| `replace` | Skip — delta carries full raw state, no conforming needed |
| empty/absent | Pass through unchanged |

The recursion handles arbitrarily nested structures. For example, a field with delta `{"plu": {"i": {"obj": {"ps": {"subnetIds": {"arr": {}}}}}}}` transforms a TF array `[{"subnet_ids": [...]}]` → Pulumi object `{"subnetIds": [...]}` by:
1. `plu`: unwrap single-element array
2. `obj`: camelCase keys, recurse into `ps`
3. `arr`: keep nested arrays as-is

All output values (simple, complex, asset) go through this path. Simple values and assets pass through unchanged when no delta entry exists.

### 4. Post-Patch Recover Validation

After patching each resource, if it has a `__pulumi_raw_state_delta`:

1. Deserialize outputs into `resource.PropertyValue` via `propertyValueFromState`
2. Unmarshal delta via `tfbridge.UnmarshalRawStateDelta`
3. Call `delta.Recover(outputsPV)`
4. **On success**: keep delta as-is
5. **On failure**: log warning with URN and error detail, remove `__pulumi_raw_state_delta` from outputs, increment `DeltaStripped` counter

Stripping the delta is safe — the bridge falls back to `rawStateRecoverNatural` which handles simple types, arrays, and nulls. The resource may show phantom diffs on the next preview but will not panic. After `pulumi up`, the bridge recomputes a correct delta.

Add `DeltaStripped int` to `PatchStateResult` and print it in the CLI output.

### 5. Test Coverage

Expand `state_patcher_bridge_test.go` with tests for each delta variant, all validated against the bridge's `Recover`:

| Test | Delta type | Scenario |
|---|---|---|
| `Pluralize_FlattenedTypeSet` | `plu` | Single-element TF array → Pulumi object |
| `Pluralize_EmptyTypeSet` | `plu` | Empty TF array → nil |
| `ArrayOrSet_MultiElement` | `arr` | Multi-element array with object elements |
| `Obj_RenamedFields` | `obj` | Object with `renamed` map entries |
| `Obj_NestedPlu` | `plu` inside `obj.ps` | Nested Pluralize inside object |
| `Mixed_MultipleComplexFields` | multiple | Resource with several complex fields |
| `RecoverValidation_StripOnFailure` | broken | Intentionally invalid delta → verify strip+warn |
| `RecoverValidation_PreservesValid` | valid | Valid delta preserved after validation |
| `Num_LargeInteger` | `num` | Large integer stored as string |
| `Asset_FileAsset` | `asset` | Already covered, verify Recover passes |
| `Archive_FileArchive` | `asset` | Already covered, verify Recover passes |

All tests use `buildTestStateIO` (separate inputs/outputs) and `validatePatchedOutputsAgainstDelta` (bridge Recover validation).

### 6. Files Changed

| File | Change |
|---|---|
| `go.mod`, `go.sum` | Upgrade bridge to v3.130.0 |
| `pkg/state_patcher.go` | `propertyValueFromState`, `conformValueToDelta`, Recover validation loop, `DeltaStripped` field |
| `pkg/state_patcher_bridge_test.go` | Expanded test suite per table above |
| `pkg/test_helpers_test.go` | Additional helpers if needed |
| `cmd/patch_state.go` | Print `DeltaStripped` count in CLI output |

### 7. Non-Goals

- Patching resources that have no digest match (no change)
- Handling `replace` deltas (they carry full raw state, no conforming needed)
- Fixing delta computation itself (that's the bridge's responsibility)
- Upgrading the AWS provider version (separate concern)
