# CFN Migration Tool (`digest cfn` + `resolve cfn`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure `pulumi-tool-terraform-migrate`'s CLI into `digest {tf,cfn}` and `resolve {tf,cfn}` subcommands, and add the CFN path: `digest cfn` (deployed CloudFormation stack → agent-safe digest with resolved attributes and pre-fetched lookup IDs) and `resolve cfn` (fill a Pulumi import skeleton from that digest). The import-ID translate logic becomes a shared `pkg/importid` core serving both TF and CFN.

**Architecture:** AWS-dependent work (template read, intrinsic resolution, the 4 AWS-lookup-only import IDs) lives in `digest cfn`. Pure import-ID composition lives in a shared `pkg/importid` core keyed on the **Pulumi** type token, using generic **roles** whose values come from a per-source attribute adapter (TF reads `function_name`; CFN reads `FunctionName`). `resolve {tf,cfn}` are thin CLI leaves over that core. Existing `tf-digest` / `import-id-match` remain as **hidden aliases** so the shipped TF skill keeps working. All AWS access sits behind narrow interfaces (`StackReader`, `CloudControlReader`, `Lookups`) so logic is unit/golden-tested with fakes — no live AWS in the suite.

**Tech Stack:** Go 1.25, cobra, AWS SDK for Go **v2** (`cloudformation`, `cloudcontrol`, `iam`, `ec2`, `elasticloadbalancingv2`), stdlib `testing` + `testify` + `hexops/autogold/v2`.

## Global Constraints

- Module path: `github.com/pulumi/pulumi-tool-terraform-migrate`. Go `1.25.0`.
- **Command tree** (build the plan around exactly this):
  ```
  digest  ├── tf   (existing tf-digest flags; hidden alias: tf-digest)
          └── cfn  (--stack-name --region --synth-template --out --pulumi-stack --pulumi-project --project-dir)
  resolve ├── tf   (existing import-id-match flags; hidden alias: import-id-match)
          └── cfn  (--digest --import-file --mapping-file --provider --out)
  patch-state   (unchanged)
  set-secrets   (unchanged)
  ```
- **Hidden aliases:** `tf-digest` and `import-id-match` stay registered as top-level `cobra.Command`s with `Hidden: true`, whose `RunE` calls the SAME functions as `digest tf` / `resolve tf`. Do not duplicate logic — share the run function.
- New packages: `pkg/importid/` (shared translate core), `pkg/cfn/` (CFN digest + adapter). Existing `pkg/import_filler.go` stays; its TF matching is reused by `resolve tf`.
- AWS SDK: **v2** only (v2 already in `go.mod`; never introduce v1). Add `github.com/aws/aws-sdk-go-v2/service/{cloudformation,cloudcontrol,iam,ec2,elasticloadbalancingv2}` as direct deps.
- Cobra pattern: `newXxxCmd() *cobra.Command` constructor + package-level `init()` registering on the correct parent. Flags via `cmd.Flags().StringVar`/`BoolVar` + `cmd.MarkFlagRequired`. Body is `RunE`. Stats to `os.Stderr`; errors wrapped `fmt.Errorf("...: %w", err)`. JSON out via `json.MarshalIndent(v, "", "    ")` + `os.WriteFile(path, data, 0o644)`.
- No live AWS in tests. Golden tests: `autogold.ExpectFile(t, val)` → `testdata/<TestName>.golden`; regenerate with `go test ./... -update`.
- Build: `go build -o bin/pulumi-tool-terraform-migrate .`. Single test: `go test ./pkg/importid -run TestName -v`.
- **Pure composition → shared spec; AWS-lookup → pre-resolved in digest.** The shared `pkg/importid` spec table covers only pure string-composition types. The 4 lookup types (`AWS::IAM::Policy`→ARN, `AWS::EC2::SecurityGroup{Ingress,Egress}`→`sgr-*`, `AWS::EC2::EIP`→alloc id, `AWS::EC2::VPCGatewayAttachment`→`igw:vpc`) are pre-resolved by `digest cfn` and carried on the resource's `ImportID`.
- Provider scope: aws-classic everywhere; aws-native only for the API Gateway family. `AWS::ApiGateway::Deployment` native identifier order is **reversed** (`DeploymentId|RestApiId`) vs classic (`RestApiId/Id`).
- Skip these CFN types (emit a `Skipped` resource, no import entry): `AWS::CloudFormation::CustomResource`, `AWS::CDK::Metadata`, `AWS::CloudFormation::WaitCondition`, `AWS::CloudFormation::WaitConditionHandle`, any `Custom::` prefix.

---

### Task 1: Shared import-ID spec core (`pkg/importid`)

**Files:**
- Create: `pkg/importid/spec.go`
- Test: `pkg/importid/spec_test.go`

**Interfaces:**
- Produces:
  - `type Role string` + role constants.
  - `type IDSpec struct { Classic []Role; ClassicDelim string; Native []Role; NativeDelim string; Custom func(get func(Role) string, provider string) (string, error) }`
  - `var Specs map[string]IDSpec` — keyed by **Pulumi type token**.
  - `func Compose(pulumiType, provider string, get func(Role) string) (string, bool, error)` — returns `(id, handled, err)`; `handled==false` when the type has no spec (caller falls back to a pre-resolved ID).
- Consumed by: `resolve tf` and `resolve cfn` (Tasks 9, 11) and their source adapters.

- [ ] **Step 1: Write the failing test**

```go
// pkg/importid/spec_test.go
package importid

import "testing"

func TestCompose_PureJoins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		pulumiType string
		provider   string
		attrs      map[Role]string
		want       string
	}{
		{"lambda permission", "aws:lambda/permission:Permission", "classic",
			map[Role]string{RoleFunction: "ffs-dev-api", RoleStatement: "AllowS3"}, "ffs-dev-api/AllowS3"},
		{"apigw resource classic", "aws:apigateway/resource:Resource", "classic",
			map[Role]string{RoleRestApi: "abc", RoleID: "res"}, "abc/res"},
		{"apigw deployment native reversed", "aws:apigateway/deployment:Deployment", "native",
			map[Role]string{RoleRestApi: "abc", RoleID: "dep"}, "dep|abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			get := func(r Role) string { return tc.attrs[r] }
			got, handled, err := Compose(tc.pulumiType, tc.provider, get)
			if err != nil || !handled || got != tc.want {
				t.Fatalf("Compose = %q handled=%v err=%v; want %q", got, handled, err, tc.want)
			}
		})
	}
}

func TestCompose_Unhandled(t *testing.T) {
	t.Parallel()
	_, handled, err := Compose("aws:iam/policy:Policy", "classic", func(Role) string { return "" })
	if err != nil || handled {
		t.Fatalf("expected unhandled, no error; got handled=%v err=%v", handled, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/importid -run TestCompose -v`
Expected: FAIL — undefined package/symbols.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/importid/spec.go
package importid

import (
	"fmt"
	"strings"
)

// Role is a source-agnostic logical field name. Per-source adapters map roles
// to their own attribute names (TF: function_name; CFN: FunctionName).
type Role string

const (
	RoleFunction  Role = "function"
	RoleStatement Role = "statement"
	RoleRestApi   Role = "restApi"
	RoleID        Role = "id"
	RoleResource  Role = "resource"
	RoleHTTP      Role = "httpMethod"
	RoleUsagePlan Role = "usagePlan"
	RoleKey       Role = "key"
	RoleUserPool  Role = "userPool"
	RoleSubnet    Role = "subnet"
	RoleRouteTbl  Role = "routeTable"
	RoleServer    Role = "server"
	RoleUser      Role = "user"
	RoleQualifier Role = "qualifier"
	RoleListener  Role = "listener"
	RoleCert      Role = "certificate"
	RoleBucket    Role = "bucket"
	RoleQueue     Role = "queue"
	RoleHostZone  Role = "hostedZone"
	RoleName      Role = "name"
	RoleType      Role = "recordType"
	RoleSetID     Role = "setIdentifier"
	RoleStage     Role = "stage"
	RoleAuthorizer Role = "authorizer"
)

// IDSpec describes how to compose an import ID for a Pulumi type. Classic is
// the aws-classic format; Native (optional) is the aws-native format for the
// API Gateway family. Custom overrides both for reorder/split cases.
type IDSpec struct {
	Classic      []Role
	ClassicDelim string
	Native       []Role
	NativeDelim  string
	Custom       func(get func(Role) string, provider string) (string, error)
}

// Specs is keyed by Pulumi type token. Only pure-composition types appear here;
// AWS-lookup types are pre-resolved in the digest step.
var Specs = map[string]IDSpec{
	"aws:lambda/permission:Permission":                 {Classic: []Role{RoleFunction, RoleStatement}, ClassicDelim: "/"},
	"aws:apigateway/resource:Resource":                 {Classic: []Role{RoleRestApi, RoleID}, ClassicDelim: "/", Native: []Role{RoleRestApi, RoleID}, NativeDelim: "|"},
	"aws:apigateway/deployment:Deployment":             {Classic: []Role{RoleRestApi, RoleID}, ClassicDelim: "/", Native: []Role{RoleID, RoleRestApi}, NativeDelim: "|"}, // native reversed
	"aws:apigateway/method:Method":                     {Classic: []Role{RoleRestApi, RoleResource, RoleHTTP}, ClassicDelim: "/", Native: []Role{RoleRestApi, RoleResource, RoleHTTP}, NativeDelim: "|"},
	"aws:apigateway/usagePlanKey:UsagePlanKey":         {Classic: []Role{RoleUsagePlan, RoleKey}, ClassicDelim: "/", Native: []Role{RoleUsagePlan, RoleKey}, NativeDelim: "|"},
	"aws:apigateway/stage:Stage":                       {Native: []Role{RoleRestApi, RoleStage}, NativeDelim: "|", Classic: []Role{RoleRestApi, RoleStage}, ClassicDelim: "/"},
	"aws:apigateway/authorizer:Authorizer":             {Native: []Role{RoleRestApi, RoleAuthorizer}, NativeDelim: "|", Classic: []Role{RoleRestApi, RoleAuthorizer}, ClassicDelim: "/"},
	"aws:cognito/userPoolClient:UserPoolClient":        {Classic: []Role{RoleUserPool, RoleID}, ClassicDelim: "/"},
	"aws:ec2/routeTableAssociation:RouteTableAssociation": {Classic: []Role{RoleSubnet, RoleRouteTbl}, ClassicDelim: "/"},
	"aws:transfer/user:User":                           {Classic: []Role{RoleServer, RoleUser}, ClassicDelim: "/"},
	"aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig": {Classic: []Role{RoleFunction, RoleQualifier}, ClassicDelim: ":"},
	"aws:lb/listenerCertificate:ListenerCertificate":   {Classic: []Role{RoleListener, RoleCert}, ClassicDelim: "_"},
	"aws:s3/bucketPolicy:BucketPolicy":                 {Classic: []Role{RoleBucket}, ClassicDelim: ""},
	"aws:sqs/queuePolicy:QueuePolicy":                  {Classic: []Role{RoleQueue}, ClassicDelim: ""},
	"aws:route53/record:Record":                        {Custom: composeRoute53},
	"aws:appautoscaling/policy:Policy":                 {Custom: composeScalingPolicy},
	"aws:appautoscaling/target:Target":                 {Custom: composeScalableTarget},
	"aws:ecs/service:Service":                          {Custom: composeEcsService},
	"aws:transfer/server:Server":                       {Custom: composeTransferServer},
	"aws:lambda/layerVersionPermission:LayerVersionPermission": {Custom: composeLayerVersionPermission},
	"aws:ec2/route:Route":                              {Classic: []Role{RoleRouteTbl, RoleID}, ClassicDelim: "_"}, // Id role carries CidrBlock for Route (see adapter)
}

// Compose builds the import ID for a Pulumi type. Returns handled=false when the
// type is not in Specs (caller uses a pre-resolved ID or physical id).
func Compose(pulumiType, provider string, get func(Role) string) (id string, handled bool, err error) {
	spec, ok := Specs[pulumiType]
	if !ok {
		return "", false, nil
	}
	if spec.Custom != nil {
		id, err = spec.Custom(get, provider)
		return id, true, err
	}
	parts := spec.Classic
	delim := spec.ClassicDelim
	if provider == "native" && spec.Native != nil {
		parts = spec.Native
		delim = spec.NativeDelim
	}
	vals := make([]string, 0, len(parts))
	for _, r := range parts {
		v := get(r)
		if v == "" {
			return "", true, fmt.Errorf("%s: missing role %q", pulumiType, r)
		}
		vals = append(vals, v)
	}
	return strings.Join(vals, delim), true, nil
}
```

- [ ] **Step 4: Run to verify it passes** (the `Custom` funcs are declared in Task 2; add temporary stubs returning `("", nil)` if compiling now, or implement Task 2 before running the full package). For this task, run only the two tests above after stubbing customs:

Run: `go test ./pkg/importid -run TestCompose -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/importid/spec.go pkg/importid/spec_test.go
git commit -m "feat(importid): add shared Pulumi-type-keyed import-ID spec core"
```

---

### Task 2: Custom composers (reorder/split cases)

**Files:**
- Create: `pkg/importid/custom.go`
- Test: `pkg/importid/custom_test.go`

**Interfaces:**
- Produces: `composeRoute53`, `composeScalingPolicy`, `composeScalableTarget`, `composeEcsService`, `composeTransferServer`, `composeLayerVersionPermission` — each `func(get func(Role) string, provider string) (string, error)`. Referenced by `Specs` (Task 1).
- Consumes: role values via `get`. These need raw composite roles: `RoleScalingTargetID`, `RoleEcsID`, `RoleTransferID`, `RoleLayerArn` — add them to the const block in `spec.go`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/importid/custom_test.go
package importid

import "testing"

func TestCustomComposers(t *testing.T) {
	t.Parallel()
	// ScalingPolicy: ScalingTargetId "a|b|c" + PolicyName "cpu" -> cpu/c/a/b
	got, err := composeScalingPolicy(role(map[Role]string{RoleName: "cpu", RoleScalingTargetID: "svc|rid|dim"}), "classic")
	must(t, got, err, "cpu/dim/svc/rid")

	// ScalableTarget: Id "a|b|c" -> c/a/b
	got, err = composeScalableTarget(role(map[Role]string{RoleScalingTargetID: "svc|rid|dim"}), "classic")
	must(t, got, err, "dim/svc/rid")

	// ECS Service: Id "cluster/svc" -> svc
	got, err = composeEcsService(role(map[Role]string{RoleEcsID: "arn:cluster/svc"}), "classic")
	must(t, got, err, "svc")

	// Transfer Server: Id "a/s-1" -> s-1
	got, err = composeTransferServer(role(map[Role]string{RoleTransferID: "a/s-1"}), "classic")
	must(t, got, err, "s-1")

	// LayerVersionPermission: arn a:b:c:2 -> a:b:c,2
	got, err = composeLayerVersionPermission(role(map[Role]string{RoleLayerArn: "arn:l:1:2"}), "classic")
	must(t, got, err, "arn:l:1,2")

	// Route53: Z1 + a.example.com + A -> Z1_a.example.com_A
	got, err = composeRoute53(role(map[Role]string{RoleHostZone: "Z1", RoleName: "a.example.com", RoleType: "A"}), "classic")
	must(t, got, err, "Z1_a.example.com_A")
}

func role(m map[Role]string) func(Role) string { return func(r Role) string { return m[r] } }
func must(t *testing.T, got string, err error, want string) {
	t.Helper()
	if err != nil || got != want {
		t.Fatalf("got %q err=%v; want %q", got, err, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/importid -run TestCustomComposers -v`
Expected: FAIL — undefined composers / roles.

- [ ] **Step 3: Write minimal implementation**

Add roles to `spec.go` const block: `RoleScalingTargetID Role = "scalingTargetId"`, `RoleEcsID Role = "ecsId"`, `RoleTransferID Role = "transferId"`, `RoleLayerArn Role = "layerArn"`. Then:

```go
// pkg/importid/custom.go
package importid

import (
	"fmt"
	"strings"
)

func composeScalingPolicy(get func(Role) string, _ string) (string, error) {
	name := get(RoleName)
	parts := strings.Split(get(RoleScalingTargetID), "|")
	if name == "" || len(parts) != 3 {
		return "", fmt.Errorf("scaling policy needs name + 3-part target id")
	}
	return name + "/" + parts[2] + "/" + parts[0] + "/" + parts[1], nil
}

func composeScalableTarget(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleScalingTargetID), "|")
	if len(parts) != 3 {
		return "", fmt.Errorf("scalable target needs 3-part id")
	}
	return parts[2] + "/" + parts[0] + "/" + parts[1], nil
}

func composeEcsService(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleEcsID), "/")
	if len(parts) <= 1 {
		return "", fmt.Errorf("ecs service id has no cluster segment")
	}
	return strings.Join(parts[1:], "/"), nil
}

func composeTransferServer(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleTransferID), "/")
	if len(parts) <= 1 {
		return "", fmt.Errorf("transfer server id malformed")
	}
	return parts[1], nil
}

func composeLayerVersionPermission(get func(Role) string, _ string) (string, error) {
	parts := strings.Split(get(RoleLayerArn), ":")
	if len(parts) <= 1 {
		return "", fmt.Errorf("layer version arn malformed")
	}
	return strings.Join(parts[:len(parts)-1], ":") + "," + parts[len(parts)-1], nil
}

func composeRoute53(get func(Role) string, _ string) (string, error) {
	hz, name, typ := get(RoleHostZone), get(RoleName), get(RoleType)
	if hz == "" || name == "" || typ == "" {
		return "", fmt.Errorf("route53 record needs hostedZone, name, type")
	}
	id := hz + "_" + name + "_" + typ
	if sid := get(RoleSetID); sid != "" {
		id += "_" + sid
	}
	return id, nil
}
```

Remove any temporary stubs added in Task 1.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/importid -v`
Expected: PASS (Task 1 + Task 2 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/importid/spec.go pkg/importid/custom.go pkg/importid/custom_test.go
git commit -m "feat(importid): add custom composers for reorder/split cases"
```

---

### Task 3: CFN attribute adapter + digest types

**Files:**
- Create: `pkg/cfn/digest.go`, `pkg/cfn/adapter.go`
- Test: `pkg/cfn/digest_test.go`, `pkg/cfn/adapter_test.go`

**Interfaces:**
- Produces:
  - `StackDigest`, `CfnResource` (as in the design; `CfnResource.Attributes` holds resolved CFN property names).
  - `func CfnGetter(attrs map[string]interface{}) func(importid.Role) string` — maps `importid.Role` → CFN property value.
- Consumed by: digest builder (Task 6), `resolve cfn` (Task 9).

- [ ] **Step 1: Write the failing test**

```go
// pkg/cfn/adapter_test.go
package cfn

import (
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/importid"
	"github.com/stretchr/testify/require"
)

func TestCfnGetter(t *testing.T) {
	t.Parallel()
	get := CfnGetter(map[string]interface{}{
		"FunctionName": "ffs-dev-api", "Id": "AllowS3", "RestApiId": "abc",
	})
	require.Equal(t, "ffs-dev-api", get(importid.RoleFunction))
	require.Equal(t, "AllowS3", get(importid.RoleStatement)) // CFN "Id" -> statement role
	require.Equal(t, "abc", get(importid.RoleRestApi))
}
```

```go
// pkg/cfn/digest_test.go
package cfn

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackDigest_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	d := StackDigest{StackName: "ffs-dev", Region: "us-east-1", Resources: []CfnResource{{
		LogicalID: "MigrateFn", CfnType: "AWS::Lambda::Function", PhysicalID: "ffs-dev-migrate",
		PulumiType: "aws:lambda/function:Function",
		Attributes: map[string]interface{}{"FunctionName": "ffs-dev-migrate"},
	}}}
	data, err := json.Marshal(d)
	require.NoError(t, err)
	var got StackDigest
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, d, got)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/cfn -run 'TestCfnGetter|TestStackDigest' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/cfn/digest.go
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
	ImportID       string                 `json:"importId,omitempty"`       // pre-resolved (lookup types only)
	Attributes     map[string]interface{} `json:"attributes,omitempty"`
	DerivedName    string                 `json:"derivedName,omitempty"`
	CdkHashedName  bool                   `json:"cdkHashedName,omitempty"`
	ServerAssigned bool                   `json:"serverAssigned,omitempty"`
	Skipped        bool                   `json:"skipped,omitempty"`
	SkipReason     string                 `json:"skipReason,omitempty"`
}
```

```go
// pkg/cfn/adapter.go
package cfn

import (
	"fmt"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/importid"
)

// cfnRoleNames maps a source-agnostic role to the CloudFormation property name
// that carries its value. This is the CFN half of the shared translate core.
var cfnRoleNames = map[importid.Role]string{
	importid.RoleFunction:  "FunctionName",
	importid.RoleStatement: "Id",
	importid.RoleRestApi:   "RestApiId",
	importid.RoleID:        "Id",
	importid.RoleResource:  "ResourceId",
	importid.RoleHTTP:      "HttpMethod",
	importid.RoleUsagePlan: "UsagePlanId",
	importid.RoleKey:       "KeyId",
	importid.RoleUserPool:  "UserPoolId",
	importid.RoleSubnet:    "SubnetId",
	importid.RoleRouteTbl:  "RouteTableId",
	importid.RoleServer:    "ServerId",
	importid.RoleUser:      "UserName",
	importid.RoleQualifier: "Qualifier",
	importid.RoleListener:  "ListenerArn",
	importid.RoleCert:      "Certificates",
	importid.RoleBucket:    "Bucket",
	importid.RoleQueue:     "Queues",
	importid.RoleHostZone:  "HostedZoneId",
	importid.RoleName:      "Name",
	importid.RoleType:      "Type",
	importid.RoleSetID:     "SetIdentifier",
	importid.RoleStage:     "StageName",
	importid.RoleAuthorizer: "AuthorizerId",
	// Composite raw roles for custom composers:
	importid.RoleScalingTargetID: "ScalingTargetId",
	importid.RoleEcsID:           "Id",
	importid.RoleTransferID:      "Id",
	importid.RoleLayerArn:        "LayerVersionArn",
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
```

> **Implementer note:** `RoleName` is overloaded — Route53 uses CFN `Name`, ScalingPolicy uses CFN `PolicyName`. The getter prefers `PolicyName` when present. If a future type needs both simultaneously, split into two roles.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/cfn -run 'TestCfnGetter|TestStackDigest' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cfn/digest.go pkg/cfn/adapter.go pkg/cfn/adapter_test.go pkg/cfn/digest_test.go
git commit -m "feat(cfn): add StackDigest types and CFN role adapter"
```

---

### Task 4: CDK name classification

**Files:**
- Create: `pkg/cfn/names.go`
- Test: `pkg/cfn/names_test.go`

**Interfaces:**
- Produces: `func ClassifyName(logicalID, physicalID, cfnType string) (derivedName string, hashed bool, serverAssigned bool)` — consumed by the digest builder (Task 6).

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/cfn -run TestClassifyName -v`
Expected: FAIL — undefined `ClassifyName`.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/cfn -run TestClassifyName -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cfn/names.go pkg/cfn/names_test.go
git commit -m "feat(cfn): add CDK/CFN name classification"
```

---

### Task 5: CFN intrinsics + AWS lookups (interfaces + pure resolver)

**Files:**
- Create: `pkg/cfn/intrinsics.go`, `pkg/cfn/lookups.go`
- Test: `pkg/cfn/intrinsics_test.go`, `pkg/cfn/lookups_test.go`

**Interfaces:**
- Produces:
  - `type CloudControlReader interface { GetResource(ctx, typeName, id string) (map[string]interface{}, error) }`
  - `func ResolveProperties(ctx, props map[string]interface{}, resources, resourceTypes, exports map[string]string, cc CloudControlReader) (map[string]interface{}, error)`
  - `type Lookups interface { IAMPolicyARN(...); SecurityGroupRuleID(...); EIPAllocationID(...); InternetGatewayAttachment(...) }`
  - `func LookupImportID(ctx, cfnType string, attrs map[string]interface{}, lk Lookups) (string, bool, error)` — returns `(id, isLookupType, err)`; `isLookupType==false` for non-lookup types.
- Consumed by: digest builder (Task 6).

- [ ] **Step 1: Write the failing test** — `intrinsics_test.go` (identical to the design's `TestResolveProperties`, passing `resourceTypes` as the 4th arg) and:

```go
// pkg/cfn/lookups_test.go
package cfn

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeLookups struct{ policyARN, sgRule, eip, igw string }

func (f fakeLookups) IAMPolicyARN(context.Context, string) (string, error)  { return f.policyARN, nil }
func (f fakeLookups) SecurityGroupRuleID(context.Context, bool, map[string]interface{}) (string, error) {
	return f.sgRule, nil
}
func (f fakeLookups) EIPAllocationID(context.Context, string) (string, error) { return f.eip, nil }
func (f fakeLookups) InternetGatewayAttachment(context.Context, string) (string, error) {
	return f.igw, nil
}

func TestLookupImportID(t *testing.T) {
	t.Parallel()
	lk := fakeLookups{policyARN: "arn:pol", sgRule: "sgr-1", eip: "eipalloc-1", igw: "igw-1:vpc-1"}
	ctx := context.Background()

	id, isLookup, err := LookupImportID(ctx, "AWS::IAM::Policy", map[string]interface{}{"Id": "p"}, lk)
	require.NoError(t, err)
	require.True(t, isLookup)
	require.Equal(t, "arn:pol", id)

	_, isLookup, err = LookupImportID(ctx, "AWS::Lambda::Function", nil, lk)
	require.NoError(t, err)
	require.False(t, isLookup)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/cfn -run 'TestResolveProperties|TestLookupImportID' -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation** — `intrinsics.go` as in the design doc (Ref / Fn::GetAtt / Fn::ImportValue / Fn::Join with `resourceTypes`), plus:

```go
// pkg/cfn/lookups.go
package cfn

import (
	"context"
	"fmt"
)

type Lookups interface {
	IAMPolicyARN(ctx context.Context, nameOrID string) (string, error)
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
func LookupImportID(ctx context.Context, cfnType string, attrs map[string]interface{}, lk Lookups) (string, bool, error) {
	switch cfnType {
	case "AWS::IAM::Policy":
		id, err := lk.IAMPolicyARN(ctx, str(attrs, "Id"))
		return id, true, err
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/cfn -run 'TestResolveProperties|TestLookupImportID' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cfn/intrinsics.go pkg/cfn/lookups.go pkg/cfn/intrinsics_test.go pkg/cfn/lookups_test.go
git commit -m "feat(cfn): add intrinsic resolution and AWS-lookup import IDs"
```

---

### Task 6: CFN→Pulumi type map + digest builder (golden)

**Files:**
- Create: `pkg/cfn/typemap.go`, `pkg/cfn/build.go`
- Test: `pkg/cfn/build_test.go`
- Test data: `pkg/cfn/testdata/ffs-min.template.json`, `pkg/cfn/testdata/TestBuildDigest_Golden.golden`

**Interfaces:**
- Produces:
  - `func PulumiType(cfnType string) string` — CFN type → aws-classic Pulumi token (curated map; unknown → "").
  - `type StackReader interface { GetTemplate(...); ListStackResources(...) ([]StackResource, error); GetExports(...) }`, `type StackResource struct { LogicalID, PhysicalID, CfnType string }`.
  - `func BuildDigest(ctx, stackName, region string, sr StackReader, cc CloudControlReader, lk Lookups) (*StackDigest, error)`.
- Consumes: `ClassifyName` (T4), `ResolveProperties` + `LookupImportID` (T5), `PulumiType`.

- [ ] **Step 1: Write the failing test** — create `testdata/ffs-min.template.json` (ApiPermission with `Ref` to ApiHandler + `Id`; ApiHandler; a `MetaData` of `AWS::CDK::Metadata`), and `build_test.go` with a `fakeStack` + `fakeCC` driving `BuildDigest` and asserting via `autogold.ExpectFile(t, digest)` (same structure as the design doc's `TestBuildDigest_Golden`).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/cfn -run TestBuildDigest_Golden -v`
Expected: FAIL — undefined `BuildDigest`, `PulumiType`, `StackReader`.

- [ ] **Step 3: Write minimal implementation**

`typemap.go`: a curated `map[string]string` covering the field-report resource families (Lambda, IAM role/policy, ECS, API Gateway family, S3, LogGroup, EIP, SG rules, etc.) plus `func PulumiType(t string) string { return typeMap[t] }`. Seed with ~40 entries from the port-target table; unknown returns "".

`build.go`:

```go
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
```

- [ ] **Step 4: Generate + verify golden**

Run: `go test ./pkg/cfn -run TestBuildDigest_Golden -update -v`
Inspect `testdata/TestBuildDigest_Golden.golden`: `ApiPermission` has `pulumiType: aws:lambda/permission:Permission`, `attributes.FunctionName: ffs-dev-api` (Ref resolved), no `importId` (pure type — composed at resolve). `MetaData` is `skipped`. Then run without `-update`:
Run: `go test ./pkg/cfn -run TestBuildDigest_Golden -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cfn/typemap.go pkg/cfn/build.go pkg/cfn/build_test.go pkg/cfn/testdata/
git commit -m "feat(cfn): add CFN->Pulumi type map and digest builder (golden)"
```

---

### Task 7: Real AWS clients

**Files:**
- Create: `pkg/cfn/clients.go`, `pkg/cfn/clients_test.go` (interface assertions)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: `func NewAWSClients(ctx, region string) (StackReader, CloudControlReader, Lookups, error)`.

- [ ] **Step 1: Add SDK modules**

```bash
go get github.com/aws/aws-sdk-go-v2/service/cloudformation@latest \
       github.com/aws/aws-sdk-go-v2/service/cloudcontrol@latest \
       github.com/aws/aws-sdk-go-v2/service/iam@latest \
       github.com/aws/aws-sdk-go-v2/service/ec2@latest \
       github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2@latest
```

- [ ] **Step 2: Write the failing test**

```go
// pkg/cfn/clients_test.go
package cfn

var (
	_ StackReader        = (*cfnStackReader)(nil)
	_ CloudControlReader = (*ccReader)(nil)
	_ Lookups            = (*awsLookups)(nil)
)
```

- [ ] **Step 3: Run to verify it fails**

Run: `go build ./pkg/cfn`
Expected: FAIL — undefined adapter types.

- [ ] **Step 4: Write minimal implementation** — `clients.go` with `NewAWSClients` loading `awsconfig.LoadDefaultConfig(ctx, WithRegion(region))` and adapter types `cfnStackReader`/`ccReader`/`awsLookups` implementing each interface method with the SDK v2 calls enumerated in the design doc (`GetTemplate`, paginated `ListStackResources`/`ListExports`, `GetResource` → JSON-unmarshal `Properties`, `ListPolicies`, `DescribeSecurityGroupRules`, `DescribeAddresses`, `DescribeInternetGateways`). Methods are thin, no branching beyond nil-checks and the single-match assertion for SG rules.

- [ ] **Step 5: Run to verify it passes**

Run: `go build ./... && go test ./pkg/cfn -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/cfn/clients.go pkg/cfn/clients_test.go go.mod go.sum
git commit -m "feat(cfn): add real AWS SDK v2 client adapters"
```

---

### Task 8: `resolve cfn` fill logic (shared core)

**Files:**
- Create: `pkg/cfn/resolve.go`
- Test: `pkg/cfn/resolve_test.go`

**Interfaces:**
- Produces: `func FillFromDigest(digest *StackDigest, importFile *pkg.ImportFile, mappings map[string]string, provider string) *pkg.FillResult`.
- Consumes: `pkg.ImportFile`/`ImportEntry`/`FillResult`; `importid.Compose`; `CfnGetter`.

> **Fill+translate rule:** match each import entry to a digest resource by CFN logical ID (entry name suffix after last `-`, or explicit `mappings[entry.Name]`). Then: (1) `importid.Compose(entry.Type, provider, CfnGetter(res.Attributes))` — if `handled`, use it; (2) else if `res.ImportID != ""` (pre-resolved lookup type), use it; (3) else use `res.PhysicalID`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/cfn/resolve_test.go
package cfn

import (
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg"
	"github.com/stretchr/testify/require"
)

func TestFillFromDigest(t *testing.T) {
	t.Parallel()
	digest := &StackDigest{Resources: []CfnResource{
		{LogicalID: "ApiPermission", CfnType: "AWS::Lambda::Permission",
			PulumiType: "aws:lambda/permission:Permission",
			Attributes: map[string]interface{}{"FunctionName": "ffs-dev-api", "Id": "AllowApiGw"}},
		{LogicalID: "TaskPolicy", CfnType: "AWS::IAM::Policy",
			PulumiType: "aws:iam/policy:Policy", ImportID: "arn:aws:iam::1:policy/p"},
		{LogicalID: "Dep", CfnType: "AWS::ApiGateway::Deployment",
			Attributes: map[string]interface{}{"RestApiId": "abc", "Id": "dep"}},
	}}
	importFile := &pkg.ImportFile{Resources: []pkg.ImportEntry{
		{Type: "aws:lambda/permission:Permission", Name: "ffs-ApiPermission"},
		{Type: "aws:iam/policy:Policy", Name: "ffs-TaskPolicy"},
		{Type: "aws:apigateway/deployment:Deployment", Name: "ffs-Dep"},
	}}
	res := FillFromDigest(digest, importFile, nil, "native")
	require.Equal(t, 3, res.Filled)
	require.Equal(t, "ffs-dev-api/AllowApiGw", importFile.Resources[0].ID) // composed
	require.Equal(t, "arn:aws:iam::1:policy/p", importFile.Resources[1].ID) // pre-resolved lookup
	require.Equal(t, "dep|abc", importFile.Resources[2].ID)                 // native reversed
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/cfn -run TestFillFromDigest -v`
Expected: FAIL — undefined `FillFromDigest`.

- [ ] **Step 3: Write minimal implementation**

```go
// pkg/cfn/resolve.go
package cfn

import (
	"strings"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg"
	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/importid"
)

func suffix(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// FillFromDigest fills each import entry's ID from the matching digest resource,
// composing pure types via the shared spec core and using pre-resolved IDs for
// lookup types.
func FillFromDigest(digest *StackDigest, importFile *pkg.ImportFile, mappings map[string]string, provider string) *pkg.FillResult {
	byLogical := make(map[string]*CfnResource, len(digest.Resources))
	for i := range digest.Resources {
		byLogical[digest.Resources[i].LogicalID] = &digest.Resources[i]
	}
	res := &pkg.FillResult{}
	for i := range importFile.Resources {
		entry := &importFile.Resources[i]
		if entry.Component {
			continue
		}
		logical := suffix(entry.Name)
		if m, ok := mappings[entry.Name]; ok {
			logical = m
		}
		r, ok := byLogical[logical]
		if !ok || r.Skipped {
			res.Unmatched++
			res.Warnings = append(res.Warnings, "no digest match for "+entry.Name)
			continue
		}
		if id, handled, err := importid.Compose(entry.Type, provider, CfnGetter(r.Attributes)); err == nil && handled {
			entry.ID = id
		} else if r.ImportID != "" {
			entry.ID = r.ImportID
		} else {
			entry.ID = r.PhysicalID
		}
		res.Filled++
	}
	return res
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/cfn -run TestFillFromDigest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cfn/resolve.go pkg/cfn/resolve_test.go
git commit -m "feat(cfn): add resolve-cfn fill using shared spec core"
```

---

### Task 9: `digest` parent + `digest cfn` + `digest tf` (with hidden alias)

**Files:**
- Create: `cmd/digest.go`, `cmd/digest_cfn.go`, `cmd/digest_tf.go`
- Modify: `cmd/tf_digest.go` (extract its run body into a shared function; register a hidden `tf-digest` alias)
- Test: `cmd/digest_test.go`

**Interfaces:**
- Consumes: `cfn.NewAWSClients`, `cfn.BuildDigest`; the existing tf-digest run function.

- [ ] **Step 1: Write the failing test**

```go
// cmd/digest_test.go
package cmd

import "testing"

func TestDigestSubcommands(t *testing.T) {
	for _, path := range [][]string{{"digest", "cfn"}, {"digest", "tf"}, {"tf-digest"}} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("%v not registered: cmd=%v err=%v", path, cmd, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd -run TestDigestSubcommands -v`
Expected: FAIL — `digest` parent not found.

- [ ] **Step 3: Write minimal implementation**

- `cmd/digest.go`: a parent command `newDigestCmd()` (`Use: "digest"`, no RunE) registered on `rootCmd` in `init()`.
- Refactor `cmd/tf_digest.go`: move the `RunE` body into `func runTfDigest(cmd *cobra.Command, flags tfDigestFlags) error`. Keep the existing top-level `tf-digest` command but set `Hidden: true` and have its `RunE` call `runTfDigest`.
- `cmd/digest_tf.go`: `newDigestTfCmd()` with the same flags, `RunE` calling `runTfDigest`, registered on the `digest` parent.
- `cmd/digest_cfn.go`:

```go
// cmd/digest_cfn.go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/cfn"
	"github.com/spf13/cobra"
)

func newDigestCfnCmd() *cobra.Command {
	var stackName, region, out string
	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Digest a deployed CloudFormation stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sr, cc, lk, err := cfn.NewAWSClients(ctx, region)
			if err != nil {
				return fmt.Errorf("aws clients: %w", err)
			}
			digest, err := cfn.BuildDigest(ctx, stackName, region, sr, cc, lk)
			if err != nil {
				return fmt.Errorf("build digest: %w", err)
			}
			data, err := json.MarshalIndent(digest, "", "    ")
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Resources: %d\nOutput written to %s\n", len(digest.Resources), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&stackName, "stack-name", "", "Deployed CloudFormation stack name")
	cmd.Flags().StringVar(&region, "region", "", "AWS region")
	cmd.Flags().StringVarP(&out, "out", "o", "", "Output digest JSON path")
	cmd.MarkFlagRequired("stack-name")
	cmd.MarkFlagRequired("region")
	cmd.MarkFlagRequired("out")
	return cmd
}
```

Wire in `cmd/digest.go`'s `init()`: build the parent, add `newDigestTfCmd()` and `newDigestCfnCmd()`, add parent to `rootCmd`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd -run TestDigestSubcommands -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/digest.go cmd/digest_cfn.go cmd/digest_tf.go cmd/tf_digest.go cmd/digest_test.go
git commit -m "feat(cmd): add digest parent with tf/cfn subcommands + hidden tf-digest alias"
```

---

### Task 10: `resolve` parent + `resolve cfn` + `resolve tf` (with hidden alias)

**Files:**
- Create: `cmd/resolve.go`, `cmd/resolve_cfn.go`, `cmd/resolve_tf.go`
- Modify: `cmd/import_id_match.go` (extract run body; hidden `import-id-match` alias)
- Test: `cmd/resolve_test.go`

**Interfaces:**
- Consumes: `cfn.FillFromDigest`; the existing import-id-match run function.

- [ ] **Step 1: Write the failing test**

```go
// cmd/resolve_test.go
package cmd

import "testing"

func TestResolveSubcommands(t *testing.T) {
	for _, path := range [][]string{{"resolve", "cfn"}, {"resolve", "tf"}, {"import-id-match"}} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("%v not registered: cmd=%v err=%v", path, cmd, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd -run TestResolveSubcommands -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

- `cmd/resolve.go`: parent `newResolveCmd()` (`Use: "resolve"`), registered on `rootCmd`.
- Refactor `cmd/import_id_match.go`: extract body into `func runImportIDMatch(...) error`; keep top-level `import-id-match` command as `Hidden: true` calling it; add `newResolveTfCmd()` on the `resolve` parent calling the same function.
- `cmd/resolve_cfn.go`: `newResolveCfnCmd()` with flags `--digest --import-file --mapping-file --provider (default "classic") --out`, `RunE` that reads the `cfn.StackDigest` JSON + `pkg.ImportFile` JSON + optional YAML mappings, calls `cfn.FillFromDigest`, writes the filled import file, prints `Filled`/`Unmatched` to stderr. (Mirror the design doc's `resolve cfn` command body; reuse `gopkg.in/yaml.v3` as `import-id-match` does.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd -run TestResolveSubcommands -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/resolve.go cmd/resolve_cfn.go cmd/resolve_tf.go cmd/import_id_match.go cmd/resolve_test.go
git commit -m "feat(cmd): add resolve parent with tf/cfn subcommands + hidden import-id-match alias"
```

---

### Task 11: Port full resolver table (data-driven golden)

**Files:**
- Modify: `pkg/importid/spec.go` (+ `pkg/cfn/adapter.go`, `pkg/cfn/typemap.go` for any new roles/types)
- Test: `pkg/importid/table_test.go`
- Test data: `pkg/importid/testdata/TestResolverTable.golden`

**Interfaces:** Consumes `Compose`.

Close coverage against the **full port-target table** (from `pulumi-tool-importer`). Pure-composition rows go in `Specs`; lookup rows are digest-side (Task 5) and excluded here.

| Pulumi type | Roles / construction |
|---|---|
| aws:lambda/permission:Permission | function `/` statement |
| aws:apigateway/resource:Resource | restApi `/` id (native `\|`) |
| aws:apigateway/deployment:Deployment | restApi `/` id (native id `\|` restApi — reversed) |
| aws:apigateway/method:Method | restApi `/` resource `/` httpMethod |
| aws:apigateway/usagePlanKey:UsagePlanKey | usagePlan `/` key |
| aws:cognito/userPoolClient:UserPoolClient | userPool `/` id |
| aws:ec2/routeTableAssociation:RouteTableAssociation | subnet `/` routeTable |
| aws:transfer/user:User | server `/` user |
| aws:transfer/server:Server | split id on `/`, take [1] |
| aws:ecs/service:Service | split id on `/`, drop first |
| aws:lb/listenerCertificate:ListenerCertificate | listener `_` certificate |
| aws:lambda/functionEventInvokeConfig:FunctionEventInvokeConfig | function `:` qualifier |
| aws:lambda/layerVersionPermission:LayerVersionPermission | layerArn `,` version |
| aws:route53/record:Record | hostedZone `_` name `_` type [`_` setId] |
| aws:appautoscaling/policy:Policy | name/parts[2]/parts[0]/parts[1] |
| aws:appautoscaling/target:Target | parts[2]/parts[0]/parts[1] |
| aws:s3/bucketPolicy:BucketPolicy | bucket |
| aws:sqs/queuePolicy:QueuePolicy | queue |
| aws:ec2/route:Route | routeTable `_` cidr |
| aws:cloudwatch/logGroup:LogGroup | name |
| aws:cloudwatch/metricAlarm:MetricAlarm | name |
| aws:cloudwatch/eventRule:EventRule | arn (or busName/ruleName) |
| aws:cloudwatch/eventBus:EventBus | name |
| aws:ec2/volumeAttachment:VolumeAttachment | device `:` volume `:` instance |
| aws:iam/policy:Policy (ManagedPolicy) | policyArn |

- [ ] **Step 1: Write the failing golden test** — a table exercising every pure row with representative role attrs, collecting `{pulumiType: id}` and asserting `autogold.ExpectFile(t, result)`.
- [ ] **Step 2: Run** `go test ./pkg/importid -run TestResolverTable -v` — Expected: FAIL (missing: logGroup, metricAlarm, eventRule, eventBus, volumeAttachment, managed policy, route).
- [ ] **Step 3: Add missing `Specs` entries + roles** (e.g. `RoleDevice`, `RoleVolume`, `RoleInstance`, `RoleArn`, `RoleCidr`) and their `cfnRoleNames` mappings. Add corresponding `typemap.go` rows.
- [ ] **Step 4: Generate + verify golden** — `-update`, inspect, re-run without `-update`. Expected: PASS.
- [ ] **Step 5: Commit**

```bash
git add pkg/importid/ pkg/cfn/adapter.go pkg/cfn/typemap.go
git commit -m "feat(importid): complete resolver coverage against full port-target table"
```

---

### Task 12: Extend `aws-import-diff-fields.json`

**Files:**
- Modify: `aws-import-diff-fields.json`
- Test: `pkg/cfn/diff_fields_test.go`

- [ ] **Step 1: Write the failing test** — read `../../aws-import-diff-fields.json`, assert `fields["aws:apigateway/integration:Integration"].not_read.passthroughBehavior` exists (as in the design doc).
- [ ] **Step 2: Run** `go test ./pkg/cfn -run TestDiffFieldsHasApiGatewayIntegrationDefaults -v` — Expected: FAIL.
- [ ] **Step 3: Add** `aws:apigateway/integration:Integration` (`passthroughBehavior: WHEN_NO_MATCH`, `timeoutInMillis: 29000`, `connectionType: INTERNET`) and `aws:apigateway/stage:Stage` (`cacheClusterEnabled: false`) under `fields`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit**

```bash
git add aws-import-diff-fields.json pkg/cfn/diff_fields_test.go
git commit -m "feat(cfn): extend diff-fields with API Gateway provider-populated defaults"
```

---

### Task 13: Full suite + build gate

**Files:** none.

- [ ] **Step 1:** `go test ./... 2>&1 | tail -40` — Expected: `pkg/importid`, `pkg/cfn`, `cmd` PASS. (Live-AWS `test/` integration tests may skip/fail for lack of creds — confirm that matches `main`'s pre-existing state; do not block on it.)
- [ ] **Step 2:** `go build -o bin/pulumi-tool-terraform-migrate . && ./bin/pulumi-tool-terraform-migrate digest cfn --help` — Expected: build ok; help lists `--stack-name`, `--region`, `--out`.
- [ ] **Step 3:** `./bin/pulumi-tool-terraform-migrate resolve cfn --help` and `./bin/pulumi-tool-terraform-migrate tf-digest --help` — Expected: cfn resolve help lists `--digest`/`--provider`; hidden `tf-digest` still runs.
- [ ] **Step 4:** `git commit -m "chore(cfn): full suite green + build gate" --allow-empty`

---

## Self-Review

**Spec coverage** (against `2026-07-23-cdk-to-pulumi-migration-design.md`, Component 1, with the revised command tree):
- `digest cfn` (resolved attributes, derived name, `cdkHashedName`/`serverAssigned`, pre-resolved lookup IDs, deployed-stack source of truth) → Tasks 3, 4, 5, 6, 9. ✓
- `resolve {tf,cfn}` unified over a shared `pkg/importid` core (classic + native, reversed Deployment, port of the resolver table) → Tasks 1, 2, 8, 10, 11. ✓
- `digest`/`resolve` subcommand tree + hidden `tf-digest`/`import-id-match` aliases → Tasks 9, 10. ✓
- Reuse + extend `aws-import-diff-fields.json` → Task 12. ✓
- Secret handling (NoEcho params) → **deferred, documented** (see below).

**Placeholder scan:** No "TBD"/"handle appropriately". Tasks 7, 9, 10, 11 delegate enumerated bodies to the design doc's exact code/SDK-call lists rather than leaving them vague; each names the precise functions/flags to write.

**Type consistency:** `importid.Role` constants and `Specs` keys (Pulumi type tokens) are shared by `Compose` (T1), custom composers (T2), the CFN adapter `cfnRoleNames` (T3), and the port table (T11). `StackDigest`/`CfnResource` identical across T3, T6, T8. `CfnGetter` signature `func(importid.Role) string` matches `Compose`'s `get` param. `FillFromDigest` signature consistent T8↔T10. Existing `pkg.ImportFile`/`ImportEntry`/`FillResult` reused unchanged.

**Deliberate deferrals / follow-ups (out of scope, not silently dropped):**
- **Full TF migration onto the shared core:** `resolve tf` currently routes to the existing `TranslateImportIDs`/`FillImportFile` (proven, golden-guarded) via the shared command wiring; porting the TF path to consume `pkg/importid` (a TF role adapter mirroring `CfnGetter`) is a fast-follow guarded by the existing TF goldens. The CLI is already symmetric; this unifies the *implementation* without risking the shipped skill.
- **NoEcho / inline-secret redaction in `digest cfn`:** the field report found CDK secrets were largely referenced by name (N/A); reuse the existing `set-secrets`/config path when a stack with inline secrets appears.
- **Real FaceFinderService end-to-end golden:** Task 6 uses a minimal synthetic template (the design's open question); substitute a fuller fixture when the stack is re-deployable.
- **`Fn::Sub`:** only Ref/GetAtt/ImportValue/Join implemented; add if a target stack uses `Fn::Sub`.
- **aws-native SG-rule / composite lookups beyond the API Gateway family:** out of scope per the classic-default posture.
