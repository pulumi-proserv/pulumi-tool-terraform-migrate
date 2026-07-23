// pkg/cfn/intrinsics.go
package cfn

import (
	"context"
	"fmt"
	"strings"
)

// CloudControlReader fetches a resource's current attributes via the AWS Cloud
// Control API — used to resolve Fn::GetAtt references not present in the template.
type CloudControlReader interface {
	GetResource(ctx context.Context, typeName, id string) (map[string]interface{}, error)
}

// ResolveProperties evaluates CloudFormation intrinsics in a resource's
// properties into concrete values.
//   - resources:     logical ID -> physical ID (from ListStackResources)
//   - resourceTypes: logical ID -> CFN type (for Fn::GetAtt Cloud Control reads)
//   - exports:       stack export name -> value (for Fn::ImportValue)
func ResolveProperties(
	ctx context.Context,
	props map[string]interface{},
	resources, resourceTypes, exports map[string]string,
	cc CloudControlReader,
) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(props))
	for k, v := range props {
		rv, err := resolveValue(ctx, v, resources, resourceTypes, exports, cc)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}

func resolveValue(ctx context.Context, v interface{}, resources, resourceTypes, exports map[string]string, cc CloudControlReader) (interface{}, error) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return v, nil // literal
	}
	if ref, ok := m["Ref"].(string); ok {
		if phys, ok := resources[ref]; ok {
			return phys, nil
		}
		return ref, nil // pseudo-param or param — left as-is
	}
	if imp, ok := m["Fn::ImportValue"].(string); ok {
		if val, ok := exports[imp]; ok {
			return val, nil
		}
		return "", fmt.Errorf("unresolved Fn::ImportValue %q", imp)
	}
	if ga, ok := m["Fn::GetAtt"].([]interface{}); ok && len(ga) == 2 {
		logical, _ := ga[0].(string)
		attr, _ := ga[1].(string)
		phys, ok := resources[logical]
		if !ok {
			return "", fmt.Errorf("Fn::GetAtt unknown resource %q", logical)
		}
		attrs, err := cc.GetResource(ctx, resourceTypes[logical], phys)
		if err != nil {
			return "", err
		}
		if val, ok := attrs[attr]; ok {
			return val, nil
		}
		return "", fmt.Errorf("Fn::GetAtt %q.%q not found", logical, attr)
	}
	if joinArgs, ok := m["Fn::Join"].([]interface{}); ok && len(joinArgs) == 2 {
		delim, _ := joinArgs[0].(string)
		list, _ := joinArgs[1].([]interface{})
		parts := make([]string, 0, len(list))
		for _, item := range list {
			rv, err := resolveValue(ctx, item, resources, resourceTypes, exports, cc)
			if err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("%v", rv))
		}
		return strings.Join(parts, delim), nil
	}
	if m, ok := v.(map[string]interface{}); ok && len(m) == 1 {
		for k := range m {
			if strings.HasPrefix(k, "Fn::") || k == "Ref" {
				return fmt.Sprintf("<unresolved-intrinsic:%s>", k), nil
			}
		}
	}
	return v, nil
}
