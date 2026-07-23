// pkg/cfn/names.go
package cfn

import "regexp"

// cdkHashSuffix matches CDK's construct-path hash: 8 uppercase-hex chars ending
// a logical ID (e.g. ...DefaultPolicyDFEB0894).
var cdkHashSuffix = regexp.MustCompile(`[0-9A-F]{8}$`)

// cfnRandomSuffix matches CloudFormation's server-assigned suffix on a physical
// ID: a hyphen + 12-13 mixed-case alphanumerics (e.g. ...-xQMUV6Ikl78Y).
var cfnRandomSuffix = regexp.MustCompile(`-[0-9A-Za-z]{12,13}$`)

// ClassifyName decides how a resource's name is handled on migration.
//   - hashed: settable name carrying a CDK construct hash -> route to config.
//   - serverAssigned: CFN-generated name -> leave unset, import preserves it.
// Mutually exclusive; a server-assigned physical ID wins.
func ClassifyName(logicalID, physicalID, cfnType string) (derivedName string, hashed bool, serverAssigned bool) {
	if cfnRandomSuffix.MatchString(physicalID) {
		return physicalID, false, true
	}
	if cdkHashSuffix.MatchString(logicalID) {
		return logicalID, true, false
	}
	return physicalID, false, false
}
