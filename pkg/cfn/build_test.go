// pkg/cfn/build_test.go
package cfn

import (
	"context"
	"os"
	"testing"

	"github.com/hexops/autogold/v2"
	"github.com/stretchr/testify/require"
)

type fakeStack struct {
	template  string
	resources []StackResource
}

func (f fakeStack) GetTemplate(_ context.Context, _ string) (string, error) { return f.template, nil }
func (f fakeStack) ListStackResources(_ context.Context, _ string) ([]StackResource, error) {
	return f.resources, nil
}
func (f fakeStack) GetExports(_ context.Context) (map[string]string, error) { return nil, nil }

func TestBuildDigest_Golden(t *testing.T) {
	t.Parallel()
	tmpl, err := os.ReadFile("testdata/ffs-min.template.json")
	require.NoError(t, err)
	sr := fakeStack{
		template: string(tmpl),
		resources: []StackResource{
			{LogicalID: "ApiPermission", PhysicalID: "ffs-dev-api-AllowApiGw", CfnType: "AWS::Lambda::Permission"},
			{LogicalID: "ApiHandler", PhysicalID: "ffs-dev-api", CfnType: "AWS::Lambda::Function"},
			{LogicalID: "MetaData", PhysicalID: "n/a", CfnType: "AWS::CDK::Metadata"},
		},
	}
	digest, err := BuildDigest(context.Background(), "ffs-dev", "us-east-1", sr, nil, nil)
	require.NoError(t, err)
	autogold.ExpectFile(t, digest)
}
