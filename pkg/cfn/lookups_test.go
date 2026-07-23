// pkg/cfn/lookups_test.go
package cfn

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeLookups struct{ policyARN, sgRule, eip, igw string }

func (f fakeLookups) IAMPolicyARN(context.Context, string) (string, error) { return f.policyARN, nil }
func (f fakeLookups) SecurityGroupRuleID(context.Context, bool, map[string]interface{}) (string, error) {
	return f.sgRule, nil
}
func (f fakeLookups) EIPAllocationID(context.Context, string) (string, error) { return f.eip, nil }
func (f fakeLookups) InternetGatewayAttachment(context.Context, string) (string, error) {
	return f.igw, nil
}

func TestLookupImportID(t *testing.T) {
	t.Parallel()
	lk := fakeLookups{policyARN: "arn:pol", sgRule: "sgr-1", eip: "eipalloc-1", igw: "igw-1:vpc-1"}
	ctx := context.Background()

	id, isLookup, err := LookupImportID(ctx, "AWS::IAM::Policy", map[string]interface{}{"Id": "p"}, lk)
	require.NoError(t, err)
	require.True(t, isLookup)
	require.Equal(t, "arn:pol", id)

	_, isLookup, err = LookupImportID(ctx, "AWS::Lambda::Function", nil, lk)
	require.NoError(t, err)
	require.False(t, isLookup)
}
