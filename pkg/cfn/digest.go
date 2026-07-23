package cfn

// StackDigest is the agent-safe representation of a deployed CloudFormation
// stack — the CFN analog of tf-digest's ModuleMap. The raw stack/template is
// never read directly by the migration agent.
type StackDigest struct {
	StackName string        `json:"stackName"`
	Region    string        `json:"region"`
	Resources []CfnResource `json:"resources"`
}

// CfnResource is one resource in the deployed stack. ImportID is set ONLY for
// the AWS-lookup types (pre-resolved because they need live AWS); pure types
// are composed later by `resolve cfn` from Attributes.
type CfnResource struct {
	LogicalID      string                 `json:"logicalId"`
	CfnType        string                 `json:"cfnType"`
	PulumiType     string                 `json:"pulumiType,omitempty"`
	PhysicalID     string                 `json:"physicalId,omitempty"`
	ImportID       string                 `json:"importId,omitempty"` // pre-resolved (lookup types only)
	Attributes     map[string]interface{} `json:"attributes,omitempty"`
	DerivedName    string                 `json:"derivedName,omitempty"`
	CdkHashedName  bool                   `json:"cdkHashedName,omitempty"`
	ServerAssigned bool                   `json:"serverAssigned,omitempty"`
	Skipped        bool                   `json:"skipped,omitempty"`
	SkipReason     string                 `json:"skipReason,omitempty"`
}
