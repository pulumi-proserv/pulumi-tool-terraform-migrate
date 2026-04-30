# Module Map: Data Sources and Root Resources — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `mode` field to `ModuleResource` and `rootResources` to `ModuleMap` so the module-map manifest includes data sources and root-level resources.

**Architecture:** The `matchResources` function gains mode-awareness from `addrs.Resource.Mode`. `BuildModuleMap` calls `matchResources` with empty segments for root resources. `rawStateFromTfjson` is fixed to preserve data source mode and include data sources.

**Tech Stack:** Go, OpenTofu `states`/`addrs` packages, `tfjson`

**Spec:** `docs/superpowers/specs/2026-04-30-module-map-data-sources-and-root-resources-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `pkg/module_map.go` | `ModuleMap`, `ModuleResource` structs; `matchResources`; `BuildModuleMap` |
| `pkg/generate_module_map.go` | `rawStateFromTfjson` — tfjson-to-raw-state converter |
| `pkg/module_map_test.go` | All tests for module map building and serialization |

---

## Chunk 1: Schema + Mode Field

### Task 1: Add `Mode` field to `ModuleResource` and `RootResources` to `ModuleMap`

**Files:**
- Modify: `pkg/module_map.go:36-45`
- Modify: `pkg/module_map_test.go` (existing assertions)

- [ ] **Step 1: Update the structs**

In `pkg/module_map.go`, change:

```go
type ModuleMap struct {
	Modules       map[string]*ModuleMapEntry `json:"modules"`
	RootResources []ModuleResource           `json:"rootResources,omitempty"`
}

type ModuleResource struct {
	Mode             string `json:"mode"` // "managed" or "data"
	TranslatedURN    string `json:"translatedUrn"`
	TerraformAddress string `json:"terraformAddress"`
	ImportID         string `json:"importId"`
}
```

- [ ] **Step 2: Run build to confirm struct changes compile**

Run: `go build ./...`
Expected: PASS (existing code doesn't set `Mode`, so it defaults to `""`)

- [ ] **Step 3: Write failing test assertions for `Mode: "managed"`**

In `pkg/module_map_test.go`, update `TestBuildModuleMap_WithoutEval`:

After line 60 (`assert.Len(t, pet0.Resources, 1)`), add:
```go
	assert.Equal(t, "managed", pet0.Resources[0].Mode)
```

After line 65 (`assert.Len(t, pet1.Resources, 1)`), add:
```go
	assert.Equal(t, "managed", pet1.Resources[0].Mode)
```

Update the `ModuleResource` literal in `TestWriteModuleMap` (line ~149):

```go
			Resources: []ModuleResource{{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:stack::project::aws:ec2/vpc:Vpc::main",
				TerraformAddress: "module.vpc.aws_vpc.main",
				ImportID:         "vpc-12345",
			}},
```

Add assertion after line 181:
```go
	assert.Equal(t, "managed", got.Modules["vpc"].Resources[0].Mode)
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./pkg/ -run "TestBuildModuleMap_WithoutEval|TestWriteModuleMap" -v -count=1`
Expected: FAIL — `Mode` is `""`, not `"managed"`

- [ ] **Step 5: Implement `matchResources` mode-aware logic**

In `pkg/module_map.go`, inside the `matchResources` function, replace the resource append block (lines ~288-293):

```go
				// Determine mode string.
				mode := "managed"
				if res.Addr.Resource.Mode == addrs.DataResourceMode {
					mode = "data"
				}

				// Data sources don't map to Pulumi resources.
				urn := ""
				if mode == "managed" {
					urn = buildResourceURN(address, providerName, resourceType, pulumiProviders, stackName, projectName)
				}

				resources = append(resources, ModuleResource{
					Mode:             mode,
					TranslatedURN:    urn,
					TerraformAddress: address,
					ImportID:         importID,
				})
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/ -run "TestBuildModuleMap|TestWriteModuleMap" -v -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: add mode field to ModuleResource and rootResources to ModuleMap"
```

---

### Task 2: Populate `RootResources` in `BuildModuleMap`

**Files:**
- Modify: `pkg/module_map.go:78-96` (`BuildModuleMap`)
- Modify: `pkg/module_map_test.go` (new test)

- [ ] **Step 1: Write the failing test `TestBuildModuleMap_RootResources`**

Add to `pkg/module_map_test.go`:

```go
func TestBuildModuleMap_RootResources(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	// Build a state with root-level resources programmatically.
	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	// Add a root-level managed resource to the existing state.
	rootModule := rawState.RootModule()
	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "aws_s3_bucket",
				Name: "example",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"id":"my-bucket","bucket":"my-bucket"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	// Add a root-level data source.
	rootModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.DataResourceMode,
				Type: "terraform_remote_state",
				Name: "old",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"backend":"s3"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("terraform.io/builtin/terraform"),
		},
		nil,
	)

	mm, err := BuildModuleMap(config, nil, rawState, nil, "test-stack", "test-project")
	require.NoError(t, err)
	require.NotNil(t, mm)

	// Module resources should still work.
	require.Contains(t, mm.Modules, "pet[0]")

	// Root resources should be populated.
	require.NotNil(t, mm.RootResources)
	require.Len(t, mm.RootResources, 2)

	// Sort by address for deterministic assertion.
	sort.Slice(mm.RootResources, func(i, j int) bool {
		return mm.RootResources[i].TerraformAddress < mm.RootResources[j].TerraformAddress
	})

	// Managed resource — URN falls back to raw address when pulumiProviders is nil.
	assert.Equal(t, "managed", mm.RootResources[0].Mode)
	assert.Equal(t, "aws_s3_bucket.example", mm.RootResources[0].TranslatedURN)
	assert.Equal(t, "aws_s3_bucket.example", mm.RootResources[0].TerraformAddress)
	assert.Equal(t, "my-bucket", mm.RootResources[0].ImportID)

	// Data source — URN should be empty.
	assert.Equal(t, "data", mm.RootResources[1].Mode)
	assert.Equal(t, "data.terraform_remote_state.old", mm.RootResources[1].TerraformAddress)
	assert.Equal(t, "", mm.RootResources[1].TranslatedURN)
	assert.Equal(t, "", mm.RootResources[1].ImportID) // no "id" attribute
}
```

Add these imports if not already present: `"sort"`, `"github.com/pulumi/opentofu/addrs"`, `"github.com/pulumi/opentofu/states"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/ -run TestBuildModuleMap_RootResources -v -count=1`
Expected: FAIL — `mm.RootResources` is nil

- [ ] **Step 3: Add root resource collection to `BuildModuleMap`**

In `pkg/module_map.go`, update `BuildModuleMap` to call `matchResources` with empty segments for the root module, then set `RootResources`:

```go
func BuildModuleMap(
	config *configs.Config,
	tofuCtx *tofu.Context,
	state *states.State,
	pulumiProviders map[providermap.TerraformProviderName]*ProviderWithMetadata,
	stackName string,
	projectName string,
) (*ModuleMap, error) {
	mm := &ModuleMap{
		Modules: make(map[string]*ModuleMapEntry),
	}

	err := buildModuleMapLevel(mm.Modules, config, tofuCtx, state, pulumiProviders, stackName, projectName, nil) //nolint:lll
	if err != nil {
		return nil, err
	}

	// Collect root-level resources (empty segments = root module).
	rootResources := matchResources(state, nil, pulumiProviders, stackName, projectName)
	if len(rootResources) > 0 {
		mm.RootResources = rootResources
	}

	return mm, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/ -run TestBuildModuleMap_RootResources -v -count=1`
Expected: PASS

- [ ] **Step 5: Run all module map tests to verify no regressions**

Run: `go test ./pkg/ -run "TestBuildModuleMap|TestWriteModuleMap" -v -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/module_map.go pkg/module_map_test.go
git commit -m "feat: populate rootResources in BuildModuleMap"
```

---

## Chunk 2: Data Sources + Serialization

### Task 3: Test data sources inside modules

**Files:**
- Modify: `pkg/module_map_test.go` (new test)

- [ ] **Step 1: Write the failing test `TestBuildModuleMap_DataSources`**

Add to `pkg/module_map_test.go`:

```go
func TestBuildModuleMap_DataSources(t *testing.T) {
	t.Parallel()
	tfDir, err := filepath.Abs(filepath.Join("testdata", "tf_indexed_modules"))
	require.NoError(t, err)

	config, err := LoadConfig(tfDir)
	require.NoError(t, err)

	rawState, err := LoadRawState(filepath.Join(tfDir, "terraform.tfstate"))
	require.NoError(t, err)

	// Add a data source inside module.pet[0].
	petModule := rawState.Module(addrs.RootModuleInstance.Child("pet", addrs.IntKey(0)))
	require.NotNil(t, petModule)

	petModule.SetResourceInstanceCurrent(
		addrs.ResourceInstance{
			Resource: addrs.Resource{
				Mode: addrs.DataResourceMode,
				Type: "aws_caller_identity",
				Name: "current",
			},
			Key: addrs.NoKey,
		},
		&states.ResourceInstanceObjectSrc{
			AttrsJSON: []byte(`{"account_id":"123456789","id":"123456789"}`),
		},
		addrs.AbsProviderConfig{
			Provider: addrs.MustParseProviderSourceString("registry.opentofu.org/hashicorp/aws"),
		},
		nil,
	)

	mm, err := BuildModuleMap(config, nil, rawState, nil, "test-stack", "test-project")
	require.NoError(t, err)

	pet0 := mm.Modules["pet[0]"]
	require.NotNil(t, pet0)

	// Should have 2 resources: the managed random_pet and the data source.
	require.Len(t, pet0.Resources, 2)

	// Find the data source entry.
	var dataRes *ModuleResource
	for i := range pet0.Resources {
		if pet0.Resources[i].Mode == "data" {
			dataRes = &pet0.Resources[i]
			break
		}
	}
	require.NotNil(t, dataRes, "expected a data source in pet[0] resources")

	assert.Equal(t, "data", dataRes.Mode)
	assert.Equal(t, "module.pet[0].data.aws_caller_identity.current", dataRes.TerraformAddress)
	assert.Equal(t, "", dataRes.TranslatedURN)
	assert.Equal(t, "123456789", dataRes.ImportID)

	// The managed resource should still be there.
	var managedRes *ModuleResource
	for i := range pet0.Resources {
		if pet0.Resources[i].Mode == "managed" {
			managedRes = &pet0.Resources[i]
			break
		}
	}
	require.NotNil(t, managedRes)
	assert.Equal(t, "managed", managedRes.Mode)
	assert.Equal(t, "module.pet[0].random_pet.this", managedRes.TerraformAddress)
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./pkg/ -run TestBuildModuleMap_DataSources -v -count=1`
Expected: PASS (the mode-aware logic from Task 1 Step 3 already handles this)

- [ ] **Step 3: Commit**

```bash
git add pkg/module_map_test.go
git commit -m "test: add data source inside module test"
```

---

### Task 4: Update `TestWriteModuleMap` for `rootResources` round-trip

**Files:**
- Modify: `pkg/module_map_test.go`

- [ ] **Step 1: Update `TestWriteModuleMap` to include `RootResources`**

In `pkg/module_map_test.go`, update the `ModuleMap` literal in `TestWriteModuleMap` to add root resources:

```go
	mm := &ModuleMap{
		Modules: map[string]*ModuleMapEntry{
			"vpc": {
				TerraformPath: "module.vpc",
				Source:        "./modules/vpc",
				Resources: []ModuleResource{{
					Mode:             "managed",
					TranslatedURN:    "urn:pulumi:stack::project::aws:ec2/vpc:Vpc::main",
					TerraformAddress: "module.vpc.aws_vpc.main",
					ImportID:         "vpc-12345",
				}},
				Interface: &ModuleInterface{
					Inputs:  []ModuleInterfaceField{{Name: "cidr", Required: true}},
					Outputs: []ModuleInterfaceField{{Name: "id"}},
				},
			},
		},
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:stack::project::aws:s3/bucket:Bucket::example",
				TerraformAddress: "aws_s3_bucket.example",
				ImportID:         "my-bucket",
			},
			{
				Mode:             "data",
				TranslatedURN:    "",
				TerraformAddress: "data.terraform_remote_state.old",
				ImportID:         "",
			},
		},
	}
```

Add assertions after the existing ones:

```go
	// Root resources round-trip.
	require.Len(t, got.RootResources, 2)
	assert.Equal(t, "managed", got.RootResources[0].Mode)
	assert.Equal(t, "aws_s3_bucket.example", got.RootResources[0].TerraformAddress)
	assert.Equal(t, "my-bucket", got.RootResources[0].ImportID)
	assert.Equal(t, "data", got.RootResources[1].Mode)
	assert.Equal(t, "", got.RootResources[1].TranslatedURN)
	assert.Equal(t, "data.terraform_remote_state.old", got.RootResources[1].TerraformAddress)
```

- [ ] **Step 2: Run test**

Run: `go test ./pkg/ -run TestWriteModuleMap -v -count=1`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add pkg/module_map_test.go
git commit -m "test: add rootResources serialization round-trip"
```

---

### Task 5: Fix `rawStateFromTfjson` to preserve data source mode

**Files:**
- Modify: `pkg/generate_module_map.go:118-169`
- Modify: `pkg/module_map_test.go` (new test)

- [ ] **Step 1: Write the failing test `TestRawStateFromTfjson_DataSources`**

Add to `pkg/module_map_test.go`:

```go
func TestRawStateFromTfjson_DataSources(t *testing.T) {
	t.Parallel()

	// Build a minimal tfjson state with a data source at root level.
	tfjsonState := &tfjson.State{
		FormatVersion: "1.0",
		Values: &tfjson.StateValues{
			RootModule: &tfjson.StateModule{
				Resources: []*tfjson.StateResource{
					{
						Address:      "data.terraform_remote_state.old",
						Mode:         tfjson.DataResourceMode,
						Type:         "terraform_remote_state",
						Name:         "old",
						ProviderName: "terraform.io/builtin/terraform",
						AttributeValues: map[string]interface{}{
							"backend": "s3",
						},
					},
					{
						Address:      "aws_s3_bucket.example",
						Mode:         tfjson.ManagedResourceMode,
						Type:         "aws_s3_bucket",
						Name:         "example",
						ProviderName: "registry.opentofu.org/hashicorp/aws",
						AttributeValues: map[string]interface{}{
							"id":     "my-bucket",
							"bucket": "my-bucket",
						},
					},
				},
			},
		},
	}

	state := rawStateFromTfjson(tfjsonState)

	// Check that the root module has both resources with correct modes.
	rootModule := state.RootModule()
	require.NotNil(t, rootModule)

	// Data source should have DataResourceMode.
	dataRes := rootModule.Resource(addrs.Resource{
		Mode: addrs.DataResourceMode,
		Type: "terraform_remote_state",
		Name: "old",
	})
	require.NotNil(t, dataRes, "expected data.terraform_remote_state.old in root module")

	// Managed resource should have ManagedResourceMode.
	managedRes := rootModule.Resource(addrs.Resource{
		Mode: addrs.ManagedResourceMode,
		Type: "aws_s3_bucket",
		Name: "example",
	})
	require.NotNil(t, managedRes, "expected aws_s3_bucket.example in root module")
}
```

Add import: `tfjson "github.com/hashicorp/terraform-json"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/ -run TestRawStateFromTfjson_DataSources -v -count=1`
Expected: FAIL — data source not found because: (a) `VisitOptions` skips data sources by default, and (b) mode is hardcoded to `ManagedResourceMode`

- [ ] **Step 3: Fix `rawStateFromTfjson`**

In `pkg/generate_module_map.go`, update `rawStateFromTfjson`:

Change the `VisitOptions` (line 166) to include data sources:
```go
	}, &tofuutil.VisitOptions{IncludeDataSources: true})
```

Change the resource mode mapping (lines 147-151) to use `r.Mode`:
```go
		// Build resource address with correct mode.
		mode := addrs.ManagedResourceMode
		if r.Mode == tfjson.DataResourceMode {
			mode = addrs.DataResourceMode
		}
		resAddr := addrs.Resource{
			Mode: mode,
			Type: r.Type,
			Name: r.Name,
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/ -run TestRawStateFromTfjson_DataSources -v -count=1`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/generate_module_map.go pkg/module_map_test.go
git commit -m "fix: preserve data source mode in rawStateFromTfjson"
```

---

## Verification

- [ ] **Final: Run full test suite and build**

```bash
go build ./...
go test ./... -count=1
```

- [ ] **Manual verification against veridos project**

```bash
go run . module-map \
  --from /Users/jdavenport/pulumi-repos/veridos/factory-infrastructure/environments/develop \
  --state-file /Users/jdavenport/pulumi-repos/veridos/factory-infrastructure/environments/develop/terraform.tfstate \
  --out /tmp/module-map.json \
  --pulumi-project veridos \
  --pulumi-stack dev
```

Verify output contains:
- `"rootResources"` array with managed and data entries
- `"mode": "managed"` on module resources
- `"mode": "data"` on `data.terraform_remote_state.old` with `"translatedUrn": ""`
