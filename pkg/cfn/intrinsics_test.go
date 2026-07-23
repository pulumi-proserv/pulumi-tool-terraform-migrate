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

func TestResolveProperties_JoinWithNestedRef(t *testing.T) {
	t.Parallel()
	resources := map[string]string{"RestApi": "abc123"}
	cc := fakeCC{}
	props := map[string]interface{}{
		"Arn": map[string]interface{}{
			"Fn::Join": []interface{}{
				":",
				[]interface{}{
					"arn",
					map[string]interface{}{"Ref": "RestApi"},
					"literal",
				},
			},
		},
	}
	got, err := ResolveProperties(context.Background(), props, resources, nil, nil, cc)
	require.NoError(t, err)
	// The nested Ref must be resolved to the physical id before joining.
	require.Equal(t, "arn:abc123:literal", got["Arn"])
}

func TestResolveProperties_UnresolvedImportValue(t *testing.T) {
	t.Parallel()
	props := map[string]interface{}{
		"Vpc": map[string]interface{}{"Fn::ImportValue": "missing-export"},
	}
	_, err := ResolveProperties(context.Background(), props, nil, nil, map[string]string{}, fakeCC{})
	require.Error(t, err)
}

func TestResolveProperties_GetAttUnknownResource(t *testing.T) {
	t.Parallel()
	props := map[string]interface{}{
		"Host": map[string]interface{}{
			"Fn::GetAtt": []interface{}{"NoSuchResource", "Endpoint.Address"},
		},
	}
	_, err := ResolveProperties(context.Background(), props, map[string]string{}, map[string]string{}, nil, fakeCC{})
	require.Error(t, err)
}

func TestResolveProperties_GetAttUnknownAttribute(t *testing.T) {
	t.Parallel()
	resources := map[string]string{"Db": "db-1"}
	resourceTypes := map[string]string{"Db": "AWS::RDS::DBInstance"}
	cc := fakeCC{attrs: map[string]map[string]interface{}{
		"AWS::RDS::DBInstance/db-1": {"Endpoint.Address": "db.example.com"},
	}}
	props := map[string]interface{}{
		"Host": map[string]interface{}{
			"Fn::GetAtt": []interface{}{"Db", "Endpoint.Port"},
		},
	}
	_, err := ResolveProperties(context.Background(), props, resources, resourceTypes, nil, cc)
	require.Error(t, err)
}

func TestResolveProperties_UnresolvedRefPassthrough(t *testing.T) {
	t.Parallel()
	props := map[string]interface{}{
		"Region": map[string]interface{}{"Ref": "AWS::Region"},
	}
	got, err := ResolveProperties(context.Background(), props, map[string]string{}, nil, nil, fakeCC{})
	require.NoError(t, err)
	require.Equal(t, "AWS::Region", got["Region"])
}
