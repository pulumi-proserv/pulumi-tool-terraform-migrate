// pkg/cfn/names_test.go
package cfn

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, logicalID, physicalID, cfnType string
		wantHashed, wantServer               bool
	}{
		{"cdk hashed policy", "TaskRoleDefaultPolicyDFEB0894", "FFSStackTaskRoleDefaultPolicyDFEB0894", "AWS::IAM::Policy", true, false},
		{"cfn random role", "TaskRole30FC0FBB", "FFSStack-TaskRole30FC0FBB-xQMUV6Ikl78Y", "AWS::IAM::Role", false, true},
		{"plain name", "ApiHandler", "ffs-dev-api", "AWS::Lambda::Function", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, hashed, server := ClassifyName(tc.logicalID, tc.physicalID, tc.cfnType)
			require.Equal(t, tc.wantHashed, hashed)
			require.Equal(t, tc.wantServer, server)
		})
	}
}
