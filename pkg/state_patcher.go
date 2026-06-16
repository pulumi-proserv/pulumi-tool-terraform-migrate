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

package pkg

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// FieldsFile represents the aws-import-diff-fields.json structure.
type FieldsFile struct {
	Fields map[string]FieldCategory `json:"fields"`
}

// FieldCategory represents the categories for a resource type.
type FieldCategory struct {
	NotRead map[string]FieldInfo `json:"not_read,omitempty"`
}

// FieldInfo describes a single not_read field.
type FieldInfo struct {
	Default interface{} `json:"default"`
}

// PatchStateResult contains statistics from the patch operation.
type PatchStateResult struct {
	Patched          int
	FieldsFromDigest int
	FieldsFromDefaults int
	SkippedSensitive int
	NoMatch          int
	NoFields         int
	DigestMapped     int
}

// tfToPulumiField maps TF snake_case attribute names to Pulumi camelCase field names
// for known not_read fields.
var tfToPulumiField = map[string]string{
	"acl":                                "acl",
	"apply_immediately":                  "applyImmediately",
	"certificate_body":                   "certificateBody",
	"certificate_chain":                  "certificateChain",
	"code":                               "code",
	"confirmation_timeout_in_minutes":    "confirmationTimeoutInMinutes",
	"content":                            "content",
	"endpoint_auto_confirms":             "endpointAutoConfirms",
	"force_destroy":                      "forceDestroy",
	"force_overwrite_replica_secret":     "forceOverwriteReplicaSecret",
	"master_password":                    "masterPassword",
	"private_key":                        "privateKey",
	"publish":                            "publish",
	"recovery_window_in_days":            "recoveryWindowInDays",
	"revoke_rules_on_delete":             "revokeRulesOnDelete",
	"secret_string":                      "secretString",
	"skip_destroy":                       "skipDestroy",
	"source":                             "source",
	"wait_for_steady_state":              "waitForSteadyState",
}

// pulumiToTFField is the reverse of tfToPulumiField.
var pulumiToTFField = func() map[string]string {
	m := make(map[string]string, len(tfToPulumiField))
	for k, v := range tfToPulumiField {
		m[v] = k
	}
	return m
}()

// shortPulumiType extracts the short type from a full Pulumi type token.
// "aws:secretsmanager/secret:Secret" → "secret:Secret"
// "pulumi:pulumi:Stack" → "pulumi:Stack"
func shortPulumiType(fullType string) string {
	parts := strings.FieldsFunc(fullType, func(r rune) bool {
		return r == ':' || r == '/'
	})
	if len(parts) >= 3 {
		return parts[len(parts)-2] + ":" + parts[len(parts)-1]
	}
	return ""
}

// BuildDigestNameMap builds a mapping from Pulumi resource name → ModuleResource
// using the same matching logic as FillImportFile: resource-level mappings first,
// then module-level type+name matching with type-only fallback.
func BuildDigestNameMap(
	digest *ModuleMap,
	moduleMappings, resourceMappings map[string]string,
	stateResources []json.RawMessage,
	stateResourceNames map[string]stateResourceInfo,
) map[string]*ModuleResource {
	result := make(map[string]*ModuleResource)

	// Index all managed digest resources by TF address.
	tfByAddress := map[string]*ModuleResource{}
	for i := range digest.RootResources {
		r := &digest.RootResources[i]
		if r.Mode == "managed" {
			tfByAddress[r.TerraformAddress] = r
		}
	}
	collectAllResources(digest.Modules, tfByAddress)

	// Phase 1: Resource-level mappings (direct).
	for tfAddr, pulumiName := range resourceMappings {
		if r, ok := tfByAddress[tfAddr]; ok {
			result[pulumiName] = r
		}
	}

	// Phase 2: Module-level matching.
	tfByModule := map[string][]ModuleResource{}
	collectModuleResources(digest.Modules, tfByModule)

	for tfModulePath, componentName := range moduleMappings {
		tfResources, ok := tfByModule[tfModulePath]
		if !ok {
			continue
		}

		// Find state resources that are children of this component.
		var children []stateResourceInfo
		for _, info := range stateResourceNames {
			if info.parentName == componentName {
				children = append(children, info)
			}
		}

		// Index TF resources by [type, name] for matching.
		type typeNameKey struct{ pulumiType, tfName string }
		byTypeName := map[typeNameKey]*ModuleResource{}
		byType := map[string][]*ModuleResource{}

		for i := range tfResources {
			r := &tfResources[i]
			if r.Mode != "managed" {
				continue
			}
			pulumiType := extractTypeFromURN(r.TranslatedURN)
			if pulumiType == "" {
				continue
			}
			tfName := extractResourceName(r.TerraformAddress)
			byTypeName[typeNameKey{pulumiType, tfName}] = r
			byType[pulumiType] = append(byType[pulumiType], r)
		}

		used := map[string]bool{}
		for _, child := range children {
			if _, already := result[child.name]; already {
				continue
			}

			suffix := extractImportSuffix(child.name, componentName)

			// Try exact match by type + name.
			key := typeNameKey{child.resourceType, suffix}
			if r, ok := byTypeName[key]; ok && !used[r.TerraformAddress] {
				result[child.name] = r
				used[r.TerraformAddress] = true
				continue
			}

			// Try normalized match: strip "this[" wrapper and quotes from TF name,
			// since component children often have TF names like this["key"]
			// while Pulumi suffix is just "key".
			matched := false
			for tkKey, r := range byTypeName {
				if tkKey.pulumiType != child.resourceType || used[r.TerraformAddress] {
					continue
				}
				normalized := normalizeTFName(tkKey.tfName)
				if normalized == suffix {
					result[child.name] = r
					used[r.TerraformAddress] = true
					matched = true
					break
				}
			}
			if matched {
				continue
			}

			// Fallback: exactly one unused candidate of this type.
			var candidates []*ModuleResource
			for _, r := range byType[child.resourceType] {
				if !used[r.TerraformAddress] {
					candidates = append(candidates, r)
				}
			}
			if len(candidates) == 1 {
				result[child.name] = candidates[0]
				used[candidates[0].TerraformAddress] = true
			}
		}
	}

	// Phase 3: Root resources (parented to Stack).
	{
		type typeNameKey struct{ pulumiType, tfName string }
		byTypeName := map[typeNameKey]*ModuleResource{}
		byType := map[string][]*ModuleResource{}

		for i := range digest.RootResources {
			r := &digest.RootResources[i]
			if r.Mode != "managed" {
				continue
			}
			pulumiType := extractTypeFromURN(r.TranslatedURN)
			if pulumiType == "" {
				continue
			}
			tfName := extractResourceName(r.TerraformAddress)
			byTypeName[typeNameKey{pulumiType, tfName}] = r
			byType[pulumiType] = append(byType[pulumiType], r)
		}

		used := map[string]bool{}
		for _, info := range stateResourceNames {
			if !info.isRoot {
				continue
			}
			if _, already := result[info.name]; already {
				continue
			}

			key := typeNameKey{info.resourceType, info.name}
			if r, ok := byTypeName[key]; ok && !used[r.TerraformAddress] {
				result[info.name] = r
				used[r.TerraformAddress] = true
				continue
			}

			candidates := make([]*ModuleResource, 0)
			for _, r := range byType[info.resourceType] {
				if !used[r.TerraformAddress] {
					candidates = append(candidates, r)
				}
			}
			if len(candidates) == 1 {
				result[info.name] = candidates[0]
				used[candidates[0].TerraformAddress] = true
			}
		}
	}

	return result
}

// stateResourceInfo holds the minimal info needed from a state resource for matching.
type stateResourceInfo struct {
	name         string
	resourceType string
	parentName   string
	isRoot       bool // parented directly to Stack
}

// PatchState patches not_read fields from digest into imported state.
// configSecrets is an optional map of config key → decrypted value, used to
// resolve sensitive fields that the digest redacts as "(sensitive)". Keys are
// generated by flattenAddress(terraformAddress, tfAttribute).
func PatchState(
	stateData []byte,
	digest *ModuleMap,
	fieldsFile *FieldsFile,
	moduleMappings, resourceMappings map[string]string,
	configSecrets map[string]string,
) ([]byte, *PatchStateResult, error) {
	// Parse state using a decoder with UseNumber to preserve exact number
	// representations. Without this, large integers (like AWS account IDs)
	// become float64 and may re-serialize as scientific notation (e.g.,
	// "5399223e-54"), which Pulumi's state parser rejects.
	var state map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(stateData)))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("parsing state: %w", err)
	}

	deployment, ok := state["deployment"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing deployment")
	}

	resourcesRaw, ok := deployment["resources"].([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("state missing resources")
	}

	// Build not_read field sets and defaults, keyed by both full and short type.
	// The fields file uses full type keys (aws:secretsmanager/secret:Secret),
	// but we match state resources by short type (secret:Secret) for convenience.
	notReadByType := map[string]map[string]interface{}{} // type → {pulumiField → default}
	for fullType, cat := range fieldsFile.Fields {
		if len(cat.NotRead) > 0 {
			fields := make(map[string]interface{}, len(cat.NotRead))
			for pulumiField, info := range cat.NotRead {
				fields[pulumiField] = info.Default // may be nil
			}
			// Index by both full type and short type for lookup flexibility.
			notReadByType[fullType] = fields
			st := shortPulumiType(fullType)
			if st != "" {
				notReadByType[st] = fields
			}
		}
	}

	// Extract resource info from state for matching.
	stateInfos := make(map[string]stateResourceInfo)
	for _, raw := range resourcesRaw {
		rMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		custom, _ := rMap["custom"].(bool)
		if !custom {
			continue
		}
		urn, _ := rMap["urn"].(string)
		resType, _ := rMap["type"].(string)
		parent, _ := rMap["parent"].(string)

		name := urnName(urn)
		parentName := urnName(parent)
		isRoot := strings.Contains(parent, "pulumi:pulumi:Stack") || parent == ""

		stateInfos[name] = stateResourceInfo{
			name:         name,
			resourceType: resType,
			parentName:   parentName,
			isRoot:       isRoot,
		}
	}

	// Build digest name map.
	nameMap := BuildDigestNameMap(digest, moduleMappings, resourceMappings, nil, stateInfos)

	result := &PatchStateResult{DigestMapped: len(nameMap)}

	// Patch resources.
	for i, raw := range resourcesRaw {
		rMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		custom, _ := rMap["custom"].(bool)
		if !custom {
			continue
		}
		urn, _ := rMap["urn"].(string)
		resType, _ := rMap["type"].(string)
		name := urnName(urn)

		st := shortPulumiType(resType)
		notReadFields, hasFields := notReadByType[st]
		if !hasFields {
			result.NoFields++
			continue
		}

		digResource := nameMap[name]

		inputsRaw, _ := rMap["inputs"].(map[string]interface{})
		outputsRaw, _ := rMap["outputs"].(map[string]interface{})
		if inputsRaw == nil {
			inputsRaw = map[string]interface{}{}
		}
		if outputsRaw == nil {
			outputsRaw = map[string]interface{}{}
		}

		patched := false
		for pulumiField, defaultVal := range notReadFields {
			tfAttr := pulumiToTFField[pulumiField]

			// Get digest value if we have a match.
			var digVal interface{}
			if digResource != nil && tfAttr != "" {
				digVal = digResource.Attributes[tfAttr]
			}

			// Treat empty string the same as nil — TF stores "" for unset
			// string fields, but the bridge applies the schema default.
			inputVal := inputsRaw[pulumiField]
			inputEmpty := inputVal == nil || inputVal == ""
			outputVal := outputsRaw[pulumiField]
			outputEmpty := outputVal == nil || outputVal == ""

			// Also treat empty-string digest values as unset.
			digSensitive := digVal == "(sensitive)"
			digEmpty := digVal == nil || digVal == "" || digSensitive

			// For sensitive fields, try to resolve from stack config.
			// Wrap in the Pulumi secret sentinel with "plaintext" so that
			// `pulumi stack import` re-encrypts the value.
			if digSensitive && digResource != nil && tfAttr != "" && len(configSecrets) > 0 {
				configKey := flattenAddress(digResource.TerraformAddress, tfAttr)
				if secretVal, ok := configSecrets[configKey]; ok && secretVal != "" {
					// The plaintext value in the sentinel must be JSON-encoded
					// (e.g., a string "foo" becomes "\"foo\"").
					jsonEncoded, err := json.Marshal(secretVal)
					if err != nil {
						return nil, nil, fmt.Errorf("encoding secret value for %s: %w", configKey, err)
					}
					digVal = map[string]interface{}{
						"4dabf18193072939515e22adb298388d": "1b47061264138c4ac30d75fd1eb44270",
						"plaintext":                        string(jsonEncoded),
					}
					digEmpty = false
					digSensitive = false
				}
			}

			// If we resolved a secret sentinel from config, also replace
			// existing plain-string values (from a previous bad patch).
			isSentinel := isSecretSentinel(digVal)
			inputIsBadPlain := isSentinel && inputVal != nil && !isSecretSentinel(inputVal)

			// Patch inputs.
			if inputEmpty || inputIsBadPlain {
				if !digEmpty {
					inputsRaw[pulumiField] = digVal
					result.FieldsFromDigest++
					patched = true
				} else if digSensitive {
					result.SkippedSensitive++
				} else if defaultVal != nil {
					inputsRaw[pulumiField] = defaultVal
					result.FieldsFromDefaults++
					patched = true
				}
			}

			// Patch outputs. Also replace sentinels wrapping null (from cloud import
			// where Read returns nil for write-only fields).
			outputIsBadPlain := isSentinel && outputVal != nil && !isSecretSentinel(outputVal)
			outputIsNullSentinel := isSentinel && isNullSentinel(outputVal)
			if outputEmpty || outputIsBadPlain || outputIsNullSentinel {
				if !digEmpty {
					outputsRaw[pulumiField] = digVal
				} else if defaultVal != nil {
					outputsRaw[pulumiField] = defaultVal
				}
			}
		}

		if patched {
			result.Patched++
		} else if digResource == nil {
			result.NoMatch++
		}

		rMap["inputs"] = inputsRaw
		rMap["outputs"] = outputsRaw
		resourcesRaw[i] = rMap
	}

	deployment["resources"] = resourcesRaw
	state["deployment"] = deployment

	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling patched state: %w", err)
	}

	return out, result, nil
}

// isSecretSentinel checks if a value is a Pulumi secret sentinel map.
func isSecretSentinel(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	_, hasSig := m["4dabf18193072939515e22adb298388d"]
	return hasSig
}

// isNullSentinel checks if a value is a secret sentinel wrapping null/empty.
// This happens when cloud import creates a sentinel for a write-only field
// where the provider Read returns nil.
func isNullSentinel(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	if _, hasSig := m["4dabf18193072939515e22adb298388d"]; !hasSig {
		return false
	}
	// Check plaintext or ciphertext for null/empty values.
	if pt, ok := m["plaintext"]; ok {
		s, isStr := pt.(string)
		return !isStr || s == "" || s == "null" || s == `""`
	}
	return false
}

// normalizeTFName extracts the for_each key from a TF resource name.
// resourceName["key"] → key (strips any resource name prefix and brackets/quotes)
// resourceName[0] → 0
// plain_name → plain_name (no for_each key)
func normalizeTFName(name string) string {
	idx := strings.Index(name, "[")
	if idx < 0 {
		return name
	}
	key := name[idx+1 : len(name)-1] // strip [ and ]
	key = strings.Trim(key, `"`)
	return key
}

// urnName extracts the last segment of a URN.
func urnName(urn string) string {
	parts := strings.Split(urn, "::")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// LoadFieldsFile reads and parses an aws-import-diff-fields.json file.
func LoadFieldsFile(path string) (*FieldsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading fields file: %w", err)
	}
	var ff FieldsFile
	if err := json.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("parsing fields file: %w", err)
	}
	return &ff, nil
}
