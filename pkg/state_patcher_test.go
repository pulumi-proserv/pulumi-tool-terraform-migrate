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
	"encoding/json"
	"testing"

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

func TestPatchState_PatchesFromDigest(t *testing.T) {
	t.Parallel()

	// State: a secret with nil recoveryWindowInDays
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
					"inputs": map[string]interface{}{
						"name": "my-secret",
					},
					"outputs": map[string]interface{}{
						"name": "my-secret",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Digest: the secret has recovery_window_in_days = 0
	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my-secret",
				TerraformAddress: "aws_secretsmanager_secret.my_secret",
				ImportID:         "arn:aws:secretsmanager:us-east-1:123:secret:my-secret",
				Attributes: map[string]interface{}{
					"recovery_window_in_days": float64(0),
					"name":                   "my-secret",
				},
			},
		},
	}

	// Fields: secret:Secret has recoveryWindowInDays as not_read with default 30
	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays":      {Default: float64(30)},
					"forceOverwriteReplicaSecret": {Default: false},
				},
			},
		},
	}

	// Resource mapping: direct
	resourceMappings := map[string]string{
		"aws_secretsmanager_secret.my_secret": "my-secret",
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest) // recovery_window_in_days=0 from digest
	assert.Equal(t, 1, result.FieldsFromDefaults) // forceOverwriteReplicaSecret=false from default

	// Verify the patched value
	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, float64(0), inputs["recoveryWindowInDays"]) // from digest, not default 30
	assert.Equal(t, false, inputs["forceOverwriteReplicaSecret"]) // from default
}

func TestPatchState_ModuleLevelMatching(t *testing.T) {
	t.Parallel()

	// State: component child with nil recoveryWindowInDays
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				// Component
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::data:index:SecretsManager::my-secrets",
					"type":   "data:index:SecretsManager",
					"custom": false,
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
				},
				// Child secret
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::data:index:SecretsManager$aws:secretsmanager/secret:Secret::my-secrets-/my/creds",
					"type":   "aws:secretsmanager/secret:Secret",
					"custom": true,
					"id":     "arn:aws:secretsmanager:us-east-1:123:secret:my-creds",
					"parent": "urn:pulumi:dev::proj::data:index:SecretsManager::my-secrets",
					"inputs": map[string]interface{}{
						"name": "/my/creds",
					},
					"outputs": map[string]interface{}{
						"name": "/my/creds",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Digest: module with the secret, recovery_window=0
	digest := ModuleMap{
		Modules: map[string]*ModuleMapEntry{
			"my-secrets": {
				TerraformPath: "module.my_secrets",
				Resources: []ModuleResource{
					{
						Mode:             "managed",
						TranslatedURN:    "urn:pulumi:dev::proj::aws:secretsmanager/secret:Secret::my_secrets_this[\"/my/creds\"]",
						TerraformAddress: "module.my_secrets.aws_secretsmanager_secret.this[\"/my/creds\"]",
						ImportID:         "arn:aws:secretsmanager:us-east-1:123:secret:my-creds",
						Attributes: map[string]interface{}{
							"recovery_window_in_days": float64(0),
						},
					},
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"secret:Secret": {
				NotRead: map[string]FieldInfo{
					"recoveryWindowInDays": {Default: float64(30)},
				},
			},
		},
	}

	// Module mapping (no resource mapping — must use module-level matching)
	moduleMappings := map[string]string{
		"module.my_secrets": "my-secrets",
	}

	patched, result, err := PatchState(stateData, &digest, fields, moduleMappings, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 1, result.FieldsFromDigest) // 0 from digest, not default 30

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	child := resources[1].(map[string]interface{})
	inputs := child["inputs"].(map[string]interface{})
	assert.Equal(t, float64(0), inputs["recoveryWindowInDays"])
}

func TestPatchState_DefaultFallback(t *testing.T) {
	t.Parallel()

	// State: bucket with nil forceDestroy, no digest match
	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":    "urn:pulumi:dev::proj::aws:s3/bucket:Bucket::orphan-bucket",
					"type":   "aws:s3/bucket:Bucket",
					"custom": true,
					"id":     "orphan-bucket",
					"parent": "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs": map[string]interface{}{
						"bucket": "orphan-bucket",
					},
					"outputs": map[string]interface{}{
						"bucket": "orphan-bucket",
					},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	// Empty digest — no match
	digest := ModuleMap{}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"bucket:Bucket": {
				NotRead: map[string]FieldInfo{
					"forceDestroy": {Default: false},
				},
			},
		},
	}

	patched, result, err := PatchState(stateData, &digest, fields, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Patched)
	assert.Equal(t, 0, result.FieldsFromDigest)
	assert.Equal(t, 1, result.FieldsFromDefaults)

	var patchedState map[string]interface{}
	require.NoError(t, json.Unmarshal(patched, &patchedState))
	resources := patchedState["deployment"].(map[string]interface{})["resources"].([]interface{})
	r := resources[0].(map[string]interface{})
	inputs := r["inputs"].(map[string]interface{})
	assert.Equal(t, false, inputs["forceDestroy"])
}

func TestPatchState_SkipsSensitive(t *testing.T) {
	t.Parallel()

	state := map[string]interface{}{
		"version": 3,
		"deployment": map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"urn":     "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
					"type":    "aws:rds/cluster:Cluster",
					"custom":  true,
					"id":      "my-cluster",
					"parent":  "urn:pulumi:dev::proj::pulumi:pulumi:Stack::proj-dev",
					"inputs":  map[string]interface{}{},
					"outputs": map[string]interface{}{},
				},
			},
		},
	}
	stateData, _ := json.Marshal(state)

	digest := ModuleMap{
		RootResources: []ModuleResource{
			{
				Mode:             "managed",
				TranslatedURN:    "urn:pulumi:dev::proj::aws:rds/cluster:Cluster::my-cluster",
				TerraformAddress: "aws_rds_cluster.my_cluster",
				Attributes: map[string]interface{}{
					"master_password":  "(sensitive)",
					"apply_immediately": nil,
				},
			},
		},
	}

	fields := &FieldsFile{
		Fields: map[string]FieldCategory{
			"cluster:Cluster": {
				NotRead: map[string]FieldInfo{
					"masterPassword":   {Default: nil},
					"applyImmediately": {Default: false},
				},
			},
		},
	}

	resourceMappings := map[string]string{
		"aws_rds_cluster.my_cluster": "my-cluster",
	}

	_, result, err := PatchState(stateData, &digest, fields, nil, resourceMappings)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SkippedSensitive)       // masterPassword
	assert.Equal(t, 1, result.FieldsFromDefaults)      // applyImmediately=false
}
