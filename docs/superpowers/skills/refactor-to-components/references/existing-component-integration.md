# Existing Component Integration

How to map a Terraform module to an existing published Pulumi component instead of generating a new class.

## When to use

When a TF module's resources and interface closely match a published component (e.g., `@pulumi/awsx:ec2:Vpc`, `@pulumi/awsx:ecs:FargateService`), use the existing component directly instead of generating a new one.

## Matching process

### 1. Resource type matching

Compare URN resource types in the module's `resources` array against the child resources the published component creates.

- Extract the type token from each URN (e.g., `aws:ec2/vpc:Vpc` from the full URN)
- Compare against the component's known child resource types
- A good match: most resource types in the module appear as children of the component
- Missing resources in the component are a warning, not a blocker

### 2. Interface matching

Compare module `interface.inputs` against the component's args type:

- Match by name (accounting for casing conventions: `cidr_block` vs `cidrBlock`)
- Match by type where possible
- Required inputs in the module that have no corresponding arg are a warning

### 3. Assessment output

Present to user:

| Check | Status |
|-------|--------|
| Resource coverage | 10/12 types matched |
| Missing from component | `aws:ec2/routeTableAssociation:RouteTableAssociation` (2 instances) |
| Input coverage | 4/5 inputs matched |
| Unmapped input | `enable_dns_hostnames` (no equivalent arg) |

Warn on mismatches but do not block. The user decides whether the match is close enough.

## Generation

When the user approves:

1. **No new class generated** — use the published component directly
2. Generate instantiation code with args mapped from module inputs
3. Resources not covered by the component remain at root level (or in a small wrapper)
4. Alias wiring is still required via transforms — the component creates children with different names than the flat imported state

## Alias considerations

Published components name their child resources internally. The `migration-aliases.json` must map those internal names to the old flat URNs. To determine internal names:

- Check the component's source code if available
- Or do a test instantiation with `pulumi preview --diff` to see what names the component creates
- Map each created child name to the corresponding old URN from module-map `resources`

## Example

Mapping `module.vpc` to `@pulumi/awsx:ec2:Vpc`:

```json
{
  "vpc-vpc": "urn:pulumi:dev::project::aws:ec2/vpc:Vpc::vpc_main",
  "vpc-subnet-0": "urn:pulumi:dev::project::aws:ec2/subnet:Subnet::vpc_subnets_0"
}
```

The left-hand names come from what `awsx.ec2.Vpc` creates internally. The right-hand URNs come from `module-map.json` resources.
