// pkg/cfn/lookups.go
package cfn

import (
	"context"
	"fmt"
)

// Lookups performs live AWS calls needed to resolve import IDs for CFN
// resource types whose ID cannot be derived from template attributes alone.
type Lookups interface {
	SecurityGroupRuleID(ctx context.Context, egress bool, props map[string]interface{}) (string, error)
	EIPAllocationID(ctx context.Context, publicIP string) (string, error)
	InternetGatewayAttachment(ctx context.Context, igwID string) (string, error)
}

func str(attrs map[string]interface{}, key string) string {
	if v, ok := attrs[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// LookupImportID resolves the import ID for the CFN types whose ID needs a live
// AWS call. isLookupType is false for every other type (composed later from
// attributes via the shared spec core).
//
// Note: CFN "AWS::IAM::Policy" (an inline policy embedded in a role/user/group)
// is intentionally NOT auto-resolved here. It maps to
// aws:iam/rolePolicy:RolePolicy with a "role:policy-name" import ID that
// requires a role-scoped list-role-policies lookup, and a single inline
// policy can bind to multiple principals. This is a documented manual case
// rather than something we guess at.
func LookupImportID(ctx context.Context, cfnType string, attrs map[string]interface{}, lk Lookups) (string, bool, error) {
	switch cfnType {
	case "AWS::EC2::SecurityGroupIngress":
		id, err := lk.SecurityGroupRuleID(ctx, false, attrs)
		return id, true, err
	case "AWS::EC2::SecurityGroupEgress":
		id, err := lk.SecurityGroupRuleID(ctx, true, attrs)
		return id, true, err
	case "AWS::EC2::EIP":
		id, err := lk.EIPAllocationID(ctx, str(attrs, "PublicIp"))
		return id, true, err
	case "AWS::EC2::VPCGatewayAttachment":
		id, err := lk.InternetGatewayAttachment(ctx, str(attrs, "InternetGatewayId"))
		return id, true, err
	}
	return "", false, nil
}
