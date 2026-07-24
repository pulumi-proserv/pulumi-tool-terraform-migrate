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

package cfn

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeCC struct {
	attrs map[string]map[string]interface{}
}

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

func TestResolveProperties_UnresolvedIntrinsicSentinel(t *testing.T) {
	t.Parallel()
	props := map[string]interface{}{
		"Sub":    map[string]interface{}{"Fn::Sub": "${AWS::StackName}-thing"},
		"Select": map[string]interface{}{"Fn::Select": []interface{}{0, []interface{}{"a", "b"}}},
	}
	got, err := ResolveProperties(context.Background(), props, nil, nil, nil, fakeCC{})
	require.NoError(t, err)
	require.Equal(t, "<unresolved-intrinsic:Fn::Sub>", got["Sub"])
	require.Equal(t, "<unresolved-intrinsic:Fn::Select>", got["Select"])
}

func TestResolveProperties_NestedPropertyObjectPassthrough(t *testing.T) {
	t.Parallel()
	// A multi-key map is a legitimate nested property object, not an intrinsic,
	// and must pass through unchanged.
	props := map[string]interface{}{
		"Tag": map[string]interface{}{"Key": "Name", "Value": "prod"},
	}
	got, err := ResolveProperties(context.Background(), props, nil, nil, nil, fakeCC{})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"Key": "Name", "Value": "prod"}, got["Tag"])
}

func TestResolveProperties_DeepNestedResolution(t *testing.T) {
	t.Parallel()
	// Intrinsics nested inside arrays/objects (e.g. an IAM policy document or an
	// environment-variable map) must be resolved, not passed through as raw maps.
	resources := map[string]string{"Bucket": "my-bucket", "Fn2": "fn-2"}
	props := map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": []interface{}{
				map[string]interface{}{
					"Effect":   "Allow",
					"Resource": map[string]interface{}{"Ref": "Bucket"}, // nested Ref, inside object, inside array
				},
			},
		},
		"Environment": map[string]interface{}{
			"Variables": map[string]interface{}{
				"FN": map[string]interface{}{"Ref": "Fn2"}, // nested Ref inside object
			},
		},
	}
	got, err := ResolveProperties(context.Background(), props, resources, nil, nil, fakeCC{})
	require.NoError(t, err)

	pd := got["PolicyDocument"].(map[string]interface{})
	stmt := pd["Statement"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "my-bucket", stmt["Resource"])
	require.Equal(t, "Allow", stmt["Effect"])

	env := got["Environment"].(map[string]interface{})["Variables"].(map[string]interface{})
	require.Equal(t, "fn-2", env["FN"])
}

func TestResolveProperties_DeepNestedUnresolvedIntrinsic(t *testing.T) {
	t.Parallel()
	// An unresolved intrinsic nested at depth is surfaced as the sentinel, so it
	// can never silently pass through as a raw map.
	props := map[string]interface{}{
		"Environment": map[string]interface{}{
			"Variables": map[string]interface{}{
				"URL": map[string]interface{}{"Fn::Sub": "https://${AWS::Region}.example.com"},
			},
		},
	}
	got, err := ResolveProperties(context.Background(), props, nil, nil, nil, fakeCC{})
	require.NoError(t, err)
	env := got["Environment"].(map[string]interface{})["Variables"].(map[string]interface{})
	require.Equal(t, "<unresolved-intrinsic:Fn::Sub>", env["URL"])
}

// panicCC fails the test if GetResource is ever called — used to prove a nested
// GetAtt does NOT trigger a Cloud Control call.
type panicCC struct{ t *testing.T }

func (p panicCC) GetResource(_ context.Context, _, _ string) (map[string]interface{}, error) {
	p.t.Fatal("Cloud Control GetResource should not be called for a nested Fn::GetAtt")
	return nil, nil
}

func TestResolveProperties_NestedGetAttNotResolvedViaCloudControl(t *testing.T) {
	t.Parallel()
	// A GetAtt nested inside a policy document must become a marker without any
	// Cloud Control call (top-level GetAtt is resolved; nested is not — to avoid
	// one AWS call per occurrence).
	props := map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": []interface{}{
				map[string]interface{}{
					"Resource": map[string]interface{}{"Fn::GetAtt": []interface{}{"Bucket", "Arn"}},
				},
			},
		},
	}
	got, err := ResolveProperties(context.Background(), props,
		map[string]string{"Bucket": "my-bucket"}, map[string]string{"Bucket": "AWS::S3::Bucket"}, nil,
		panicCC{t: t})
	require.NoError(t, err)
	stmt := got["PolicyDocument"].(map[string]interface{})["Statement"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "<unresolved-intrinsic:Fn::GetAtt>", stmt["Resource"])
}
