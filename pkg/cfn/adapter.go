package cfn

import (
	"fmt"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/importid"
)

// cfnRoleNames maps a source-agnostic role to the CloudFormation property name
// that carries its value. This is the CFN half of the shared translate core.
var cfnRoleNames = map[importid.Role]string{
	importid.RoleFunction:   "FunctionName",
	importid.RoleStatement:  "Id",
	importid.RoleRestApi:    "RestApiId",
	importid.RoleID:         "Id",
	importid.RoleResource:   "ResourceId",
	importid.RoleHTTP:       "HttpMethod",
	importid.RoleUsagePlan:  "UsagePlanId",
	importid.RoleKey:        "KeyId",
	importid.RoleUserPool:   "UserPoolId",
	importid.RoleSubnet:     "SubnetId",
	importid.RoleRouteTbl:   "RouteTableId",
	importid.RoleServer:     "ServerId",
	importid.RoleUser:       "UserName",
	importid.RoleQualifier:  "Qualifier",
	importid.RoleListener:   "ListenerArn",
	importid.RoleCert:       "Certificates",
	importid.RoleBucket:     "Bucket",
	importid.RoleQueue:      "Queues",
	importid.RoleHostZone:   "HostedZoneId",
	importid.RoleName:       "Name",
	importid.RoleType:       "Type",
	importid.RoleSetID:      "SetIdentifier",
	importid.RoleStage:      "StageName",
	importid.RoleAuthorizer: "AuthorizerId",
	// Composite raw roles for custom composers:
	importid.RoleScalingTargetID: "ScalingTargetId",
	importid.RoleEcsID:           "Id",
	importid.RoleTransferID:      "Id",
	importid.RoleLayerArn:        "LayerVersionArn",

	importid.RoleArn:          "Arn",
	importid.RoleCidr:         "DestinationCidrBlock",
	importid.RoleDevice:       "Device",
	importid.RoleVolume:       "VolumeId",
	importid.RoleInstance:     "InstanceId",
	importid.RoleLogGroupName: "LogGroupName",
	importid.RoleAlarmName:    "AlarmName",
}

// CfnGetter returns a role lookup over resolved CFN attributes.
func CfnGetter(attrs map[string]interface{}) func(importid.Role) string {
	return func(r importid.Role) string {
		// PolicyName carries into RoleName for scaling policy; prefer explicit CFN prop.
		if r == importid.RoleName {
			if v, ok := attrs["PolicyName"]; ok {
				return fmt.Sprintf("%v", v)
			}
		}
		name, ok := cfnRoleNames[r]
		if !ok {
			return ""
		}
		if v, ok := attrs[name]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
}
