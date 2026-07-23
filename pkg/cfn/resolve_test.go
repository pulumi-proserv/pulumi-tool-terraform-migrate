// pkg/cfn/resolve_test.go
package cfn

import (
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg"
	"github.com/stretchr/testify/require"
)

func TestFillFromDigest(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "ApiPermission", CfnType: "AWS::Lambda::Permission",
			PulumiType: "aws:lambda/permission:Permission",
			Attributes: map[string]interface{}{"FunctionName": "ffs-dev-api", "Id": "AllowApiGw"}},
		{LogicalID: "TaskPolicy", CfnType: "AWS::IAM::Policy",
			PulumiType: "aws:iam/policy:Policy", ImportID: "arn:aws:iam::1:policy/p"},
		{LogicalID: "Dep", CfnType: "AWS::ApiGateway::Deployment",
			Attributes: map[string]interface{}{"RestApiId": "abc", "Id": "dep"}},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:lambda/permission:Permission", Name: "ffs-ApiPermission"},
		{Type: "aws:iam/policy:Policy", Name: "ffs-TaskPolicy"},
		{Type: "aws:apigateway/deployment:Deployment", Name: "ffs-Dep"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 3, res.Filled)
	require.Equal(t, "ffs-dev-api/AllowApiGw", importFile.Resources[0].ID) // composed
	require.Equal(t, "arn:aws:iam::1:policy/p", importFile.Resources[1].ID) // pre-resolved lookup
	require.Equal(t, "dep|abc", importFile.Resources[2].ID)                 // native reversed
}
