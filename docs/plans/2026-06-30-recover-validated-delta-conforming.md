# Recover-Validated Delta-Conforming Patch-State Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `patch-state` transform complex output values to match the bridge's delta structure, validate with `Recover`, and strip broken deltas to prevent panics.

**Architecture:** Add `conformValueToDelta` (recursive delta-aware transform), `propertyValueFromState` (sentinel-aware deserializer), and a post-patch `Recover` validation loop to `PatchStateFromSchema`. Upgrade bridge dependency to match runtime.

**Tech Stack:** Go, pulumi-terraform-bridge/v3 (upgrade to v3.130.0), pulumi/sdk/v3

---

## File Structure

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Bridge dependency upgrade |
| `pkg/state_patcher.go` | `propertyValueFromState`, `conformValueToDelta`, Recover validation in `PatchStateFromSchema`, `DeltaStripped` counter |
| `pkg/state_patcher_bridge_test.go` | All bridge Recover validation tests (existing + new) |
| `pkg/test_helpers_test.go` | `buildTestStateIO` (already exists) |
| `cmd/patch_state.go` | Print `DeltaStripped` in CLI output |

---

## Chunk 1: Bridge Upgrade + propertyValueFromState

### Task 1: Upgrade bridge dependency

**Files:**
- Modify: `go.mod:21`

- [ ] **Step 1: Upgrade the bridge**

```bash
cd /Users/jdavenport/pulumi-repos/pulumi-tool-terraform-migrate
go get github.com/pulumi/pulumi-terraform-bridge/v3@v3.130.0
go mod tidy
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: clean build, no errors

- [ ] **Step 3: Run existing tests**

Run: `go test ./pkg/ -count=1 -timeout 120s`
Expected: all pass (bridge upgrade is backwards-compatible)

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: upgrade pulumi-terraform-bridge to v3.130.0"
```

### Task 2: Add propertyValueFromState (production function)

Move `propertyValueFromJSON` from test-only to production code in `pkg/state_patcher.go`, renamed to `propertyValueFromState`.

**Files:**
- Modify: `pkg/state_patcher.go` (add after `isSimpleValue` function, around line 985)
- Modify: `pkg/state_patcher_bridge_test.go` (update to use production function)

- [ ] **Step 1: Write the test for propertyValueFromState**

Add to `pkg/state_patcher_bridge_test.go`:

```go
func TestPropertyValueFromState_Asset(t *testing.T) {
	t.Parallel()
	sentinel := map[string]interface{}{
		sigKey: assetSig,
		"hash": "abc123",
		"path": "/tmp/test.txt",
	}
	pv := propertyValueFromState(sentinel)
	assert.True(t, pv.IsAsset(), "should deserialize as Asset")
}

func TestPropertyValueFromState_Archive(t *testing.T) {
	t.Parallel()
	sentinel := map[string]interface{}{
		sigKey: archiveSig,
		"hash": "def456",
		"path": "/tmp/test",
	}
	pv := propertyValueFromState(sentinel)
	assert.True(t, pv.IsArchive(), "should deserialize as Archive")
}

func TestPropertyValueFromState_Secret(t *testing.T) {
	t.Parallel()
	sentinel := map[string]interface{}{
		"4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270",
		"value": "my-secret",
	}
	pv := propertyValueFromState(sentinel)
	assert.True(t, pv.IsSecret(), "should deserialize as Secret")
}

func TestPropertyValueFromState_PlainObject(t *testing.T) {
	t.Parallel()
	obj := map[string]interface{}{
		"key": "value",
		"num": 42.0,
	}
	pv := propertyValueFromState(obj)
	assert.True(t, pv.IsObject(), "plain map should be Object")
}

func TestPropertyValueFromState_NestedArray(t *testing.T) {
	t.Parallel()
	arr := []interface{}{"a", "b", "c"}
	pv := propertyValueFromState(arr)
	assert.True(t, pv.IsArray(), "should be Array")
	assert.Equal(t, 3, len(pv.ArrayValue()))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/ -run TestPropertyValueFromState -v`
Expected: FAIL — `propertyValueFromState` not defined (only `propertyValueFromJSON` exists in test file)

- [ ] **Step 3: Add propertyValueFromState to state_patcher.go**

Add to `pkg/state_patcher.go` after the `isSimpleValue` function (around line 985):

```go
// propertyValueFromState converts a JSON-deserialized state value into a
// resource.PropertyValue, recognizing Pulumi sentinel maps (assets, archives,
// secrets) and converting them to properly typed PropertyValues.
//
// resource.NewPropertyValue treats sentinel maps as plain objects, which causes
// pv.IsAsset() etc. to return false. This function uses the SDK's
// DeserializeAsset/DeserializeArchive to produce correct types, matching how
// the engine deserializes state for the bridge's Diff/Recover path.
func propertyValueFromState(v interface{}) resource.PropertyValue {
	replv := func(v interface{}) (resource.PropertyValue, bool) {
		m, ok := v.(map[string]interface{})
		if !ok {
			return resource.PropertyValue{}, false
		}
		s, hasSig := m[sigKey].(string)
		if !hasSig {
			return resource.PropertyValue{}, false
		}
		switch s {
		case "1b47061264138c4ac30d75fd1eb44270": // secret
			elem := propertyValueFromState(m["value"])
			return resource.MakeSecret(elem), true
		default:
			if a, isAsset, err := resource.DeserializeAsset(m); err == nil && isAsset {
				return resource.NewAssetProperty(a), true
			}
			if ar, isArchive, err := resource.DeserializeArchive(m); err == nil && isArchive {
				return resource.NewArchiveProperty(ar), true
			}
		}
		return resource.PropertyValue{}, false
	}
	return resource.NewPropertyValueRepl(v, nil, replv)
}
```

Also add `resource` to the imports if not already present:

```go
"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
```

- [ ] **Step 4: Update test file to use production function**

In `pkg/state_patcher_bridge_test.go`, remove the test-only `propertyValueFromJSON` function and update `validatePatchedOutputsAgainstDelta` to call `propertyValueFromState` instead:

Replace:
```go
outputsPV := propertyValueFromJSON(outputs)
```
With:
```go
outputsPV := propertyValueFromState(outputs)
```

Remove the entire `propertyValueFromJSON` function definition. Also remove the `sig` import since the production code uses `sigKey` constant.

- [ ] **Step 5: Run all tests**

Run: `go test ./pkg/ -run 'TestPropertyValueFromState|TestPatchStateFromSchema' -v -count=1`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add pkg/state_patcher.go pkg/state_patcher_bridge_test.go
git commit -m "feat: add propertyValueFromState for sentinel-aware deserialization"
```

---

## Chunk 2: conformValueToDelta + output patching

### Task 3: Add conformValueToDelta and tests

**Files:**
- Modify: `pkg/state_patcher.go` (replace old `conformToDelta`, add `conformValueToDelta`)
- Modify: `pkg/state_patcher_bridge_test.go` (add delta conforming tests)

- [ ] **Step 1: Write failing tests for Pluralize conforming**

Add to `pkg/state_patcher_bridge_test.go`:

```go
func TestConformValueToDelta_Pluralize(t *testing.T) {
	t.Parallel()
	// plu with inner obj: single-element array → object
	delta := map[string]interface{}{
		"plu": map[string]interface{}{
			"i": map[string]interface{}{
				"obj": map[string]interface{}{},
			},
		},
	}
	// TF value: [{"my_key": "val"}]
	val := []interface{}{
		map[string]interface{}{"myKey": "val"},
	}
	result := conformValueToDelta(val, delta)
	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok, "should flatten to object, got %T", result)
	assert.Equal(t, "val", resultMap["myKey"])
}

func TestConformValueToDelta_Pluralize_Empty(t *testing.T) {
	t.Parallel()
	delta := map[string]interface{}{
		"plu": map[string]interface{}{
			"i": map[string]interface{}{},
		},
	}
	val := []interface{}{}
	result := conformValueToDelta(val, delta)
	assert.Nil(t, result, "empty array with plu delta should become nil")
}

func TestConformValueToDelta_Array(t *testing.T) {
	t.Parallel()
	delta := map[string]interface{}{
		"arr": map[string]interface{}{
			"el": map[string]interface{}{
				"0": map[string]interface{}{"obj": map[string]interface{}{}},
			},
		},
	}
	val := []interface{}{
		map[string]interface{}{"name": "test", "value": "123"},
	}
	result := conformValueToDelta(val, delta)
	arr, ok := result.([]interface{})
	require.True(t, ok, "should stay as array")
	require.Len(t, arr, 1)
}

func TestConformValueToDelta_Obj_Renamed(t *testing.T) {
	t.Parallel()
	delta := map[string]interface{}{
		"obj": map[string]interface{}{
			"renamed": map[string]interface{}{
				"specialName": "special_name",
			},
			"ps": map[string]interface{}{},
		},
	}
	val := map[string]interface{}{"specialName": "hello"}
	result := conformValueToDelta(val, delta)
	// Should pass through — renamed only affects Recover direction (Pulumi→TF),
	// our value is already in Pulumi representation
	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "hello", resultMap["specialName"])
}

func TestConformValueToDelta_NestedPlu(t *testing.T) {
	t.Parallel()
	// obj with a nested field that has plu delta
	delta := map[string]interface{}{
		"obj": map[string]interface{}{
			"ps": map[string]interface{}{
				"config": map[string]interface{}{
					"plu": map[string]interface{}{
						"i": map[string]interface{}{
							"obj": map[string]interface{}{},
						},
					},
				},
			},
		},
	}
	val := map[string]interface{}{
		"config": []interface{}{
			map[string]interface{}{"enabled": true},
		},
		"name": "test",
	}
	result := conformValueToDelta(val, delta)
	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	// config should be flattened from [{"enabled": true}] to {"enabled": true}
	configMap, ok := resultMap["config"].(map[string]interface{})
	require.True(t, ok, "config should be flattened object, got %T", resultMap["config"])
	assert.Equal(t, true, configMap["enabled"])
	// name should pass through
	assert.Equal(t, "test", resultMap["name"])
}

func TestConformValueToDelta_NoDelta(t *testing.T) {
	t.Parallel()
	val := "simple-string"
	result := conformValueToDelta(val, nil)
	assert.Equal(t, "simple-string", result)
}

func TestConformValueToDelta_Replace(t *testing.T) {
	t.Parallel()
	delta := map[string]interface{}{
		"replace": map[string]interface{}{"raw": "something"},
	}
	val := "original"
	result := conformValueToDelta(val, delta)
	assert.Equal(t, "original", result, "replace delta should pass through unchanged")
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./pkg/ -run TestConformValueToDelta -v`
Expected: FAIL — `conformValueToDelta` not defined

- [ ] **Step 3: Implement conformValueToDelta**

Add to `pkg/state_patcher.go`, replacing the old `conformToDelta` function (lines ~995-1055):

```go
// conformValueToDelta recursively transforms a digest value to match the
// bridge's delta structure. The bridge's Recover expects output values to match
// the delta's type expectations (e.g., plu expects an object, arr expects an
// array). This function transforms TF-representation digest values into the
// Pulumi representation that the delta describes.
//
// If delta is nil or empty, the value passes through unchanged.
func conformValueToDelta(val interface{}, delta map[string]interface{}) interface{} {
	if delta == nil {
		return val
	}

	// plu (Pluralize): TF array → Pulumi flattened single value.
	// [x] → transform x with inner delta; [] → nil
	if plu, ok := delta["plu"].(map[string]interface{}); ok {
		arr, isArr := val.([]interface{})
		if isArr && len(arr) == 0 {
			return nil
		}
		if isArr && len(arr) > 0 {
			inner, _ := plu["i"].(map[string]interface{})
			return conformValueToDelta(arr[0], inner)
		}
		// Not an array — already in Pulumi representation, pass through.
		return val
	}

	// arr (ArrayOrSet): keep as array, recurse into element deltas.
	if arrDelta, ok := delta["arr"].(map[string]interface{}); ok {
		arr, isArr := val.([]interface{})
		if !isArr {
			return val
		}
		elDeltas, _ := arrDelta["el"].(map[string]interface{})
		result := make([]interface{}, len(arr))
		for i, elem := range arr {
			key := fmt.Sprintf("%d", i)
			if elDelta, ok := elDeltas[key].(map[string]interface{}); ok {
				result[i] = conformValueToDelta(elem, elDelta)
			} else {
				result[i] = elem
			}
		}
		return result
	}

	// obj (Obj): recurse into property deltas.
	if objDelta, ok := delta["obj"].(map[string]interface{}); ok {
		m, isMap := val.(map[string]interface{})
		if !isMap {
			return val
		}
		ps, _ := objDelta["ps"].(map[string]interface{})
		result := make(map[string]interface{}, len(m))
		for k, v := range m {
			if fieldDelta, ok := ps[k].(map[string]interface{}); ok {
				result[k] = conformValueToDelta(v, fieldDelta)
			} else {
				result[k] = v
			}
		}
		return result
	}

	// map: recurse into element deltas.
	if mapDelta, ok := delta["map"].(map[string]interface{}); ok {
		m, isMap := val.(map[string]interface{})
		if !isMap {
			return val
		}
		elDeltas, _ := mapDelta["el"].(map[string]interface{})
		result := make(map[string]interface{}, len(m))
		for k, v := range m {
			if elDelta, ok := elDeltas[k].(map[string]interface{}); ok {
				result[k] = conformValueToDelta(v, elDelta)
			} else {
				result[k] = v
			}
		}
		return result
	}

	// asset, num, replace, empty: pass through unchanged.
	return val
}
```

- [ ] **Step 4: Run conformValueToDelta tests**

Run: `go test ./pkg/ -run TestConformValueToDelta -v`
Expected: all pass

- [ ] **Step 5: Commit**

```bash
git add pkg/state_patcher.go pkg/state_patcher_bridge_test.go
git commit -m "feat: add conformValueToDelta for recursive delta-aware transforms"
```

### Task 4: Wire conformValueToDelta into PatchStateFromSchema output patching

**Files:**
- Modify: `pkg/state_patcher.go:1437-1457` (output patching section of `PatchStateFromSchema`)

- [ ] **Step 1: Write a test for complex output patching with Recover validation**

Add to `pkg/state_patcher_bridge_test.go`:

```go
func TestPatchStateFromSchema_PluralizeOutputConformed(t *testing.T) {
	t.Parallel()

	prov := buildTestProvider(t, "aws_lb_listener", map[string]testFieldDef{
		"default_action": {optional: true},
		"load_balancer_arn": {required: true},
		"port": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_lb_listener": "aws:lb/listener:Listener",
	})

	// Output has a plu delta for defaultActions (TF TypeSet, MaxItems=1 → object).
	// The digest value is in TF representation (array), output has the Pulumi
	// representation (object flattened by bridge during import).
	stateData := buildTestStateIO("aws:lb/listener:Listener", "my-listener",
		map[string]any{
			"defaultActions":  nil,
			"loadBalancerArn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
		},
		map[string]any{
			"defaultActions": map[string]any{
				"type": "forward",
				"targetGroupArn": "arn:aws:elasticloadbalancing:us-east-1:123:tg/test",
			},
			"loadBalancerArn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{
						"defaultActions": map[string]any{
							"plu": map[string]any{
								"i": map[string]any{
									"obj": map[string]any{},
								},
							},
						},
					},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:lb/listener:Listener::my-listener",
				TerraformAddress: "aws_lb_listener.my_listener",
				Attributes: map[string]interface{}{
					"default_action": []interface{}{
						map[string]interface{}{
							"type":             "forward",
							"target_group_arn": "arn:aws:elasticloadbalancing:us-east-1:123:tg/new",
						},
					},
					"load_balancer_arn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_lb_listener.my_listener": "my-listener",
	}

	patched, _, err := PatchStateFromSchema(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Output defaultActions should be conformed to object (plu flattened), not array.
	da, ok := outputs["defaultActions"].(map[string]interface{})
	require.True(t, ok, "defaultActions output should be object (plu-conformed), got %T: %v",
		outputs["defaultActions"], outputs["defaultActions"])
	assert.Equal(t, "forward", da["type"])

	// Validate with bridge Recover.
	validatePatchedOutputsAgainstDelta(t, patched)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/ -run TestPatchStateFromSchema_PluralizeOutputConformed -v`
Expected: FAIL — output is not conformed (still array or skipped)

- [ ] **Step 3: Update PatchStateFromSchema output patching**

In `pkg/state_patcher.go`, replace the output patching block (lines ~1437-1457) with:

```go
		// Patch outputs. Use conformValueToDelta to transform complex values
		// to match the bridge's delta structure.
		outputIsBadPlain := isSentinel && outputVal != nil && !isSecretSentinel(outputVal)
		outputIsNullSentinel := isSentinel && isNullSentinel(outputVal)
		outputStale := patched && !digEmpty && outputVal != nil && !outputEmpty && !equalValues(outputVal, digVal)
		if outputEmpty || outputIsBadPlain || outputIsNullSentinel || outputStale {
			var newOutputVal interface{}
			if !digEmpty {
				newOutputVal = digVal
			} else if fieldInfo.HasDefault && fieldInfo.SchemaDefault != nil {
				newOutputVal = fieldInfo.SchemaDefault
			}
			if newOutputVal != nil {
				rawOutput := unwrapSecretSentinel(newOutputVal)
				// Look up the field's delta entry to conform the value.
				var fieldDelta map[string]interface{}
				if deltaRaw, ok := outputsRaw["__pulumi_raw_state_delta"]; ok {
					if delta, ok := deltaRaw.(map[string]interface{}); ok {
						if obj, ok := delta["obj"].(map[string]interface{}); ok {
							if ps, ok := obj["ps"].(map[string]interface{}); ok {
								fieldDelta, _ = ps[pulumiField].(map[string]interface{})
							}
						}
					}
				}
				conformed := conformValueToDelta(rawOutput, fieldDelta)
				if conformed != nil {
					outputsRaw[pulumiField] = conformed
				}
			}
		}
```

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/ -run TestPatchStateFromSchema_PluralizeOutputConformed -v`
Expected: PASS

- [ ] **Step 5: Run all existing tests to ensure no regressions**

Run: `go test ./pkg/ -count=1 -timeout 120s`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add pkg/state_patcher.go pkg/state_patcher_bridge_test.go
git commit -m "feat: wire conformValueToDelta into output patching"
```

---

## Chunk 3: Recover validation + DeltaStripped

### Task 5: Add post-patch Recover validation to PatchStateFromSchema

**Files:**
- Modify: `pkg/state_patcher.go:59-67` (PatchStateResult struct)
- Modify: `pkg/state_patcher.go:1460-1478` (after patching loop, before rMap assignment)
- Modify: `cmd/patch_state.go:196` (print DeltaStripped)

- [ ] **Step 1: Write test for strip-on-failure**

Add to `pkg/state_patcher_bridge_test.go`:

```go
func TestPatchStateFromSchema_RecoverValidation_StripOnFailure(t *testing.T) {
	t.Parallel()

	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"bucket":        {required: true},
		"force_destroy": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State with an intentionally broken delta: asset delta on a string field.
	// This should trigger Recover failure and delta stripping.
	stateData := buildTestStateIO("aws:s3/bucket:Bucket", "my-bucket",
		map[string]any{
			"bucket": "my-bucket",
		},
		map[string]any{
			"bucket":       "my-bucket",
			"forceDestroy": false,
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{
						"bucket": map[string]any{
							"asset": map[string]any{"kind": 0},
						},
					},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
				TerraformAddress: "aws_s3_bucket.my_bucket",
				Attributes: map[string]interface{}{
					"bucket":        "my-bucket",
					"force_destroy": true,
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_bucket.my_bucket": "my-bucket",
	}

	patched, result, err := PatchStateFromSchema(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.DeltaStripped, "should strip 1 broken delta")

	// Verify delta was removed from outputs.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	_, hasDelta := outputs["__pulumi_raw_state_delta"]
	assert.False(t, hasDelta, "broken delta should be stripped")
}

func TestPatchStateFromSchema_RecoverValidation_PreservesValid(t *testing.T) {
	t.Parallel()

	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"bucket":        {required: true},
		"force_destroy": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State with a valid delta (no asset mismatch).
	stateData := buildTestStateIO("aws:s3/bucket:Bucket", "my-bucket",
		map[string]any{
			"bucket": "my-bucket",
		},
		map[string]any{
			"bucket":       "my-bucket",
			"forceDestroy": false,
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
				TerraformAddress: "aws_s3_bucket.my_bucket",
				Attributes: map[string]interface{}{
					"bucket":        "my-bucket",
					"force_destroy": true,
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_bucket.my_bucket": "my-bucket",
	}

	patched, result, err := PatchStateFromSchema(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 0, result.DeltaStripped, "valid delta should not be stripped")

	// Verify delta is preserved.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	_, hasDelta := outputs["__pulumi_raw_state_delta"]
	assert.True(t, hasDelta, "valid delta should be preserved")
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./pkg/ -run 'TestPatchStateFromSchema_RecoverValidation' -v`
Expected: FAIL — `DeltaStripped` field doesn't exist

- [ ] **Step 3: Add DeltaStripped to PatchStateResult**

In `pkg/state_patcher.go`, modify the `PatchStateResult` struct (line ~59):

```go
type PatchStateResult struct {
	Patched          int
	FieldsFromDigest int
	FieldsFromDefaults int
	SkippedSensitive int
	NoMatch          int
	NoFields         int
	DigestMapped     int
	DeltaStripped    int
}
```

- [ ] **Step 4: Add Recover validation loop after resource patching**

In `pkg/state_patcher.go`, in `PatchStateFromSchema`, after the asset delta injection block (after line ~1470) and before `rMap["inputs"] = inputsRaw`, add:

```go
		// Recover validation: check that patched outputs + delta are consistent.
		if deltaRaw, hasDelta := outputsRaw["__pulumi_raw_state_delta"]; hasDelta {
			if deltaMap, ok := deltaRaw.(map[string]interface{}); ok {
				outputsPV := propertyValueFromState(outputsRaw)
				deltaPV := resource.NewPropertyValue(deltaMap)
				rsd, unmarshalErr := tfbridge.UnmarshalRawStateDelta(deltaPV)
				if unmarshalErr != nil {
					fmt.Fprintf(os.Stderr, "  WARNING: failed to unmarshal delta for %s: %v — stripping delta\n", urn, unmarshalErr)
					delete(outputsRaw, "__pulumi_raw_state_delta")
					result.DeltaStripped++
				} else if _, recoverErr := rsd.Recover(outputsPV); recoverErr != nil {
					fmt.Fprintf(os.Stderr, "  WARNING: Recover failed for %s: %v — stripping delta\n", urn, recoverErr)
					delete(outputsRaw, "__pulumi_raw_state_delta")
					result.DeltaStripped++
				}
			}
		}
```

Add `tfbridge` to the imports:

```go
"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge"
```

- [ ] **Step 5: Add DeltaStripped to CLI output**

In `cmd/patch_state.go`, after line ~196 (`Digest mapped`), add:

```go
fmt.Fprintf(os.Stderr, "  Delta stripped:     %d\n", result.DeltaStripped)
```

- [ ] **Step 6: Run Recover validation tests**

Run: `go test ./pkg/ -run 'TestPatchStateFromSchema_RecoverValidation' -v`
Expected: all pass

- [ ] **Step 7: Run full test suite**

Run: `go test ./pkg/ -count=1 -timeout 120s`
Expected: all pass

- [ ] **Step 8: Commit**

```bash
git add pkg/state_patcher.go pkg/state_patcher_bridge_test.go cmd/patch_state.go
git commit -m "feat: post-patch Recover validation with strip-on-failure"
```

---

## Chunk 4: Remaining test coverage

### Task 6: Add comprehensive delta variant tests

**Files:**
- Modify: `pkg/state_patcher_bridge_test.go`

- [ ] **Step 1: Add remaining test cases**

Add to `pkg/state_patcher_bridge_test.go`:

```go
func TestPatchStateFromSchema_ArrayOutputConformed(t *testing.T) {
	t.Parallel()

	prov := buildTestProvider(t, "aws_rds_cluster_parameter_group", map[string]testFieldDef{
		"parameter": {optional: true},
		"family":    {required: true},
		"name":      {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_rds_cluster_parameter_group": "aws:rds/clusterParameterGroup:ClusterParameterGroup",
	})

	stateData := buildTestStateIO("aws:rds/clusterParameterGroup:ClusterParameterGroup", "rds-params",
		map[string]any{
			"parameters": nil,
			"family":     "aurora-mysql8.0",
		},
		map[string]any{
			"parameters": nil,
			"family":     "aurora-mysql8.0",
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{
						"parameters": map[string]any{
							"arr": map[string]any{},
						},
					},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::rds-params",
				TerraformAddress: "aws_rds_cluster_parameter_group.rds_params",
				Attributes: map[string]interface{}{
					"parameter": []interface{}{
						map[string]interface{}{"name": "max_connections", "value": "100", "apply_method": "immediate"},
					},
					"family": "aurora-mysql8.0",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster_parameter_group.rds_params": "rds-params",
	}

	patched, _, err := PatchStateFromSchema(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Output parameters should be array (matching arr delta).
	params, ok := outputs["parameters"].([]interface{})
	require.True(t, ok, "parameters output should be array, got %T", outputs["parameters"])
	require.Len(t, params, 1)

	validatePatchedOutputsAgainstDelta(t, patched)
}

func TestPatchStateFromSchema_MixedComplexFields(t *testing.T) {
	t.Parallel()

	prov := buildTestProvider(t, "aws_lb_listener", map[string]testFieldDef{
		"default_action":    {optional: true},
		"load_balancer_arn": {required: true},
		"port":              {optional: true},
		"protocol":          {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_lb_listener": "aws:lb/listener:Listener",
	})

	// Resource with multiple fields: one plu, one simple.
	stateData := buildTestStateIO("aws:lb/listener:Listener", "my-listener",
		map[string]any{
			"loadBalancerArn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
			"defaultActions":  nil,
			"port":            nil,
			"protocol":        nil,
		},
		map[string]any{
			"loadBalancerArn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
			"defaultActions":  nil,
			"port":            nil,
			"protocol":        nil,
			"__pulumi_raw_state_delta": map[string]any{
				"obj": map[string]any{
					"ps": map[string]any{
						"defaultActions": map[string]any{
							"plu": map[string]any{
								"i": map[string]any{
									"obj": map[string]any{},
								},
							},
						},
					},
				},
			},
		},
	)

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:lb/listener:Listener::my-listener",
				TerraformAddress: "aws_lb_listener.my_listener",
				Attributes: map[string]interface{}{
					"default_action": []interface{}{
						map[string]interface{}{"type": "forward", "target_group_arn": "arn:tg/new"},
					},
					"load_balancer_arn": "arn:aws:elasticloadbalancing:us-east-1:123:lb/test",
					"port":             443,
					"protocol":         "HTTPS",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_lb_listener.my_listener": "my-listener",
	}

	patched, _, err := PatchStateFromSchema(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// defaultActions should be plu-conformed to object.
	da, ok := outputs["defaultActions"].(map[string]interface{})
	require.True(t, ok, "defaultActions should be object, got %T", outputs["defaultActions"])
	assert.Equal(t, "forward", da["type"])

	// Simple fields should be patched normally.
	assert.EqualValues(t, 443, outputs["port"])
	assert.Equal(t, "HTTPS", outputs["protocol"])

	validatePatchedOutputsAgainstDelta(t, patched)
}
```

- [ ] **Step 2: Run new tests**

Run: `go test ./pkg/ -run 'TestPatchStateFromSchema_ArrayOutputConformed|TestPatchStateFromSchema_MixedComplexFields' -v`
Expected: all pass

- [ ] **Step 3: Run full test suite**

Run: `go test ./pkg/ -count=1 -timeout 120s`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add pkg/state_patcher_bridge_test.go
git commit -m "test: add array, mixed complex field, and Recover validation coverage"
```

### Task 7: Build, install, and verify

- [ ] **Step 1: Build the tool**

```bash
cd /Users/jdavenport/pulumi-repos/pulumi-tool-terraform-migrate
go build -o bin/pulumi-tool-terraform-migrate .
```

- [ ] **Step 2: Install as Pulumi plugin**

```bash
pulumi plugin rm tool terraform-migrate -y
pulumi plugin install tool terraform-migrate v0.2.0 -f ./bin/pulumi-tool-terraform-migrate
```

- [ ] **Step 3: Push branch**

```bash
git push origin feat/recover-validated-delta-conforming
```
