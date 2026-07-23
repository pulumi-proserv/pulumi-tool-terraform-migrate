package cfn

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackDigest_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	d := StackDigest{StackName: "ffs-dev", Region: "us-east-1", Resources: []CfnResource{{
		LogicalID: "MigrateFn", CfnType: "AWS::Lambda::Function", PhysicalID: "ffs-dev-migrate",
		PulumiType: "aws:lambda/function:Function",
		Attributes: map[string]interface{}{"FunctionName": "ffs-dev-migrate"},
	}}}
	data, err := json.Marshal(d)
	require.NoError(t, err)
	var got StackDigest
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, d, got)
}
