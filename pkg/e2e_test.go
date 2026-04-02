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
	"context"
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/tofu"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/stretchr/testify/require"
)

// --- DNS-to-DB Stack (Fixture 1) ---
// Real-world AWS stack with ~90 managed resources across 18 module instances,
// including for_each instances, nested submodules, and a data-source-only module.

func loadDnsToDbState(t *testing.T) *TranslateStateResult {
	t.Helper()
	ctx := context.Background()
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: "testdata/tofu_state_dns_to_db.json",
	})
	require.NoError(t, err)
	data, err := TranslateState(ctx, tfState, nil, "dev", "test-project", true, true, nil, nil, nil, "")
	require.NoError(t, err)
	return data
}

func classifyResources(t *testing.T, data *TranslateStateResult) (stack []apitype.ResourceV3, providers []apitype.ResourceV3, components []apitype.ResourceV3, custom []apitype.ResourceV3) {
	t.Helper()
	for _, r := range data.Export.Deployment.Resources {
		switch {
		case string(r.Type) == "pulumi:pulumi:Stack":
			stack = append(stack, r)
		case r.Custom && r.Provider != "":
			// provider resources have no provider ref, custom resources do
			custom = append(custom, r)
		case r.Custom && r.Provider == "":
			providers = append(providers, r)
		default:
			components = append(components, r)
		}
	}
	return
}

func TestConvertDnsToDb(t *testing.T) {
	t.Parallel()
	data := loadDnsToDbState(t)

	var components []apitype.ResourceV3
	var customResources []apitype.ResourceV3
	var rootResources []apitype.ResourceV3

	stackURN := ""
	for _, r := range data.Export.Deployment.Resources {
		if string(r.Type) == "pulumi:pulumi:Stack" {
			stackURN = string(r.URN)
			continue
		}
		if !r.Custom {
			components = append(components, r)
			continue
		}
		// Check if this is a provider resource (type starts with "pulumi:providers:")
		if isProvider(r) {
			continue
		}
		customResources = append(customResources, r)
		if string(r.Parent) == stackURN {
			rootResources = append(rootResources, r)
		}
	}

	// 18 component instances (19 modules minus db_instance which has only data sources)
	require.Len(t, components, 18, "expected 18 component resources")

	// ~90 managed resources
	require.GreaterOrEqual(t, len(customResources), 85, "expected ~90 managed resources")

	// Root resources (not in any module) should be parented to Stack
	require.GreaterOrEqual(t, len(rootResources), 5, "expected root resources parented to Stack")

	// Verify for_each instances share the same type token
	componentTypes := map[string][]string{} // type → names
	for _, c := range components {
		componentTypes[string(c.Type)] = append(componentTypes[string(c.Type)], string(c.URN))
	}
	// ec2_private_app1 has 2 instances → same type token
	app1Type := "terraform:module/ec2PrivateApp1:Ec2PrivateApp1"
	require.Len(t, componentTypes[app1Type], 2, "ec2_private_app1 should have 2 for_each instances")

	// Verify nested module (rdsdb submodules) produce $-delimited URN type chain
	var rdsdbChildren []apitype.ResourceV3
	for _, c := range components {
		urn := string(c.URN)
		if contains(urn, "module/rdsdb:Rdsdb$") {
			rdsdbChildren = append(rdsdbChildren, c)
		}
	}
	// db_option_group, db_parameter_group, db_subnet_group (not db_instance — data sources only)
	require.Len(t, rdsdbChildren, 3, "rdsdb should have 3 nested component children (db_instance skipped — data only)")
}

func TestConvertDnsToDb_TypeOverrides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: "testdata/tofu_state_dns_to_db.json",
	})
	require.NoError(t, err)

	typeOverrides := map[string]string{
		"module.vpc":              "myinfra:network:Vpc",
		"module.ec2_private_app1": "myinfra:compute:AppServer",
	}

	data, err := TranslateState(ctx, tfState, nil, "dev", "test-project", true, true, typeOverrides, nil, nil, "")
	require.NoError(t, err)

	var components []apitype.ResourceV3
	for _, r := range data.Export.Deployment.Resources {
		if !r.Custom && string(r.Type) != "pulumi:pulumi:Stack" {
			components = append(components, r)
		}
	}

	// vpc should have custom type
	vpcFound := false
	for _, c := range components {
		if string(c.Type) == "myinfra:network:Vpc" {
			vpcFound = true
		}
	}
	require.True(t, vpcFound, "vpc should have overridden type myinfra:network:Vpc")

	// Both ec2_private_app1 instances should have the custom type
	app1Count := 0
	for _, c := range components {
		if string(c.Type) == "myinfra:compute:AppServer" {
			app1Count++
		}
	}
	require.Equal(t, 2, app1Count, "both ec2_private_app1 for_each instances should have custom type")

	// Other modules should keep derived types
	sgFound := false
	for _, c := range components {
		if string(c.Type) == "terraform:module/publicBastionSg:PublicBastionSg" {
			sgFound = true
		}
	}
	require.True(t, sgFound, "public_bastion_sg should keep auto-derived type")
}

func TestConvertDnsToDb_FlatMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tfState, err := tofu.LoadTerraformState(ctx, tofu.LoadTerraformStateOptions{
		StateFilePath: "testdata/tofu_state_dns_to_db.json",
	})
	require.NoError(t, err)

	// enableComponents=false → flat mode
	data, err := TranslateState(ctx, tfState, nil, "dev", "test-project", false, true, nil, nil, nil, "")
	require.NoError(t, err)

	// No component resources in flat mode
	for _, r := range data.Export.Deployment.Resources {
		if !r.Custom && string(r.Type) != "pulumi:pulumi:Stack" {
			t.Fatalf("unexpected component resource in flat mode: %s", r.Type)
		}
	}

	// All managed resources should be parented to Stack
	stackURN := ""
	for _, r := range data.Export.Deployment.Resources {
		if string(r.Type) == "pulumi:pulumi:Stack" {
			stackURN = string(r.URN)
			break
		}
	}
	for _, r := range data.Export.Deployment.Resources {
		if !r.Custom || isProvider(r) || string(r.Type) == "pulumi:pulumi:Stack" {
			continue
		}
		require.Equal(t, stackURN, string(r.Parent), "resource %s should be parented to Stack in flat mode", r.URN)
	}
}

// --- Helper functions ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func isProvider(r apitype.ResourceV3) bool {
	const prefix = "pulumi:providers:"
	return len(r.Type) >= len(prefix) && string(r.Type)[:len(prefix)] == prefix
}
