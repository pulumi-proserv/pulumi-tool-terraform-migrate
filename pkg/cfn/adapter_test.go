package cfn

import (
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/importid"
	"github.com/stretchr/testify/require"
)

func TestCfnGetter(t *testing.T) {
	t.Parallel()
	get := CfnGetter(map[string]interface{}{
		"FunctionName": "ffs-dev-api", "Id": "AllowS3", "RestApiId": "abc",
	})
	require.Equal(t, "ffs-dev-api", get(importid.RoleFunction))
	require.Equal(t, "AllowS3", get(importid.RoleStatement)) // CFN "Id" -> statement role
	require.Equal(t, "abc", get(importid.RoleRestApi))
}

func TestCfnGetter_RoleNameOverload(t *testing.T) {
	t.Parallel()

	// PolicyName present alongside Name: explicit override wins.
	getWithPolicyName := CfnGetter(map[string]interface{}{
		"PolicyName": "my-scaling-policy", "Name": "my-name",
	})
	require.Equal(t, "my-scaling-policy", getWithPolicyName(importid.RoleName))

	// PolicyName absent: falls back to the CFN "Name" property.
	getWithoutPolicyName := CfnGetter(map[string]interface{}{
		"Name": "my-name",
	})
	require.Equal(t, "my-name", getWithoutPolicyName(importid.RoleName))
}
