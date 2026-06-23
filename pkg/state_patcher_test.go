// Copyright 2016-2025, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/tfbridge/info"
	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/providermap"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTFName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, expected string
	}{
		{"this[\"/clf-DEV/cf_rds_credentials\"]", "/clf-DEV/cf_rds_credentials"},
		{"bucket[\"my-bucket\"]", "my-bucket"},
		{"public[0]", "0"},
		{"plain_name", "plain_name"},
		{"this", "this"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expected, normalizeTFName(tc.input), "input: %s", tc.input)
	}
}

func TestShortPulumiType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, expected string
	}{
		{"aws:secretsmanager/secret:Secret", "secret:Secret"},
		{"aws:s3/bucket:Bucket", "bucket:Bucket"},
		{"aws:rds/cluster:Cluster", "cluster:Cluster"},
		{"pulumi:pulumi:Stack", "pulumi:Stack"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.expected, shortPulumiType(tc.input), "input: %s", tc.input)
	}
}

// buildTestProviders creates the providers map and pulumiToTFType map needed by PatchState
// from a single test provider. tokenOverrides maps TF type -> Pulumi type token to set
// explicit Tok values on resources (needed because the generic bridge naming differs from
// real providers, e.g. "aws:s3Bucket:S3Bucket" vs "aws:s3/bucket:Bucket").
func buildTestProviders(t *testing.T, prov *ProviderWithMetadata, tokenOverrides map[string]string) (
	map[providermap.TerraformProviderName]*ProviderWithMetadata,
	map[string]string,
) {
	t.Helper()
	for tfType, tok := range tokenOverrides {
		if res, ok := prov.Resources[tfType]; ok {
			res.Tok = tokens.Type(tok)
		}
	}
	providers := map[providermap.TerraformProviderName]*ProviderWithMetadata{
		providermap.TerraformProviderName(prov.TerraformAddress): prov,
	}
	typeMap := BuildPulumiToTFTypeMap(providers)
	return providers, typeMap
}

func TestPatchState_PatchesFromDigest(t *testing.T) {
	t.Parallel()

	// Build a test provider with force_destroy (optional, default false).
	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"force_destroy": {optional: true, hasDefault: true, default_: false},
		"bucket":        {optional: true},
		"arn":           {computed: true}, // output-only, should be skipped
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State: a bucket with nil force_destroy.
	stateData := buildTestState("aws:s3/bucket:Bucket", "my-bucket", map[string]any{
		"bucket": "my-bucket",
	})

	// Digest: the bucket has force_destroy = true.
	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
				TerraformAddress: "aws_s3_bucket.my_bucket",
				Attributes: map[string]interface{}{
					"force_destroy": true,
					"bucket":        "my-bucket",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_bucket.my_bucket": "my-bucket",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest) // force_destroy=true from digest

	// Verify the patched value.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, true, inputs["forceDestroy"]) // from digest
}

func TestPatchState_DefaultFallback(t *testing.T) {
	t.Parallel()

	// Build a test provider where force_destroy has a default of false.
	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"force_destroy": {optional: true, hasDefault: true, default_: false},
		"bucket":        {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State: bucket with nil forceDestroy, no digest match.
	stateData := buildTestState("aws:s3/bucket:Bucket", "orphan-bucket", map[string]any{
		"bucket": "orphan-bucket",
	})

	// Empty digest -- no match possible.
	digest := &ModuleMap{}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 0, result.FieldsFromDigest)
	assert.Equal(t, 1, result.FieldsFromDefaults) // force_destroy=false from schema default

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, false, inputs["forceDestroy"])
}

func TestPatchState_AssetFromSchema(t *testing.T) {
	t.Parallel()

	// Create a temp file to act as the asset source.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "swagger-ui", "index.html")
	require.NoError(t, os.MkdirAll(filepath.Dir(testFile), 0o755))
	require.NoError(t, os.WriteFile(testFile, []byte("<html>hello</html>"), 0o644))

	// Compute expected hash.
	h := sha256.New()
	h.Write([]byte("<html>hello</html>"))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	// Build provider with an asset field (source as FileAsset).
	prov := buildTestProvider(t, "aws_s3_object", map[string]testFieldDef{
		"source": {
			optional: true,
			asset:    &info.AssetTranslation{Kind: info.FileAsset},
		},
		"bucket": {required: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_object": "aws:s3/object:Object",
	})

	// State: object with source as a plain string (from import).
	stateData := buildTestState("aws:s3/object:Object", "my-obj", map[string]any{
		"source": "swagger-ui/index.html",
		"bucket": "my-bucket",
	})

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/object:Object::my-obj",
				TerraformAddress: "aws_s3_object.my_obj",
				Attributes: map[string]interface{}{
					"source": "swagger-ui/index.html",
					"bucket": "my-bucket",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_object.my_obj": "my-obj",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, tmpDir)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)

	// Verify the input was patched to an asset sentinel.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	sentinel, ok := inputs["source"].(map[string]interface{})
	require.True(t, ok, "source should be an asset sentinel map")
	assert.Equal(t, assetSig, sentinel[sigKey])
	assert.Equal(t, expectedHash, sentinel["hash"])
	assert.Equal(t, testFile, sentinel["path"])
}

func TestPatchState_SensitiveResolution(t *testing.T) {
	t.Parallel()

	// Build provider with master_password field (optional, no default).
	prov := buildTestProvider(t, "aws_rds_cluster", map[string]testFieldDef{
		"master_password": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_rds_cluster": "aws:rds/cluster:Cluster",
	})

	// State: cluster with empty inputs/outputs.
	stateData := buildTestState("aws:rds/cluster:Cluster", "my-cluster", map[string]any{})

	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
				TerraformAddress: "aws_rds_cluster.my_cluster",
				Attributes: map[string]interface{}{
					"master_password": "(sensitive)",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster.my_cluster": "my-cluster",
	}

	// flattenAddress("aws_rds_cluster.my_cluster", "master_password") = "aws_rds_cluster_my_cluster_master_password"
	configSecrets := map[string]string{
		"aws_rds_cluster_my_cluster_master_password": "super-secret-pw",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, configSecrets, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest)  // resolved from config
	assert.Equal(t, 0, result.SkippedSensitive)   // none skipped

	// Verify the patched value is wrapped in the secret sentinel.
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	sentinel, ok := inputs["masterPassword"].(map[string]interface{})
	require.True(t, ok, "masterPassword should be a secret sentinel map")
	assert.Equal(t, "1b47061264138c4ac30d75fd1eb44270", sentinel["4dabf18193072939515e22adb298388d"])
	assert.Equal(t, `"super-secret-pw"`, sentinel["plaintext"])

	// Verify output was also patched.
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})
	outSentinel, ok := outputs["masterPassword"].(map[string]interface{})
	require.True(t, ok, "output masterPassword should be a secret sentinel map")
	assert.Equal(t, `"super-secret-pw"`, outSentinel["plaintext"])
}

func TestPatchState_FieldPresentNoOverwrite(t *testing.T) {
	t.Parallel()

	// Build provider with force_destroy as optional.
	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"force_destroy": {optional: true},
		"bucket":        {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State has forceDestroy = false (already set).
	stateData := buildTestState("aws:s3/bucket:Bucket", "my-bucket", map[string]any{
		"bucket":       "my-bucket",
		"forceDestroy": false,
	})

	// Digest has force_destroy = true.
	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
				TerraformAddress: "aws_s3_bucket.my_bucket",
				Attributes: map[string]interface{}{
					"force_destroy": true,
					"bucket":        "my-bucket",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_bucket.my_bucket": "my-bucket",
	}

	patched, _, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	// Verify forceDestroy stays false (not overwritten by digest value true).
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, false, inputs["forceDestroy"], "existing value should not be overwritten")
}

func TestPatchState_ComputedOnlySkipped(t *testing.T) {
	t.Parallel()

	// Build provider with "arn" as computed-only (Computed && !Optional && !Required).
	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"arn":    {computed: true},
		"bucket": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State has arn = nil.
	stateData := buildTestState("aws:s3/bucket:Bucket", "my-bucket", map[string]any{
		"bucket": "my-bucket",
	})

	// Digest has arn = "some-arn".
	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::my-bucket",
				TerraformAddress: "aws_s3_bucket.my_bucket",
				Attributes: map[string]interface{}{
					"arn":    "arn:aws:s3:::my-bucket",
					"bucket": "my-bucket",
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_s3_bucket.my_bucket": "my-bucket",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)

	// arn is computed-only, so it should NOT be patched.
	assert.Equal(t, 0, result.FieldsFromDigest, "computed-only field should not contribute to FieldsFromDigest")

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})
	_, hasArn := inputs["arn"]
	assert.False(t, hasArn, "computed-only field arn should not be patched into inputs")
}

func TestPatchState_ProviderNotFound(t *testing.T) {
	t.Parallel()

	// Build a provider for aws_s3_bucket only.
	prov := buildTestProvider(t, "aws_s3_bucket", map[string]testFieldDef{
		"bucket": {optional: true},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_s3_bucket": "aws:s3/bucket:Bucket",
	})

	// State has a resource of type "custom:index:MyComponent" (no provider).
	stateData := buildTestState("custom:index:MyComponent", "my-component", map[string]any{
		"name": "my-component",
	})

	digest := &ModuleMap{}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.NoFields, "unknown type should increment NoFields")
	assert.Equal(t, 0, result.Patched, "unknown type should not be patched")

	// State should still be valid JSON (no crash).
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
}

func TestPatchState_CamelCaseNestedKeys(t *testing.T) {
	t.Parallel()

	// Build provider with "parameter" as optional field with Pulumi name "parameters".
	prov := buildTestProvider(t, "aws_rds_cluster_parameter_group", map[string]testFieldDef{
		"parameter": {optional: true, pulumiName: "parameters"},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_rds_cluster_parameter_group": "aws:rds/clusterParameterGroup:ClusterParameterGroup",
	})

	// State: empty parameters.
	stateData := buildTestState("aws:rds/clusterParameterGroup:ClusterParameterGroup", "rds-params", map[string]any{})

	// Digest has parameter with snake_case nested keys.
	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::rds-params",
				TerraformAddress: "aws_rds_cluster_parameter_group.rds_params",
				Attributes: map[string]interface{}{
					"parameter": []interface{}{
						map[string]interface{}{
							"apply_method": "immediate",
							"name":         "rds.force_ssl",
							"value":        "1",
						},
					},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster_parameter_group.rds_params": "rds-params",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.FieldsFromDigest)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	inputs := resources[0].(map[string]interface{})["inputs"].(map[string]interface{})

	// Verify nested keys are camelCased.
	params := inputs["parameters"].([]interface{})
	require.Len(t, params, 1)
	param := params[0].(map[string]interface{})
	assert.Equal(t, "immediate", param["applyMethod"], "apply_method should be camelCased to applyMethod")
	assert.Equal(t, "rds.force_ssl", param["name"], "name should stay as name (no underscore)")
	assert.Equal(t, "1", param["value"], "value should stay as value")
}

func TestPatchState_DeltaUpdatesOnArrayPatch(t *testing.T) {
	t.Parallel()

	// Build provider with "parameter" optional field with Pulumi name "parameters".
	prov := buildTestProvider(t, "aws_rds_cluster_parameter_group", map[string]testFieldDef{
		"parameter": {optional: true, pulumiName: "parameters"},
	})
	providers, typeMap := buildTestProviders(t, prov, map[string]string{
		"aws_rds_cluster_parameter_group": "aws:rds/clusterParameterGroup:ClusterParameterGroup",
	})

	// Build state with custom output structure including delta.
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::rds-params",
					"type":   "aws:rds/clusterParameterGroup:ClusterParameterGroup",
					"custom": true,
					"id":     "rds-params",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{
						"parameters": nil,
					},
					"outputs": map[string]interface{}{
						"parameters": []interface{}{}, // empty array
						"__pulumi_raw_state_delta": map[string]interface{}{
							"obj": map[string]interface{}{
								"ps": map[string]interface{}{
									"parameters": map[string]interface{}{
										"arr": map[string]interface{}{}, // empty — no element deltas
									},
								},
								"renamed": map[string]interface{}{
									"parameters": "parameter",
								},
							},
						},
					},
				},
			},
		},
	}
	stateData, marshalErr := json.Marshal(state)
	require.NoError(t, marshalErr)

	// Digest has parameter data.
	digest := &ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/clusterParameterGroup:ClusterParameterGroup::rds-params",
				TerraformAddress: "aws_rds_cluster_parameter_group.rds_params",
				Attributes: map[string]interface{}{
					"parameter": []interface{}{
						map[string]interface{}{
							"apply_method": "immediate",
							"name":         "rds.force_ssl",
							"value":        "1",
						},
					},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster_parameter_group.rds_params": "rds-params",
	}

	patched, result, err := PatchState(stateData, digest, providers, typeMap, nil, resourceMappings, nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	outputs := resources[0].(map[string]interface{})["outputs"].(map[string]interface{})

	// Verify parameters output was patched.
	params := outputs["parameters"].([]interface{})
	require.Len(t, params, 1)
	param := params[0].(map[string]interface{})
	assert.Equal(t, "immediate", param["applyMethod"])

	// Verify delta was updated with element deltas for the new objects.
	delta := outputs["__pulumi_raw_state_delta"].(map[string]interface{})
	ps := delta["obj"].(map[string]interface{})["ps"].(map[string]interface{})
	paramsDelta := ps["parameters"].(map[string]interface{})
	arrDelta := paramsDelta["arr"].(map[string]interface{})

	el, hasEl := arrDelta["el"]
	assert.True(t, hasEl, "arr delta should have 'el' with element deltas after patching")

	elMap := el.(map[string]interface{})
	elem0, has0 := elMap["0"]
	assert.True(t, has0, "element delta should have entry for index 0")

	elem0Map := elem0.(map[string]interface{})
	_, hasObj := elem0Map["obj"]
	assert.True(t, hasObj, "element 0 delta should have 'obj' marker")
}
