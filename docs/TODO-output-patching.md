# TODO: Investigate whether output patching is necessary

## Question

Does patching outputs in `patch-state` contribute to diff reduction, or is it only for state consistency?

## Evidence so far

- SGI migration: 12 swagger_assets resources had Recover failures (outputs reverted to pre-patch state) but showed **zero diffs** in preview. Inputs were patched successfully and that was sufficient.
- The bridge's `makeDetailedDiff` compares Pulumi-level old inputs vs new inputs.
- The bridge's `makeTerraformState` reconstructs TF state from Pulumi inputs for the Diff call.
- Outputs contain `__pulumi_raw_state_delta` which the bridge merges during state reconstruction.

## Uncertainty

- The `__pulumi_raw_state_delta` in outputs may carry field values that affect the TF state reconstruction. If a field is in the delta but not in inputs, the reconstructed TF state may differ from what the provider expects.
- The engine sends outputs as `Olds` in the gRPC DiffRequest. Even if the bridge primarily uses inputs, there may be edge cases where output values matter (e.g., Computed-only fields that only exist in outputs).
- Need to verify with a controlled test: patch only inputs, leave outputs as-is, and confirm no diffs for all field categories (not just assets).

## Current issues with output patching

- Recover validation fails on asset fields due to type unmarshalling bug: `json: cannot unmarshal string into Go struct field assetDelta.obj.ps.asset.kind of type info.AssetTranslationKind`
- Recover validation adds complexity and can revert correct patches
- Output patching for complex values (arrays/objects) is already skipped to avoid breaking the delta

## Options

1. **Remove output patching entirely** — simplifies tool, eliminates Recover. Risk: unknown edge cases where outputs matter.
2. **Fix the Recover unmarshalling bug** — keep output patching working. Cost: more complexity.
3. **Make output patching opt-in** — add a `--patch-outputs` flag, default off. Can be enabled when evidence shows it's needed.

## Next step

Run a controlled experiment: for a resource type with many not_read fields (e.g., RDS Cluster), patch only inputs (not outputs) and verify the preview diff is identical to patching both.
