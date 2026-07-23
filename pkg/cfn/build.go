// pkg/cfn/build.go
package cfn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type StackResource struct{ LogicalID, PhysicalID, CfnType string }

type StackReader interface {
	GetTemplate(ctx context.Context, stackName string) (string, error)
	ListStackResources(ctx context.Context, stackName string) ([]StackResource, error)
	GetExports(ctx context.Context) (map[string]string, error)
}

var skipTypes = map[string]bool{
	"AWS::CloudFormation::CustomResource":      true,
	"AWS::CDK::Metadata":                       true,
	"AWS::CloudFormation::WaitCondition":       true,
	"AWS::CloudFormation::WaitConditionHandle": true,
}

func shouldSkip(t string) bool { return skipTypes[t] || strings.HasPrefix(t, "Custom::") }

func BuildDigest(ctx context.Context, stackName, region string, sr StackReader, cc CloudControlReader, lk Lookups) (*StackDigest, error) {
	tmplStr, err := sr.GetTemplate(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("get template: %w", err)
	}
	var tmpl struct {
		Resources map[string]struct {
			Type       string                 `json:"Type"`
			Properties map[string]interface{} `json:"Properties"`
		} `json:"Resources"`
	}
	if err := json.Unmarshal([]byte(tmplStr), &tmpl); err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	stackResources, err := sr.ListStackResources(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("list stack resources: %w", err)
	}
	resources, resourceTypes := map[string]string{}, map[string]string{}
	for _, r := range stackResources {
		resources[r.LogicalID] = r.PhysicalID
		resourceTypes[r.LogicalID] = r.CfnType
	}
	exports, err := sr.GetExports(ctx)
	if err != nil {
		return nil, fmt.Errorf("get exports: %w", err)
	}

	digest := &StackDigest{StackName: stackName, Region: region}
	for _, r := range stackResources {
		res := CfnResource{LogicalID: r.LogicalID, CfnType: r.CfnType, PhysicalID: r.PhysicalID, PulumiType: PulumiType(r.CfnType)}
		if shouldSkip(r.CfnType) {
			res.Skipped, res.SkipReason = true, "CFN-only/CDK resource"
			digest.Resources = append(digest.Resources, res)
			continue
		}
		res.DerivedName, res.CdkHashedName, res.ServerAssigned = ClassifyName(r.LogicalID, r.PhysicalID, r.CfnType)

		attrs := map[string]interface{}{"Id": r.PhysicalID}
		if t, ok := tmpl.Resources[r.LogicalID]; ok && t.Properties != nil {
			resolved, err := ResolveProperties(ctx, t.Properties, resources, resourceTypes, exports, cc)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", r.LogicalID, err)
			}
			for k, v := range resolved {
				attrs[k] = v
			}
		}
		res.Attributes = attrs

		if id, isLookup, err := LookupImportID(ctx, r.CfnType, attrs, lk); err != nil {
			return nil, fmt.Errorf("lookup %s: %w", r.LogicalID, err)
		} else if isLookup {
			res.ImportID = id // pre-resolved; resolve cfn uses it directly
		}
		digest.Resources = append(digest.Resources, res)
	}
	return digest, nil
}
