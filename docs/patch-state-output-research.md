# patch-state Output Patching Research

## Background

The `patch-state` command fills not_read fields (fields the provider's Read
function doesn't return) in the imported Pulumi state from TF digest values.
This document captures research into whether and how outputs should be patched.

## Key Finding: Outputs ARE Used by Diff

The Pulumi engine sends **old outputs** (not old inputs) as the `Olds` field
in the gRPC `DiffRequest` to the bridge.

### Evidence

**Engine side** (`pulumi/sdk/v3/go/common/resource/plugin/provider_plugin.go`):

```go
// Line ~964
resp, err := client.Diff(p.requestContext(), &pulumirpc.DiffRequest{
    Id:            string(req.ID),
    Urn:           string(req.URN),
    OldInputs:     mOldInputs,    // old inputs (separate field)
    Olds:          mOldOutputs,    // ← old OUTPUTS sent as "Olds"
    News:          mNewInputs,     // new inputs
    IgnoreChanges: req.IgnoreChanges,
})
```

**Bridge side** (`pulumi-terraform-bridge/v3/pkg/tfbridge/provider.go`):

```go
// Line ~1112 in Diff method
olds, err := plugin.UnmarshalProperties(req.GetOlds(), ...)
// olds = old OUTPUTS (received via gRPC Olds field)

// Line ~1129-1130
state, err = makeTerraformStateViaUpgrade(ctx, pNew, res, olds)
// Reconstructs TF state from old OUTPUTS + __pulumi_raw_state_delta
```

**Engine DiffRequest struct** (`pulumi/sdk/v3/go/common/resource/plugin/provider.go`):

```go
type DiffRequest struct {
    // ...
    // TODO Change to (OldInputs, OldState, NewInputs)
    OldInputs, OldOutputs, NewInputs resource.PropertyMap
    // ...
}
```

### How Import Populates Inputs and Outputs

During `pulumi import`, the flow is:

1. **ReadResource** — TF provider's Read function queries AWS for the resource state
2. **MakeTerraformResult** — bridge converts TF state → Pulumi outputs
3. **RawStateInjectDelta** — bridge computes the delta between the Pulumi
   outputs and the raw TF state, stores it in `__pulumi_raw_state_delta`
   within the outputs
4. **Inputs derived from outputs** — since import has no user-provided inputs,
   the engine copies outputs → inputs (minus computed-only fields)

After import, **inputs ≈ outputs**. Both are derived from the provider's Read.
Not_read fields (where Read returns nil) will be nil/empty in both inputs and
outputs. The `__pulumi_raw_state_delta` lives in the outputs.

### How Diff Uses Both Inputs and Outputs

On the next preview/diff:

1. Engine sends stored **inputs** as `OldInputs` and stored **outputs** as
   `Olds` to the bridge via gRPC
2. Bridge uses `Olds` (outputs) + `__pulumi_raw_state_delta` to reconstruct
   the old TF state via `Recover`
3. Bridge compares that reconstructed TF state against the new inputs from
   the program via `tf.Diff()`

This is why patching **both** inputs and outputs matters — the engine sends
them separately and the bridge uses both. Stale output values cause the
bridge to reconstruct an incorrect "prior" TF state, producing phantom diffs.

## Current Approach: Simple Values Only

Outputs are patched only for **simple values** (bool, number, string):

```go
rawOutput := unwrapSecretSentinel(newOutputVal)
if isSimpleValue(rawOutput) {
    outputsRaw[pulumiField] = rawOutput
}
```

### Why Not Complex Values?

Complex values (arrays, objects) may have been **type-mapped by the bridge**
during import. The bridge transforms TF types when creating Pulumi outputs:

- TF `TypeSet` (single-element array) → Pulumi object (flattened)
- TF snake_case nested keys → Pulumi camelCase keys
- TF null defaults → bridge schema defaults

The `__pulumi_raw_state_delta` records these transformations. The bridge's
`Recover` function uses the delta to reverse-map Pulumi outputs back to TF
state. If we overwrite outputs with digest values (which use the TF
representation), the delta no longer matches and `Recover` panics:

```
"does not apply cleanly to the resource state"
"expected PropertyValue to be an Object encoding a Terraform object"
```

### Examples of Bridge Type Mapping

| TF state | Pulumi output | Delta marker |
|----------|---------------|--------------|
| `options: [{cert_transparency_logging_preference: "DISABLED"}]` | `options: {certificateTransparencyLoggingPreference: "DISABLED"}` | `"plu":{"i":{"obj":{}}}` |
| `configuration: []` | `configuration: nil` | `"plu":{"i":{}}` |
| `default_action: [{type: "forward", ...}]` | `defaultActions: [{type: "forward", ...}]` | `"arr":{"el":{"0":{"obj":{}}}}` |

## Future Work: Complex Value Conforming

To patch complex output values, we would need to:

1. Read the `__pulumi_raw_state_delta` for the field
2. Detect the bridge's type transformation (e.g. `"plu"` → TypeSet flattening)
3. Transform the digest value to match the bridge's representation
4. Update the delta if the structure changed

A prototype `conformToDelta` function exists that handles the top-level
TypeSet→object flattening and nested plu fields. However, it doesn't cover
all cases (e.g. array elements with nested objects that also have plu deltas).

### Key Code References

- **Bridge Diff method**: `pkg/tfbridge/provider.go:1093` — receives `req.GetOlds()` (old outputs)
- **State reconstruction**: `pkg/tfbridge/schema.go:1413` — `makeTerraformStateViaUpgrade` uses delta
- **Delta definition**: `pkg/tfbridge/rawstate.go:39` — `RawStateDelta` struct and `Recover` method
- **Delta injection**: `pkg/tfbridge/provider.go:1374` — `RawStateInjectDelta` stores delta with outputs
- **Engine Diff call**: `sdk/v3/go/common/resource/plugin/provider_plugin.go:964` — sends `mOldOutputs` as `Olds`
- **DiffRequest struct**: `sdk/v3/go/common/resource/plugin/provider.go:116` — `OldInputs, OldOutputs, NewInputs`
