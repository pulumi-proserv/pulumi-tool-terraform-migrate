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
	// Composite raw roles for custom composers:
	importid.RoleScalingTargetID: "ScalingTargetId",
	importid.RoleEcsID:           "Id",
	importid.RoleTransferID:      "Id",
	importid.RoleLayerArn:        "LayerVersionArn",

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
