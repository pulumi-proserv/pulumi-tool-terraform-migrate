# CDK / CloudFormation → Pulumi Migration Asset — Design

**Date:** 2026-07-23
**Status:** Approved design, pre-implementation
**Author:** Jonathan Davenport (with Claude)

## Context

We have a validated, in-production Terraform→Pulumi migration asset set:

- **`pulumi-terraform-migrate`** (Go CLI, this repo) — `tf-digest`, `import-id-match`, `patch-state`, `set-secrets`, plus `aws-import-diff-fields.json`.
- **`pulumi-terraform-workspace-migration`** skill — the six-phase orchestration.
- **`pulumi-terraform-module-to-component`** skill — TF module → Pulumi TS ComponentResource.

A customer (Andrew H) successfully migrated a real, deployed 183-resource AWS CDK
app (C#/.NET 8, `FaceFinderService/FFSStack`) to Pulumi TypeScript by following the
Terraform workspace-migration skill as a model, hand-substituting CDK/CFN tooling
where the TF-specific parts didn't apply. He drove it to a clean `pulumi preview`
(zero diff except unavoidable write-only fields). His field report is the primary
input to this design.

**Core finding from the field report:** the workspace skill's macro-process —
incremental, dependency-ordered, zero-diff-gated import — transferred almost
perfectly. The friction was entirely in **Phase 1 (analyze)** and **Phase 4
(import)**, which are deeply Terraform-specific. CDK has no state file; its "state"
is the deployed CloudFormation stack.

## Research summary (what already exists)

- **Every official CDK→Pulumi tool targets aws-native**: `cf2pulumi`, `cdk2pulumi`
  (convert), `pulumi-tool-cdk-importer` (import), `pulumi-cdk` (interop), Neo
  (AI end-to-end). aws-native maps 1:1 to CloudFormation, so import-by-identifier
  is highly automatable — but aws-native has **less functionality than classic**,
  and the converters emit **flat, low-abstraction code**.
- **No existing tool does CloudFormation → aws-classic well.** That is exactly the
  gap the customer hit and why he hand-authored classic.
- **The hardest missing piece already exists in-house.** `pulumi-tool-importer`
  (F#/.NET) is a working reference implementation of CFN → aws-classic import-ID
  resolution: it reads a live stack (`GetTemplate` + `ListStackResources`),
  resolves `Ref`/`Fn::GetAtt`/`Fn::ImportValue`/`Fn::Join` via Cloud Control, and
  has a per-type composite-ID resolver table (`importIdentityResolvers` in
  `src/Server/AwsCloudFormationTemplates.fs`) covering the hard cases: Lambda
  Permission = `FunctionName/Id`, API Gateway Method = `restApiId/resourceId/httpMethod`,
  SG rules matched via `DescribeSecurityGroupRules`, IAM policy ARN via
  `ListPolicies`, EIP allocation IDs, ScalingPolicy reorder, Route53, etc. It emits
  **aws-classic tokens exclusively** and is extended by adding one keyed entry.
- **The Terraform tool contributes two provider-level (not TF-level) reusable
  layers:** `TranslateImportIDs` in `pkg/import_filler.go` (switch on Pulumi type
  → composite ID) and `aws-import-diff-fields.json` + `pkg/state_patcher.go`
  (per-type not-read/write-only field table, falsy-default suppression keyed on
  provider version). Because CDK migration targets the **same `pulumi-aws`
  provider**, `aws-import-diff-fields.json` carries over almost wholesale.
- **The "latest aws-classic provider" note is confirmed.** pulumi-aws fixed the
  write-only / default-tags phantom-diff-on-import issues (pulumi-aws #5215, #4030),
  per-field/per-resource. Importing into the *latest* aws classic provider avoids a
  large class of phantom diffs — this becomes a hard prerequisite in the skill.

## Decisions

| # | Decision | Rationale |
|---|---|---|
| 1 | **Migration posture: hybrid, classic-default** | Customer lands on an idiomatic, component-structured, full-featured classic Pulumi codebase they maintain long-term (consistent with the rest of a Pulumi estate). aws-native used surgically only where classic explodes. NOT all-in native (flat code, reduced functionality) — that's a lift-and-shift, not a PS deliverable worth keeping. |
| 2 | **Tool: new Go command group, full parity** | Mirrors `pulumi-terraform-migrate` ergonomics (`pulumi plugin run terraform-migrate -- <cmd>`); reuses `patch-state`, `set-secrets`, `aws-import-diff-fields.json` directly. |
| 3 | **Repo: same repo (`pulumi-tool-terraform-migrate`), shared `pkg/`** | patch-state, set-secrets, and the diff-fields JSON are shared plumbing. One tool, one release. Repo keeps its `terraform` name for now (neutral rename is deferrable churn). |
| 4 | **Program language: TypeScript for v1** | Matches the existing workspace + component skills; most mature zero-diff tooling; reuses `pulumi-terraform-module-to-component` patterns directly. C# noted as a future extension (the shops are .NET). |
| 5 | **Provider ID scope: both classic + native, native scoped to the API Gateway family** | `import-id-resolve` resolves classic IDs everywhere plus aws-native IDs for the bounded API Gateway set (RestApi, Resource, Method, Deployment, Stage, Authorizer, Integration, Lambda Permission) — where classic explodes and where we have the exact Cloud Control identifier-order quirks. Anything native beyond that set routes to existing Pulumi tooling. |
| 6 | **Digest source of truth: the deployed CloudFormation stack** | `GetTemplate` + `ListStackResources` + targeted `describe-*`; `cdk synth` used only as a cross-check that the app hasn't drifted. The deployed stack is authoritative for physical IDs and drift. |
| 7 | **`cfn-digest` is consume-only for synth** | It does not run `cdk synth` itself (synth needs Docker + the CDK toolchain — expensive/fragile). The skill runs synth if needed and passes the template path. |
| 8 | **Three deliverables, `cdk-construct-to-component` stays standalone** | Parity with the TF asset set, even though #3 is lighter (heavy reuse of the TF component skill). |

## Deliverable set

Three artifacts, each paralleling an existing Terraform one, sharing as much as possible.

| # | New artifact | Parallels | Reuse strategy |
|---|---|---|---|
| 1 | **CFN subcommands** in `pulumi-tool-terraform-migrate` (`digest cfn`, `resolve cfn`) | `digest tf`, `resolve tf` | New `cmd/` files + new `pkg/cfn/` + shared `pkg/importid/`; reuses `patch-state`, `set-secrets`, `aws-import-diff-fields.json`, provider/URN plumbing unchanged |
| 2 | **`cdk-cloudformation-workspace-migration`** skill | `pulumi-terraform-workspace-migration` | Phases 2, 3, 5, 6 transfer almost verbatim; Phases 0, 1, 4 are CFN-specific |
| 3 | **`cdk-construct-to-component`** skill | `pulumi-terraform-module-to-component` | ~80% reuse; new part is CDK construct → component interface + CDK-name-to-config handling |

## Component 1 — The tool

> **Revision (2026-07-23):** The CLI is organized as **subcommands** — `digest tf|cfn` and `resolve tf|cfn` — not flat `cfn-digest`/`import-id-resolve` commands. Existing `tf-digest` and `import-id-match` remain as **hidden aliases** so the shipped TF skill keeps working. The import-ID translate logic is a **shared `pkg/importid` core** keyed on the Pulumi type token (roles + per-source attribute adapters) serving both TF and CFN. Crucially, `digest cfn` stores resolved *attributes* (plus pre-resolved IDs for the 4 AWS-lookup types); `resolve cfn` composes the final import ID from those attributes via the shared core — mirroring the TF `tf-digest`→`import-id-match` split exactly. See the implementation plan `docs/superpowers/plans/2026-07-23-cfn-migration-tool.md` for the authoritative command tree and signatures.

### `digest cfn` (the `digest tf` / `tf-digest` analog)

Produces the agent-safe JSON the skill reads instead of the raw stack.

```
pulumi plugin run terraform-migrate -- digest cfn \
  --stack-name face-finder-service-dev \
  --region us-east-1 \
  --out /tmp/ffs-cfn-digest.json
```

> **Implemented scope (v1):** `digest cfn` ships with `--stack-name --region --out`
> only. The `--synth-template`, `--pulumi-stack`, `--pulumi-project`, and
> `--project-dir` flags shown in earlier drafts are **deferred**: the digest reads
> the *deployed* template live via `GetTemplate` (so a synth cross-check is
> optional and not wired), and NoEcho/secret-setting into stack config is a
> documented follow-up (the field report found CDK secrets were referenced by
> name, so no inline-secret extraction was needed for v1). When secret-setting is
> added, the `--pulumi-*`/`--project-dir` flags return, matching `digest tf`.

Per resource, emits:
- logical ID, CFN resource type, Pulumi type hint
- resolved attributes — from the deployed template's resolved properties + Cloud
  Control / `describe-*` calls when the template used intrinsics (`Fn::GetAtt`,
  `Ref`, `Fn::ImportValue`, `Fn::Join`). `resolve cfn` composes the final import
  ID from these.
- **pre-resolved import ID** for the 4 AWS-lookup-only types (IAM Policy ARN via
  `ListPolicies`, SG rule `sgr-*` via `DescribeSecurityGroupRules`, EIP allocation
  ID, VPC gateway attachment) — because these need a live AWS call, they are
  resolved here where AWS access lives, not in `resolve cfn`. This is the crux the
  field report flagged: `PhysicalResourceId` is frequently not the import ID.
- a clean derived name (e.g. full API path names)
- **`cdkHashedName`** flag — detects construct-hash suffixes (e.g.
  `...DefaultPolicyDFEB0894`, `FFSStacktokenauthorizer97C609E5`) → skill routes to
  config
- **`serverAssigned`** flag — CFN-generated names with random suffixes
  (`...ServiceRole-xQMUV6Ikl78Y`) → skill leaves name unset, import preserves it

Secret handling: resolve `NoEcho` parameters and known-sensitive attributes to
`(sensitive)` and set as encrypted Pulumi config secrets — same mechanism and
`--project-dir` wiring as `tf-digest`. Redaction caveat carried over: inline
assets / large attributes may embed secrets in non-sensitive string values;
`.gitignore` the digest.

**Source of truth:** deployed stack. `--synth-template` is an optional cross-check
only.

### `resolve cfn` (the `resolve tf` / `import-id-match` analog, shared translate core)

```
pulumi plugin run terraform-migrate -- resolve cfn \
  --digest /tmp/ffs-cfn-digest.json \
  --import-file imports.json \
  --mapping-file mappings.yaml \
  --provider classic \        # or native, per-node
  --out imports-ready.json
```

- Matches each import-skeleton entry to a digest resource by CFN logical ID
  (entry name suffix or explicit mapping), then fills its import ID.
- Composes the ID via the **shared `pkg/importid` core** — one table keyed on the
  Pulumi type token (`aws:lambda/permission:Permission` → `function/statement`),
  the same table `resolve tf` uses. The CFN attribute adapter supplies role values
  from CFN property names (`FunctionName`, `Id`, …); the TF adapter supplies them
  from TF attribute names. Covers:
  - **classic composite IDs** everywhere (Lambda Permission `FunctionName/Id`, ECS
    Service `cluster/name`, Route53, autoscaling reorder, etc.)
  - **aws-native IDs** for the bounded API Gateway family, including the Cloud
    Control identifier-order quirks: `Resource`/`Method`/`Stage` use `RestApiId|...`
    first; **`Deployment` is reversed** (`DeploymentId|RestApiId`).
  - **pre-resolved lookup IDs** (IAM policy ARN, SG rule IDs, EIP allocation IDs,
    VPC gateway attachment) — these came from `digest cfn` (they need live AWS);
    `resolve cfn` uses the stored value.
- **Extension mechanism (documented in the skill):** one keyed entry in the shared
  spec table per new resource type (+ a role→attribute mapping in each source
  adapter). When an import fails with "resource does not exist," treat it as
  "check the identifier format/order," not "resource missing."

### Reused unchanged

- **`patch-state`** + **`aws-import-diff-fields.json`** — extended (see Component 5)
  with the aws-native/Cloud Control diff cases. The falsy-default suppression keyed
  on provider version already exists and applies.
- **`set-secrets`** — for values not auto-discovered.

## Component 2 — Skill `cdk-cloudformation-workspace-migration`

Same six-phase spine as the TF skill, plus a Phase 0.

| Phase | Status | Content |
|---|---|---|
| **0. Posture & prerequisites** | new | Confirm classic/hybrid posture; **import into the latest aws-classic provider** (hard requirement — the phantom-diff fix); toolchain checklist: CodeArtifact `login` for private npm, CDK CLI install against public registry, Docker-for-synth caveat, native `dotnet lambda package` for .NET assets |
| **1. Analyze** | new | Run `cfn-digest` against the deployed stack; read the digest (never the raw stack); map CDK context → stack config (the direct analog of TF variables); map constructs → components; classify names via `cdkHashedName` / `serverAssigned` |
| **2. Init project** | verbatim | `pulumi new typescript`; set `aws:region` + `aws-native:region`; app config from extracted CDK context |
| **3. Incremental zero-diff loop** | verbatim (the gold) | Node-by-node in dependency order (e.g. ECS → Lambdas → API Gateway → migration), `import` → `preview --diff` → zero-diff gate; extended diff taxonomy |
| **4. Import** | new mechanics | `import-id-resolve` → `imports-ready.json`; `pulumi import --file ... --generate-code=false --protect=false --yes`; per-node provider switch to aws-native for API Gateway; validate the import file before running (one bad ID aborts the whole batch) |
| **5. Config/secrets** | mostly verbatim | `NoEcho` params + sensitive attrs → config secrets (auto via `cfn-digest`); non-secret CDK context → config |
| **6. Verify + PR** | verbatim | Clean preview, value tracing, PR |

### `references/cdk-gotchas.md` (codifies field report §5)

- **Hashed CDK names → config** (§5.1): inline-policy names, ECS task-def `Family`,
  authorizer name — vary per env, force replacement. Route to stack config.
- **Auto-named resources → leave unset** (§5.2): server-assigned names; import
  preserves the Computed name.
- **ECS TaskDefinition** (§5.3): emit container env vars **name-sorted**; emit the
  **fully-expanded** container definition (all AWS default empty arrays).
- **CloudWatch LogGroup ARN `:*` suffix** (§5.4): append `:*` in IAM policies
  referencing the log group ARN.
- **Lambda code write-only diff** (§5.5): unavoidable `+ code / - lastModified /
  + publish` until first `up`. Reference the artifact via a **project-relative
  `FileArchive` path**, never absolute (`path.resolve`/`__dirname`) — an absolute
  path baked into a GHA previously broke CI. Build .NET zips with native `dotnet
  lambda package` into a gitignored relative `artifacts/`.
- **API Gateway → aws-native** (§5.6): classic explodes (1 CFN Method → 4+ classic
  resources; 84 CFN → ~230 classic). aws-native is 1:1. Set every default the
  aws-native RestApi populates (`apiKeySourceType`, `disableExecuteApiEndpoint`,
  `endpointConfiguration`, `securityPolicy`) + `ignoreChanges:["tags"]` to make it a
  strict no-op (avoids the computed-output cascade replace). `ignoreChanges:["tags"]`
  on RestApi and Stage for CFN-injected `aws:cloudformation:*` tags. Integration
  defaults must be set (`cacheNamespace`, `passthroughBehavior:"WHEN_NO_MATCH"`,
  `responseTransferMode:"BUFFERED"`, `timeoutInMillis:29000`); proxy methods add
  `apiKeyRequired:false`; Stage needs `cacheClusterEnabled:false` + methodSettings
  defaults; Authorizer needs `authType:"custom"` + `providerArns:[]`.
- **Lambda Permission source ARNs** (§5.7): star-ify path params (`{userId}` → `*`)
  in the execute-api SourceArn. Unset `statementId` preserves the imported Sid.
- **Singleton/shared resources — don't manage** (§5.8): `AWS::ApiGateway::Account`
  is a region-wide singleton; leave it unmanaged rather than clobber it.
- **CDK custom resources / provider framework → native Pulumi** (§5.9): inspect what
  the handler actually does — it may not need the CFN protocol at all. Drop the CDK
  framework resources (unmanaged, cleaned up with the old stack); replace a
  migration custom resource with a single `aws.lambda.Invocation`.
- **Import mechanics** (§5.10): `--protect=false` (avoid cosmetic `~protect` diff);
  validate the import file (one malformed ID aborts everything); aws-native RestApi
  import emits a harmless write-only-properties warning.

## Component 3 — Skill `cdk-construct-to-component`

A thin wrapper that references `pulumi-terraform-module-to-component` for all shared
guidance (TS class structure, `Input<T>` vs plain, array types,
`getPolicyDocumentOutput`, packaging, `PulumiPlugin.yaml`, smoke tests, publishing).
New content only:

- **Construct → component interface**: L2/L3 constructs map to components like TF
  modules do; construct props → `Args`; construct outputs → component outputs. Use
  the digest's derived interface; include only inputs callers actually use.
- **CDK-name handling**: settable hashed names → `Args` sourced from config;
  server-assigned names → leave unset (import preserves).
- **Asset/bundling story**: relative `FileArchive` paths (never absolute — the
  CI-breaking lesson); native `dotnet lambda package` vs Docker.
- **Logical-name suffix rule**: child logical-name suffix = the CFN **logical ID**
  (the `import-id-resolve` matcher keys on it) — the analog of the TF-resource-name
  suffix rule.

## Component 4 — Extend `aws-import-diff-fields.json`

Add the aws-native / Cloud Control diff cases the field report surfaced, as new
categories consumed unchanged by `patch-state`:

- **`computed_output_cascade`** — aws-native RestApi update marks `rootResourceId`
  unknown, cascading replace down the tree. Mitigation is program-side (no-op
  RestApi), documented in the gotchas; the classification helps the diff taxonomy.
- **`cfn_injected_tags`** — `aws:cloudformation:logical-id/stack-id/stack-name` tags
  on taggable resources → `ignoreChanges:["tags"]`.
- **`provider_populated_defaults`** — integration `cacheNamespace`,
  `passthroughBehavior`, `responseTransferMode`, `timeoutInMillis`; Stage cache /
  throttling defaults.

## Build sequence & testing

1. **Tool first** (`cfn-digest` + `import-id-resolve`), tested against a real
   fixture stack via golden tests (mirrors this repo's existing
   `test/testdata/*.golden` pattern).
2. **Migration skill**, validated by re-running the FaceFinderService migration from
   the field report — we have a known-good end state (178/183 imported at zero diff)
   to check against.
3. **Component skill** last (lightest, most reuse).

## Non-goals / deferred

- **C# program output** — TypeScript only for v1; C# is a future extension for
  .NET shops that want to reuse project code.
- **All-in aws-native fast-path** — explicitly not this asset's posture; if a future
  engagement wants lift-and-shift speed, wrap Neo/cf2pulumi/cdk-importer separately.
- **Native resolvers beyond the API Gateway family** — route to existing Pulumi
  tooling rather than maintaining our own.
- **Repo rename** — keep `pulumi-tool-terraform-migrate` for now.
- **First `pulumi up`** and CI workflow swap — downstream of migration, out of scope
  for the tool/skill design.

## Open questions

None blocking. The FaceFinderService stack is the reference fixture; if it's not
readily re-deployable for golden tests, we substitute a smaller synthetic CDK stack
exercising the same resource-type families (Lambda, IAM, ECS, API Gateway, custom
resource).
