# Alias Wiring Pattern

Migration aliases connect old flat URNs (from Terraform import) to new child resource names inside components. This is done externally via transforms so component classes stay clean.

## migration-aliases.json

Keys are NEW child resource names (as created inside the component). Values are OLD flat URNs from imported state.

```json
{
  "vpc-main-vpc": "urn:pulumi:dev::project::aws:ec2/vpc:Vpc::vpc_main",
  "vpc-subnet-0": "urn:pulumi:dev::project::aws:ec2/subnet:Subnet::vpc_subnets_0"
}
```

## TypeScript

```typescript
import * as pulumi from "@pulumi/pulumi";
import aliasMap from "./migration-aliases.json";
import { Vpc } from "./components/vpc";

const vpc = new Vpc("vpc", { ...inputs }, {
  transformations: [(args) => {
    const oldUrn = aliasMap[args.name];
    if (oldUrn) {
      args.opts.aliases = [...(args.opts.aliases || []), { urn: oldUrn }];
    }
    return args;
  }],
});
```

Ensure `tsconfig.json` has `"resolveJsonModule": true`.

## Python

```python
import json
import pulumi
from components.vpc import Vpc

with open("migration-aliases.json") as f:
    alias_map = json.load(f)

def migration_transform(args):
    old_urn = alias_map.get(args.name)
    if old_urn:
        args.opts.aliases = (args.opts.aliases or []) + [pulumi.Alias(urn=old_urn)]
    return args

vpc = Vpc("vpc", inputs, opts=pulumi.ResourceOptions(
    transformations=[migration_transform],
))
```

## Post-migration cleanup

1. Run `pulumi up` with transforms in place — state adopts new component URNs
2. Delete `migration-aliases.json`
3. Remove `transformations` from all component instantiations
4. Run `pulumi preview` — must show zero changes
