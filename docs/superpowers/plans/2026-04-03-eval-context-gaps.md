# Eval Context Gaps — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fill remaining eval context gaps so component inputs/outputs resolve for real-world TF projects using remote modules, data sources, locals, cross-module refs, and path refs.

**Architecture:** Five independent fixes to the HCL evaluation context in `populateComponentsFromHCL`. Each adds a new variable namespace or source resolution path. All changes are in `pkg/component_populate.go`, `pkg/hcl/discovery.go`, `pkg/hcl/parser.go`, and `pkg/hcl/evaluator.go`. Tested against the DNS-to-DB real-world fixture.

**Tech Stack:** Go, `hashicorp/hcl/v2`, `zclconf/go-cty`, existing `pkg/hcl` and `pkg/tofu` packages.

**Branch:** `feat/mc-09-state-population` (state population pipeline). Restack `feat/mc-10-discovery-acceptance` after.

**TDD order for every task:** Write failing test → run to verify failure → implement → verify pass → commit.

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/hcl/discovery.go` | Add `ResolveModuleSourceFromCache` to read `.terraform/modules/modules.json` |
| Create | `pkg/hcl/discovery_test.go` | Tests for module cache resolution (extend existing file) |
| Modify | `pkg/hcl/parser.go` | Add `LoadAllTfvars` (loads `terraform.tfvars` + `*.auto.tfvars`), add `ParseLocals` |
| Modify | `pkg/hcl/parser_test.go` | Tests for auto.tfvars loading and locals parsing |
| Modify | `pkg/component_populate.go` | Wire all new eval context sources: remote module resolution, data sources, path refs, locals, module cross-refs, auto.tfvars |
| Modify | `pkg/component_populate_test.go` | Tests for each gap fix |

---

## Task 1: Resolve remote module sources from `.terraform/modules/` cache

The biggest unlock. After `tofu init`, remote modules (registry, git) are cached at `.terraform/modules/<key>/`. A `modules.json` manifest maps module keys to directories. Parsing this lets us find HCL source for any module, not just local ones.

**Files:** Modify `pkg/hcl/discovery.go`, `pkg/hcl/discovery_test.go`

- [ ] **Step 1: Create test fixture**

```
pkg/hcl/testdata/root_with_module_cache/.terraform/modules/modules.json
```

```json
{
  "Modules": [
    {"Key": "", "Source": "", "Dir": "."},
    {"Key": "pet", "Source": "registry.opentofu.org/someorg/pet/random", "Dir": ".terraform/modules/pet"},
    {"Key": "nested.child", "Source": "./modules/child", "Dir": ".terraform/modules/nested/modules/child"}
  ]
}
```

Also create dummy module dirs with a `main.tf` so path resolution can be verified:

```
pkg/hcl/testdata/root_with_module_cache/.terraform/modules/pet/main.tf
pkg/hcl/testdata/root_with_module_cache/.terraform/modules/nested/modules/child/main.tf
```

- [ ] **Step 2: Write failing tests**

```go
// In pkg/hcl/discovery_test.go
func TestResolveModuleSourceFromCache(t *testing.T) {
	sources, err := ResolveModuleSourcesFromCache("testdata/root_with_module_cache")
	require.NoError(t, err)
	require.Equal(t, "testdata/root_with_module_cache/.terraform/modules/pet", sources["module.pet"])
	require.Equal(t, "testdata/root_with_module_cache/.terraform/modules/nested/modules/child", sources["module.nested.module.child"])
	_, hasRoot := sources[""]
	require.False(t, hasRoot, "root module entry should be excluded")
}

func TestResolveModuleSourceFromCache_NoCacheDir(t *testing.T) {
	sources, err := ResolveModuleSourcesFromCache("testdata/root_with_pet")
	require.NoError(t, err)
	require.Len(t, sources, 0) // no .terraform dir
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./pkg/hcl/ -run "TestResolveModuleSourceFromCache" -v`

- [ ] **Step 4: Implement**

```go
// In pkg/hcl/discovery.go

// moduleManifest represents the .terraform/modules/modules.json structure.
type moduleManifest struct {
	Modules []moduleManifestEntry `json:"Modules"`
}

type moduleManifestEntry struct {
	Key    string `json:"Key"`
	Source string `json:"Source"`
	Dir    string `json:"Dir"`
}

// ResolveModuleSourcesFromCache reads .terraform/modules/modules.json and returns
// a map of "module.<name>" -> absolute directory path for each cached module.
// This resolves remote modules (registry, git) that tofu init has downloaded.
// Returns empty map if no cache exists (not an error).
func ResolveModuleSourcesFromCache(rootDir string) (map[string]string, error) {
	manifestPath := filepath.Join(rootDir, ".terraform", "modules", "modules.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return map[string]string{}, nil // no cache, not an error
	}

	var manifest moduleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing modules.json: %w", err)
	}

	sources := map[string]string{}
	for _, entry := range manifest.Modules {
		if entry.Key == "" {
			continue // skip root module
		}
		// Convert manifest key (e.g., "rdsdb.db_subnet_group") to TF module path
		// (e.g., "module.rdsdb.module.db_subnet_group")
		moduleAddr := manifestKeyToModuleAddr(entry.Key)
		// Resolve Dir relative to rootDir
		dir := entry.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(rootDir, dir)
		}
		sources[moduleAddr] = dir
	}
	return sources, nil
}

// manifestKeyToModuleAddr converts a modules.json key like "rdsdb.db_subnet_group"
// to a TF module address like "module.rdsdb.module.db_subnet_group".
func manifestKeyToModuleAddr(key string) string {
	parts := strings.Split(key, ".")
	var addr []string
	for _, p := range parts {
		addr = append(addr, "module."+p)
	}
	return strings.Join(addr, ".")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/hcl/ -run "TestResolveModuleSourceFromCache" -v`

- [ ] **Step 6: Wire into `populateComponentsFromHCL`**

In `pkg/component_populate.go`, after the existing source resolution block, add cache-based resolution as a fallback:

```go
// Resolve HCL source path: override > local auto-discovery > module cache
sourcePath := ""
if override, ok := sourceOverrides["module."+moduleName]; ok {
	sourcePath = override
} else if callSite, ok := callSiteMap[moduleName]; ok && hclpkg.IsLocalModuleSource(callSite.Source) {
	sourcePath = filepath.Join(tfSourceDir, callSite.Source)
}
// Fallback: resolve from .terraform/modules/ cache (remote modules)
if sourcePath == "" {
	if cached, ok := cachedModuleSources[node.modulePath]; ok {
		sourcePath = cached
	}
}
```

Where `cachedModuleSources` is built once at the top of the function:

```go
cachedModuleSources, _ := hclpkg.ResolveModuleSourcesFromCache(tfSourceDir)
```

- [ ] **Step 7: Run full test suite, commit**

Run: `go test ./pkg/... -count=1`

```bash
git add pkg/hcl/discovery.go pkg/hcl/discovery_test.go pkg/component_populate.go
git commit -m "feat: resolve remote module sources from .terraform/modules cache"
```

---

## Task 2: Include data sources in resource attr map

Currently `buildResourceAttrMap` only visits managed resources. Data source attributes (e.g., `data.aws_ami.amzlinux2.id`, `data.aws_route53_zone.mydomain.zone_id`) are missing from the eval context.

**Files:** Modify `pkg/component_populate.go`, `pkg/component_populate_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestPopulateComponentsFromHCL_DataSourceRef(t *testing.T) {
	// Create a fixture where a module call site references data.random_pet.base.id
	// and verify it resolves when data sources are in the resource attr map
}
```

Actually, the fix is in `buildResourceAttrMap` — just add `IncludeDataSources: true` to the `VisitOptions`. But we need to handle the `data.` prefix in the variable namespace. Data sources in HCL are referenced as `data.<type>.<name>`, so they need to go under a `data` key in the eval context, not mixed with managed resources.

- [ ] **Step 1: Write failing test**

```go
// In pkg/component_populate_test.go
func TestBuildResourceAttrMap_IncludesDataSources(t *testing.T) {
	// Use tofu_state_dns_to_db.json which has data sources
	ctx := context.Background()
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: "testdata/tofu_state_dns_to_db.json",
	})
	require.NoError(t, err)

	attrs := buildResourceAttrMap(tfState)

	// Should have data sources under "data" key
	dataAttrs, hasData := attrs["data"]
	require.True(t, hasData, "should have 'data' key for data sources")

	// dns-to-db has data.aws_ami and data.aws_route53_zone
	require.NotNil(t, dataAttrs)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/ -run "TestBuildResourceAttrMap_IncludesDataSources" -v`

- [ ] **Step 3: Implement**

Modify `buildResourceAttrMap` in `pkg/component_populate.go`:

```go
func buildResourceAttrMap(tfState *tfjson.State) map[string]map[string]cty.Value {
	result := map[string]map[string]cty.Value{}
	if tfState == nil {
		return result
	}

	// Include both managed resources AND data sources
	tofu.VisitResources(tfState, func(r *tfjson.StateResource) error {
		addr := r.Address
		parts := splitAddressParts(addr)
		if len(parts) < 2 {
			return nil
		}
		resType := parts[len(parts)-2]
		resName := parts[len(parts)-1]

		// Data sources: HCL references them as data.<type>.<name>
		// They appear in state as "data.<type>.<name>" with mode "data"
		if r.Mode == tfjson.DataResourceMode {
			if r.AttributeValues != nil {
				attrs := map[string]cty.Value{}
				for k, v := range r.AttributeValues {
					attrs[k] = interfaceToCty(v)
				}
				// Build nested: data -> type -> name -> attrs
				if _, ok := result["data"]; !ok {
					result["data"] = map[string]cty.Value{}
				}
				// We need data.aws_ami.amzlinux2 -> need data to be an object
				// containing aws_ami which is an object containing amzlinux2
				// For now, store as data[type_name] = attrs to handle "data.aws_ami.name" refs
				dataKey := resType + "." + resName
				result["data"] = map[string]cty.Value{} // will need nesting, see step 4
			}
			return nil
		}

		// Managed resources (existing code)
		if r.AttributeValues != nil {
			attrs := map[string]cty.Value{}
			for k, v := range r.AttributeValues {
				attrs[k] = interfaceToCty(v)
			}
			if _, ok := result[resType]; !ok {
				result[resType] = map[string]cty.Value{}
			}
			result[resType][resName] = cty.ObjectVal(attrs)
		}
		return nil
	}, &tofu.VisitOptions{IncludeDataSources: true})

	return result
}
```

For data sources, HCL evaluates `data.aws_ami.amzlinux2.id` by looking up `Variables["data"]` → object with key `aws_ami` → object with key `amzlinux2` → attr `id`. So we need nested objects in the eval context. Build a separate `dataSourceAttrs` map and add it via `AddVariables`:

```go
// In populateComponentsFromHCL, build data source attrs separately:
func buildDataSourceAttrMap(tfState *tfjson.State) map[string]cty.Value {
	// Returns a single cty.Value for the "data" variable:
	// data = { aws_ami = { amzlinux2 = { id = "...", ... } }, aws_route53_zone = { ... } }
	typeMap := map[string]map[string]cty.Value{}
	tofu.VisitResources(tfState, func(r *tfjson.StateResource) error {
		if r.Mode != tfjson.DataResourceMode || r.AttributeValues == nil {
			return nil
		}
		parts := splitAddressParts(r.Address)
		resType := parts[len(parts)-2]
		resName := parts[len(parts)-1]
		attrs := map[string]cty.Value{}
		for k, v := range r.AttributeValues {
			attrs[k] = interfaceToCty(v)
		}
		if _, ok := typeMap[resType]; !ok {
			typeMap[resType] = map[string]cty.Value{}
		}
		typeMap[resType][resName] = cty.ObjectVal(attrs)
		return nil
	}, &tofu.VisitOptions{IncludeDataSources: true})

	if len(typeMap) == 0 {
		return nil
	}
	result := map[string]cty.Value{}
	for typeName, instances := range typeMap {
		result[typeName] = cty.ObjectVal(instances)
	}
	return result
}
```

Then add to eval context: `evalCtx.AddVariables(map[string]cty.Value{"data": cty.ObjectVal(dataSourceAttrs)})`

- [ ] **Step 4: Run tests, commit**

```bash
git add pkg/component_populate.go pkg/component_populate_test.go
git commit -m "feat: include data source attributes in HCL eval context"
```

---

## Task 3: Add `path.*` refs to eval context

Trivial. Add `path.module`, `path.root`, `path.cwd` to the eval context as cty string values.

**Files:** Modify `pkg/component_populate.go`, `pkg/component_populate_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestPopulateComponentsFromHCL_PathRef(t *testing.T) {
	// Create a fixture with: user_data = file("${path.module}/install.sh")
	// Verify no "Unknown variable path" warning when path.module is set
}
```

- [ ] **Step 2: Implement**

In `populateComponentsFromHCL`, add path variables to eval context:

```go
// Add path.* refs
pathVars := map[string]cty.Value{
	"module": cty.StringVal(tfSourceDir),
	"root":   cty.StringVal(tfSourceDir),
	"cwd":    cty.StringVal(tfSourceDir),
}
evalCtx.AddVariables(map[string]cty.Value{
	"path": cty.ObjectVal(pathVars),
})
```

Also set `lang.Scope.BaseDir` to `tfSourceDir` in `buildFunctionTable` so `file()` and `templatefile()` resolve paths correctly. This requires passing `tfSourceDir` to `NewEvalContext`.

- [ ] **Step 3: Run tests, commit**

```bash
git add pkg/component_populate.go pkg/component_populate_test.go pkg/hcl/evaluator.go
git commit -m "feat: add path.module, path.root, path.cwd to eval context"
```

---

## Task 4: Load `.auto.tfvars` files

Currently only `terraform.tfvars` is loaded. Terraform also automatically loads `*.auto.tfvars` files. The DNS-to-DB project has `ec2instance.auto.tfvars`, `rdsdb.auto.tfvars`, `vpc.auto.tfvars`.

**Files:** Modify `pkg/hcl/parser.go`, `pkg/hcl/parser_test.go`, `pkg/component_populate.go`

- [ ] **Step 1: Write failing test**

```go
// In pkg/hcl/parser_test.go
func TestLoadAllTfvars(t *testing.T) {
	vars, err := LoadAllTfvars("testdata/root_with_auto_tfvars")
	require.NoError(t, err)
	// terraform.tfvars has env = "prod"
	require.Equal(t, "prod", vars["env"].AsString())
	// vpc.auto.tfvars has vpc_name = "myvpc"
	require.Equal(t, "myvpc", vars["vpc_name"].AsString())
}
```

Create test fixture:
```
pkg/hcl/testdata/root_with_auto_tfvars/terraform.tfvars    → env = "prod"
pkg/hcl/testdata/root_with_auto_tfvars/vpc.auto.tfvars     → vpc_name = "myvpc"
pkg/hcl/testdata/root_with_auto_tfvars/db.auto.tfvars      → db_name = "mydb"
```

- [ ] **Step 2: Implement**

```go
// In pkg/hcl/parser.go

// LoadAllTfvars loads terraform.tfvars and all *.auto.tfvars files from a directory.
// Variables from later files override earlier ones. terraform.tfvars is loaded first,
// then *.auto.tfvars in alphabetical order (matching Terraform's behavior).
func LoadAllTfvars(dir string) (map[string]cty.Value, error) {
	result := map[string]cty.Value{}

	// Load terraform.tfvars first
	tfvars, err := LoadTfvars(filepath.Join(dir, "terraform.tfvars"))
	if err != nil {
		return nil, err
	}
	maps.Copy(result, tfvars)

	// Load *.auto.tfvars in alphabetical order
	autoFiles, _ := filepath.Glob(filepath.Join(dir, "*.auto.tfvars"))
	sort.Strings(autoFiles)
	for _, f := range autoFiles {
		vars, err := LoadTfvars(f)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", f, err)
		}
		maps.Copy(result, vars)
	}

	return result, nil
}
```

- [ ] **Step 3: Update `populateComponentsFromHCL` to use `LoadAllTfvars`**

Replace:
```go
tfvarsPath := filepath.Join(tfSourceDir, "terraform.tfvars")
tfvars, err := hclpkg.LoadTfvars(tfvarsPath)
```

With:
```go
tfvars, err := hclpkg.LoadAllTfvars(tfSourceDir)
```

- [ ] **Step 4: Run tests, commit**

```bash
git add pkg/hcl/parser.go pkg/hcl/parser_test.go pkg/component_populate.go
git commit -m "feat: load .auto.tfvars files alongside terraform.tfvars"
```

---

## Task 5: Parse and evaluate `locals` blocks

Terraform `locals` blocks define computed values. They can reference `var.*`, other locals, and data sources. In the DNS-to-DB project:

```hcl
locals {
  owners      = var.business_divsion
  environment = var.environment
  name        = "${var.business_divsion}-${var.environment}"
  common_tags = { owners = local.owners, environment = local.environment }
}
```

These need to be parsed, evaluated in dependency order, and added to the eval context as `local.*`.

**Files:** Modify `pkg/hcl/parser.go`, `pkg/hcl/parser_test.go`, `pkg/component_populate.go`

- [ ] **Step 1: Write failing test**

```go
// In pkg/hcl/parser_test.go
func TestParseLocals(t *testing.T) {
	locals, err := ParseLocals("testdata/root_with_locals")
	require.NoError(t, err)
	require.Len(t, locals, 2) // name, env
}
```

Create fixture:
```hcl
# pkg/hcl/testdata/root_with_locals/main.tf
variable "env" { default = "dev" }
locals {
  name = "myproject-${var.env}"
  upper_name = upper(local.name)
}
module "pet" {
  source = "../pet_module"
  prefix = local.name
}
```

- [ ] **Step 2: Implement `ParseLocals`**

```go
// LocalDefinition represents a single local value declaration.
type LocalDefinition struct {
	Name       string
	Expression hcl.Expression
}

// ParseLocals parses all locals blocks from .tf files in a directory.
func ParseLocals(dir string) ([]LocalDefinition, error) {
	files, err := parseTFFiles(dir)
	if err != nil {
		return nil, err
	}
	var locals []LocalDefinition
	for _, f := range files {
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, block := range body.Blocks {
			if block.Type != "locals" {
				continue
			}
			attrs, _ := block.Body.JustAttributes()
			for name, attr := range attrs {
				locals = append(locals, LocalDefinition{Name: name, Expression: attr.Expr})
			}
		}
	}
	return locals, nil
}
```

- [ ] **Step 3: Evaluate locals in dependency order**

In `pkg/component_populate.go`, add a function that evaluates locals iteratively (multiple passes until stable, since locals can reference other locals):

```go
// evaluateLocals evaluates local definitions against an eval context.
// Uses iterative evaluation since locals can reference other locals.
// Returns a map of local name -> cty.Value.
func evaluateLocals(locals []hclpkg.LocalDefinition, evalCtx *hclpkg.EvalContext) map[string]cty.Value {
	resolved := map[string]cty.Value{}
	remaining := locals

	// Iterate until no more progress (max 10 passes to prevent infinite loops)
	for pass := 0; pass < 10 && len(remaining) > 0; pass++ {
		evalCtx.AddVariables(map[string]cty.Value{"local": cty.ObjectVal(resolved)})
		var unresolved []hclpkg.LocalDefinition
		for _, l := range remaining {
			val, err := evalCtx.EvaluateExpression(l.Expression)
			if err == nil {
				resolved[l.Name] = val
			} else {
				unresolved = append(unresolved, l)
			}
		}
		if len(unresolved) == len(remaining) {
			break // no progress, remaining locals have unresolvable dependencies
		}
		remaining = unresolved
	}

	return resolved
}
```

Wire into `populateComponentsFromHCL` after building the initial eval context:

```go
// Parse and evaluate locals
locals, _ := hclpkg.ParseLocals(tfSourceDir)
if len(locals) > 0 {
	localValues := evaluateLocals(locals, evalCtx)
	if len(localValues) > 0 {
		evalCtx.AddVariables(map[string]cty.Value{"local": cty.ObjectVal(localValues)})
	}
}
```

- [ ] **Step 4: Run tests, commit**

```bash
git add pkg/hcl/parser.go pkg/hcl/parser_test.go pkg/component_populate.go pkg/component_populate_test.go
git commit -m "feat: parse and evaluate locals blocks for HCL eval context"
```

---

## Task 6: Wire module cross-references into eval context

Module call sites often reference other modules' outputs (e.g., `module.vpc.vpc_id`). The eval context needs a `module.*` namespace populated with resolved module outputs.

The challenge: module outputs depend on child resource attributes from state. We need to evaluate modules in dependency order — modules whose outputs are referenced by other modules must be processed first.

**Files:** Modify `pkg/component_populate.go`, `pkg/component_populate_test.go`

- [ ] **Step 1: Implement two-pass approach**

First pass: evaluate all module outputs (from state resource attrs). Second pass: build `module.*` namespace from those outputs and re-evaluate module inputs.

```go
// In populateComponentsFromHCL, after the component loop:
//
// Build module output namespace from evaluated component outputs.
// moduleOutputs maps module name -> output name -> value.
moduleOutputs := map[string]map[string]cty.Value{}
for _, comp := range components {
	node := findComponentNode(componentTree, comp.Name)
	if node == nil || comp.Outputs == nil {
		continue
	}
	outs := hclpkg.PulumiPropertyMapToCtyMap(comp.Outputs)
	if len(outs) > 0 {
		moduleOutputs[node.name] = outs
	}
}

// Re-evaluate inputs with module cross-refs now available
if len(moduleOutputs) > 0 {
	// Second pass with module.* namespace populated
	for i, comp := range components {
		// ... rebuild eval context with moduleOutputs added ...
		// evalCtx := hclpkg.NewEvalContext(evalVars, resourceAttrs, moduleOutputs)
		// re-evaluate call site arguments
	}
}
```

- [ ] **Step 2: Write test**

```go
func TestPopulateComponentsFromHCL_ModuleCrossRef(t *testing.T) {
	// Fixture: module "a" outputs vpc_id, module "b" inputs vpc_id = module.a.vpc_id
	// Verify module.b's input resolves to module.a's output value
}
```

Create fixture:
```hcl
# pkg/hcl/testdata/root_with_cross_ref/main.tf
module "base" {
  source = "./modules/base"
}
module "consumer" {
  source = "./modules/consumer"
  vpc_id = module.base.vpc_id
}

# modules/base/main.tf
resource "random_string" "id" { length = 8; special = false }
output "vpc_id" { value = random_string.id.result }

# modules/consumer/main.tf
variable "vpc_id" { type = string }
resource "random_pet" "this" { prefix = var.vpc_id }
output "name" { value = random_pet.this.id }
```

Deploy this fixture to capture state, then test.

- [ ] **Step 3: Run tests, commit**

```bash
git add pkg/component_populate.go pkg/component_populate_test.go pkg/hcl/testdata/root_with_cross_ref/
git commit -m "feat: wire module cross-references into eval context"
```

---

## Task 7: Integration test against DNS-to-DB with `tofu init`

Ultimate validation: run the tool against the DNS-to-DB state with `tofu init`'d module cache, and verify significantly more inputs/outputs are populated.

**Files:** Modify `pkg/e2e_test.go`

- [ ] **Step 1: Add test**

```go
func TestConvertDnsToDb_WithHCL(t *testing.T) {
	// This test requires tofu init to have been run on the DNS-to-DB fixture.
	// The .terraform/modules/ cache must exist for remote module resolution.
	// Skip if cache doesn't exist (CI may not have it).
	if _, err := os.Stat("testdata/tf_dns_to_db/.terraform/modules/modules.json"); os.IsNotExist(err) {
		t.Skip("skipping: requires tofu init on tf_dns_to_db fixture")
	}

	ctx := context.Background()
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: "testdata/tofu_state_dns_to_db.json",
	})
	require.NoError(t, err)

	data, err := TranslateState(ctx, tfState, nil, "dev", "test-project", true, true, nil, nil, nil, "testdata/tf_dns_to_db")
	require.NoError(t, err)

	// With module cache, ALL components should have populated inputs
	for _, r := range data.Export.Deployment.Resources {
		if !r.Custom && string(r.Type) != "pulumi:pulumi:Stack" {
			name := string(r.URN)[strings.LastIndex(string(r.URN), "::")+2:]
			require.NotEmpty(t, r.Inputs, "component %s should have inputs with module cache", name)
		}
	}
}
```

- [ ] **Step 2: Run `tofu init` on the DNS-to-DB fixture to create module cache**

```bash
cd pkg/testdata/tf_dns_to_db && tofu init -input=false -backend=false
```

Note: `-backend=false` skips backend config since we don't have the actual backend.

- [ ] **Step 3: Commit the modules.json manifest (not the full cache)**

The module cache is too large to commit. Instead, commit just `modules.json` and add `.terraform/modules/` contents to `.gitignore`. The test skips if the cache isn't present (CI setup can run `tofu init`).

- [ ] **Step 4: Run test, commit**

```bash
git add pkg/e2e_test.go
git commit -m "test: add DNS-to-DB integration test with module cache resolution"
```

---

## Verification

After all tasks:

```bash
# Run the tool against DNS-to-DB with module cache
/tmp/pulumi-terraform-migrate stack \
  --from /path/to/tf_stack_dns_to_db/terraform-manifests \
  --to /tmp --out /tmp/state.json --plugins /tmp/plugins.json \
  --pulumi-stack dev --pulumi-project test

# Count warnings (should be significantly fewer)
# All components should have populated inputs and outputs
```

Expected: warnings for `module.*` cross-refs that can't be resolved (circular dependencies) and any remaining edge cases, but the majority of inputs/outputs should now be populated.
