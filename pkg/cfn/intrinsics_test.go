// pkg/cfn/intrinsics_test.go
package cfn

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeCC struct{ attrs map[string]map[string]interface{} }

func (f fakeCC) GetResource(_ context.Context, typeName, id string) (map[string]interface{}, error) {
	return f.attrs[typeName+"/"+id], nil
}

func TestResolveProperties(t *testing.T) {
	t.Parallel()
	resources := map[string]string{"RestApi": "abc123", "Db": "db-1"}
	resourceTypes := map[string]string{"Db": "AWS::RDS::DBInstance"}
	exports := map[string]string{"shared-vpc": "vpc-999"}
	cc := fakeCC{attrs: map[string]map[string]interface{}{
		"AWS::RDS::DBInstance/db-1": {"Endpoint.Address": "db.example.com"},
	}}
	props := map[string]interface{}{
		"RestApiId": map[string]interface{}{"Ref": "RestApi"},
		"Vpc":       map[string]interface{}{"Fn::ImportValue": "shared-vpc"},
		"DbHost": map[string]interface{}{
			"Fn::GetAtt": []interface{}{"Db", "Endpoint.Address"},
		},
		"Literal": "unchanged",
	}
	got, err := ResolveProperties(context.Background(), props, resources, resourceTypes, exports, cc)
	require.NoError(t, err)
	require.Equal(t, "abc123", got["RestApiId"])
	require.Equal(t, "vpc-999", got["Vpc"])
	require.Equal(t, "db.example.com", got["DbHost"])
	require.Equal(t, "unchanged", got["Literal"])
}
