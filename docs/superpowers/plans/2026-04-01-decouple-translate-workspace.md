# Decouple State Translation from Pulumi Workspace — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `TranslateState` and `InsertResourcesIntoDeployment` pure functions of strings, removing the dependency on a real Pulumi workspace.

**Architecture:** `InsertResourcesIntoDeployment` constructs the Stack resource internally from `stackName` + `projectName` instead of receiving a deployment. `TranslateState` takes strings instead of a workspace path. `TranslateAndWriteState` resolves names from optional CLI flags or workspace fallback.

**Tech Stack:** Go, `gopkg.in/yaml.v3` (already in `go.mod`, for reading `Pulumi.yaml`), cobra CLI flags.

**Note on Stack resource ID:** The internally-constructed Stack resource has no `ID` field (empty string). This is safe — `pulumi stack import` does not require an ID on the Stack resource, and the Pulumi engine sets it to empty for component/Stack resources.

**Spec:** `docs/superpowers/specs/2026-04-01-decouple-translate-from-workspace.md`

**TDD order for every task:** Write failing tests → implement code → verify tests pass → commit.

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/pulumi_state.go` | Change `InsertResourcesIntoDeployment` signature, construct Stack resource internally, delete `GetDeployment`/`findStackResource`/`DeploymentResult`, add `getProjectName` |
| Modify | `pkg/pulumi_state_test.go` | Update `InsertResourcesIntoDeployment` tests to new signature, replace validation tests, delete `TestGetDeployment`/`runCommand` |
| Update | `pkg/testdata/TestInsertResourcesIntoDeployment.golden` | Update autogold snapshot (Stack resource now constructed internally) |
| Modify | `pkg/state_adapter.go` | Change `TranslateState` and `TranslateAndWriteState` signatures |
| Modify | `pkg/state_adapter_test.go` | Delete `createPulumiStack`, update `translateStateFromJson` to pass strings, add `t.Parallel()` to all tests |
| Modify | `cmd/stack.go` | Add `--pulumi-stack` and `--pulumi-project` flags |
| Modify | `test/translate_test.go` | Update `TranslateAndWriteState` calls to pass override strings |

---

## Task 1: Refactor `InsertResourcesIntoDeployment` to construct Stack resource

**Files:** Modify `pkg/pulumi_state.go:147-220`, `pkg/pulumi_state_test.go`

- [ ] **Step 1: Write failing tests for new signature**

Replace the existing tests with tests that use the new signature (no `deployment` parameter). Add validation tests for empty strings.

```go
// In pkg/pulumi_state_test.go — replace TestInsertResourcesIntoDeployment_ZeroResources
// and TestInsertResourcesIntoDeployment_MultipleResources with:

func TestInsertResourcesIntoDeployment_EmptyStackName(t *testing.T) {
	t.Parallel()
	_, err := InsertResourcesIntoDeployment(&PulumiState{}, "", "project")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stackName")
}

func TestInsertResourcesIntoDeployment_EmptyProjectName(t *testing.T) {
	t.Parallel()
	_, err := InsertResourcesIntoDeployment(&PulumiState{}, "dev", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "projectName")
}
```

Update the existing `TestInsertResourcesIntoDeployment` call to remove the `deployment` parameter:

```go
// Change from:
data, err := InsertResourcesIntoDeployment(&PulumiState{...}, "dev", "example", apitype.DeploymentV3{
    Resources: []apitype.ResourceV3{{
        URN:  "urn:pulumi:dev::example::pulumi:pulumi:Stack::example-dev",
        Type: "pulumi:pulumi:Stack",
        ID:   "a339fe8e-e15d-4203-8719-c0ca5d3f414e",
    }},
})
// Change to:
data, err := InsertResourcesIntoDeployment(&PulumiState{...}, "dev", "example")
```

Same for `TestInsertResourcesIntoDeployment_multi_provider`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/ -run "TestInsertResourcesIntoDeployment" -v`
Expected: Compilation error — `InsertResourcesIntoDeployment` still has old signature.

- [ ] **Step 3: Implement the new signature**

In `pkg/pulumi_state.go`, replace:

```go
func InsertResourcesIntoDeployment(state *PulumiState, stackName, projectName string, deployment apitype.DeploymentV3) (apitype.DeploymentV3, error) {
	nres := len(deployment.Resources)

	if nres == 0 {
		return apitype.DeploymentV3{}, fmt.Errorf(
			"No Stack resource found in the Pulumi state for stack '%q'. "+
				"Please run `pulumi up` to populate the initial Pulumi state and configure secrets providers, then try again.",
			stackName)
	}

	if nres > 1 {
		return apitype.DeploymentV3{}, fmt.Errorf(
			"Found %d resources in stack %q, expected 1 (Stack resource). "+
				"Migrating resources into stacks with pre-existing resources is not yet supported",
			nres, stackName)
	}

	now := time.Now()

	stackResource, err := findStackResource(deployment)
	if err != nil {
		return apitype.DeploymentV3{}, err
	}
```

With:

```go
func InsertResourcesIntoDeployment(state *PulumiState, stackName, projectName string) (apitype.DeploymentV3, error) {
	if stackName == "" {
		return apitype.DeploymentV3{}, fmt.Errorf("stackName must not be empty")
	}
	if projectName == "" {
		return apitype.DeploymentV3{}, fmt.Errorf("projectName must not be empty")
	}

	now := time.Now()

	stackURN := makeUrn(stackName, projectName, "pulumi:pulumi:Stack", projectName+"-"+stackName)

	deployment := apitype.DeploymentV3{}
	deployment.Resources = append(deployment.Resources, apitype.ResourceV3{
		URN:  stackURN,
		Type: "pulumi:pulumi:Stack",
	})
```

Then replace all references to `stackResource.URN` with `stackURN` in the rest of the function.

Delete `findStackResource` and `DeploymentResult` struct.

- [ ] **Step 4: Delete old tests and code**

Delete from `pkg/pulumi_state_test.go`:
- `TestInsertResourcesIntoDeployment_ZeroResources` (entire function)
- `TestInsertResourcesIntoDeployment_MultipleResources` (entire function)
- `TestGetDeployment` (entire function)
- `runCommand` helper function

Delete from `pkg/pulumi_state.go`:
- `GetDeployment` function
- `findStackResource` function
- `DeploymentResult` struct
- Remove `"context"` and `"github.com/pulumi/pulumi/sdk/v3/go/auto"` from imports if no longer used

- [ ] **Step 5: Update autogold snapshot**

Run: `go test ./pkg/ -run "TestInsertResourcesIntoDeployment$" -update`

This regenerates `pkg/testdata/TestInsertResourcesIntoDeployment.golden` with the new Stack resource format (no ID field, since we construct it internally).

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./pkg/ -run "TestInsertResourcesIntoDeployment" -v`
Expected: All PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/pulumi_state.go pkg/pulumi_state_test.go pkg/testdata/TestInsertResourcesIntoDeployment.golden
git commit -m "refactor: InsertResourcesIntoDeployment constructs Stack resource from strings"
```

---

## Task 2: Refactor `TranslateState` and add `getProjectName`

**Files:** Modify `pkg/state_adapter.go:50-151`, `pkg/pulumi_state.go`, `pkg/pulumi_state_test.go`

- [ ] **Step 1: Write test for `getProjectName`**

Create a test fixture and test in `pkg/pulumi_state_test.go`:

```go
func TestGetProjectName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), []byte("name: my-project\nruntime: go\n"), 0644)
	require.NoError(t, err)

	name, err := getProjectName(dir)
	require.NoError(t, err)
	require.Equal(t, "my-project", name)
}

func TestGetProjectName_Missing(t *testing.T) {
	t.Parallel()
	_, err := getProjectName(t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Pulumi.yaml")
}

func TestGetProjectName_EmptyName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), []byte("runtime: go\n"), 0644)
	require.NoError(t, err)

	_, err = getProjectName(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}
```

Add `"path/filepath"` to imports in `pkg/pulumi_state_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/ -run "TestGetProjectName" -v`
Expected: Compilation error — `getProjectName` not defined.

- [ ] **Step 3: Implement `getProjectName`**

In `pkg/pulumi_state.go`, add after `getStackName`:

```go
func getProjectName(projectDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "Pulumi.yaml"))
	if err != nil {
		return "", fmt.Errorf("failed to read Pulumi.yaml: %w", err)
	}

	var project struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &project); err != nil {
		return "", fmt.Errorf("failed to parse Pulumi.yaml: %w", err)
	}
	if project.Name == "" {
		return "", fmt.Errorf("project name is empty in Pulumi.yaml")
	}
	return project.Name, nil
}
```

Add imports: `"path/filepath"`, `"gopkg.in/yaml.v3"` (already in `go.mod`).

- [ ] **Step 4: Run `getProjectName` tests**

Run: `go test ./pkg/ -run "TestGetProjectName" -v`
Expected: All PASS.

- [ ] **Step 5: Change `TranslateState` signature**

In `pkg/state_adapter.go`, change:

```go
func TranslateState(ctx context.Context, tfState *tfjson.State, providerVersions map[string]string, pulumiProgramDir string) (*TranslateStateResult, error) {
```

To:

```go
func TranslateState(ctx context.Context, tfState *tfjson.State, providerVersions map[string]string, stackName, projectName string) (*TranslateStateResult, error) {
```

Replace the body — remove `GetDeployment` call, pass strings to `InsertResourcesIntoDeployment`:

```go
func TranslateState(ctx context.Context, tfState *tfjson.State, providerVersions map[string]string, stackName, projectName string) (*TranslateStateResult, error) {
	pulumiProviders, err := GetPulumiProvidersForTerraformState(tfState, providerVersions)
	if err != nil {
		return nil, err
	}

	pulumiState, errorMessages, err := convertState(tfState, pulumiProviders)
	if err != nil {
		return nil, fmt.Errorf("failed to convert state: %w", err)
	}

	editedDeployment, err := InsertResourcesIntoDeployment(pulumiState, stackName, projectName)
	if err != nil {
		return nil, fmt.Errorf("failed to insert resources into deployment: %w", err)
	}

	requiredProviders := slices.Collect(maps.Values(pulumiProviders))

	return &TranslateStateResult{
		Export: StackExport{
			Deployment: editedDeployment,
			Version:    3,
		},
		RequiredProviders: requiredProviders,
		ErrorMessages:     errorMessages,
	}, nil
}
```

- [ ] **Step 6: Change `TranslateAndWriteState` signature**

In `pkg/state_adapter.go`, change:

```go
func TranslateAndWriteState(
	ctx context.Context,
	tfDir string,
	pulumiProgramDir string,
	outputFilePath string,
	requiredProvidersOutputFilePath string,
	strict bool,
) error {
```

To:

```go
func TranslateAndWriteState(
	ctx context.Context,
	tfDir string,
	pulumiProgramDir string,
	outputFilePath string,
	requiredProvidersOutputFilePath string,
	strict bool,
	stackNameOverride string,
	projectNameOverride string,
) error {
```

Add name resolution after `tofu.GetProviderVersions` and before `TranslateState`. Use explicit `var` declarations to avoid `:=` shadowing:

```go
	// Resolve stack and project names from overrides or workspace fallback
	var stackName string
	if stackNameOverride != "" {
		stackName = stackNameOverride
	} else {
		var err error
		stackName, err = getStackName(pulumiProgramDir)
		if err != nil {
			return fmt.Errorf("failed to get stack name: %w", err)
		}
	}

	var projectName string
	if projectNameOverride != "" {
		projectName = projectNameOverride
	} else {
		var err error
		projectName, err = getProjectName(pulumiProgramDir)
		if err != nil {
			return fmt.Errorf("failed to get project name: %w", err)
		}
	}

	res, err := TranslateState(ctx, tfState, providerVersions.ProviderSelections, stackName, projectName)
```

- [ ] **Step 7: Fix compilation errors**

Update the one caller in `cmd/stack.go` (line 71):

```go
// From:
err := pkg.TranslateAndWriteState(cmd.Context(), from, to, out, plugins, strict)
// To:
err := pkg.TranslateAndWriteState(cmd.Context(), from, to, out, plugins, strict, "", "")
```

This passes empty overrides so the fallback path is used (same behavior as today).

- [ ] **Step 8: Run tests to verify compilation**

Run: `go build ./...`
Expected: No errors.

- [ ] **Step 9: Commit**

```bash
git add pkg/state_adapter.go pkg/pulumi_state.go pkg/pulumi_state_test.go cmd/stack.go
git commit -m "refactor: TranslateState takes stackName/projectName strings instead of workspace path"
```

---

## Task 3: Update unit tests to remove workspace dependency

**Files:** Modify `pkg/state_adapter_test.go`

- [ ] **Step 1: Delete `createPulumiStack` and update `translateStateFromJson`**

In `pkg/state_adapter_test.go`:

Delete the `createPulumiStack` function (lines 341-349).

Change `translateStateFromJson` from:

```go
func translateStateFromJson(ctx context.Context, tfStateJson string, pulumiProgramDir string) (*TranslateStateResult, error) {
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: tfStateJson,
	})
	if err != nil {
		return nil, err
	}
	return TranslateState(ctx, tfState, nil, pulumiProgramDir)
}
```

To:

```go
func translateStateFromJson(ctx context.Context, tfStateJson string) (*TranslateStateResult, error) {
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: tfStateJson,
	})
	if err != nil {
		return nil, err
	}
	return TranslateState(ctx, tfState, nil, "dev", "test-project")
}
```

- [ ] **Step 2: Update all test call sites**

Update every test that called `translateStateFromJson(ctx, path, stackFolder)` to `translateStateFromJson(ctx, path)`. Remove the `stackFolder := createPulumiStack(t)` line from each.

Add `t.Parallel()` to all tests that don't have it (`TestConvertSimple`, `TestConvertWithDependencies`, `TestConvertInvolved`, `TestConvertTwoModules`, `TestConvertNestedModules`). `TestConvertWithSensitiveValues` already has `t.Parallel()` but still needs its call site updated.

Remove unused imports (`"os"`).

All 6 tests that use `translateStateFromJson` become:

```go
func TestConvertSimple(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data, err := translateStateFromJson(ctx, "testdata/bucket_state.json")
	if err != nil {
		t.Fatalf("failed to convert Terraform state: %v", err)
	}
	require.Len(t, data.Export.Deployment.Resources, 3)
}
```

Same pattern for: `TestConvertWithDependencies`, `TestConvertInvolved`, `TestConvertTwoModules`, `TestConvertNestedModules`, `TestConvertWithSensitiveValues`.

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./pkg/ -run "TestConvert" -v -timeout 60s`
Expected: All PASS. Should complete in seconds, not minutes.

- [ ] **Step 4: Run full test suite**

Run: `go test ./pkg/... -timeout 60s`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/state_adapter_test.go
git commit -m "test: remove workspace dependency from unit tests, add t.Parallel()"
```

---

## Task 4: Add CLI flags and update integration tests

**Files:** Modify `cmd/stack.go`, `test/translate_test.go`

- [ ] **Step 1: Add CLI flags**

In `cmd/stack.go`, add variables and flags:

```go
var pulumiStack string
var pulumiProject string
```

In the `RunE` function, pass them:

```go
err := pkg.TranslateAndWriteState(cmd.Context(), from, to, out, plugins, strict, pulumiStack, pulumiProject)
```

Register the flags after the existing ones:

```go
cmd.Flags().StringVar(&pulumiStack, "pulumi-stack", "", "Override Pulumi stack name (skip auto-detection)")
cmd.Flags().StringVar(&pulumiProject, "pulumi-project", "", "Override Pulumi project name (skip auto-detection)")
```

- [ ] **Step 2: Update integration tests**

In `test/translate_test.go`, update all `TranslateAndWriteState` calls to pass empty overrides:

```go
// From:
err := pkg.TranslateAndWriteState(ctx, statePath, stackFolder, filepath.Join(stackFolder, "state.json"), "", false)
// To:
err := pkg.TranslateAndWriteState(ctx, statePath, stackFolder, filepath.Join(stackFolder, "state.json"), "", false, "", "")
```

Same for all 4 integration test functions (`TestTranslateBasic`, `TestTranslateBasicWithDependencies`, `TestTranslateBasicWithEdit`, `TestTranslateWithDependency`).

- [ ] **Step 3: Build and verify**

Run: `go build ./...`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/stack.go test/translate_test.go
git commit -m "feat: add --pulumi-stack and --pulumi-project CLI flags"
```

---

## Verification

After all tasks:

- [ ] `go test ./pkg/... -timeout 60s` — all pass, completes in under 30s
- [ ] `go build ./...` — no compilation errors
- [ ] `go vet ./...` — no warnings
- [ ] Unit tests no longer spawn subprocesses or require network access
- [ ] Integration tests in `test/` still work (they use their own `createPulumiStack`)
