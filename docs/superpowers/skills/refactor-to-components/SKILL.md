# Refactor to Components

Restructure flat Pulumi state (imported from Terraform) into component resources using `module-map.json` as the blueprint.

## Prerequisites

- `module-map.json` exists (produced by `pulumi-terraform-migrate module-map`)
- Pulumi state imported into stack (via `pulumi-terraform-migrate stack`)
- Target language chosen: TypeScript or Python

## Workflow

### Step 1: Load module-map.json

Parse the file and present an inventory table to the user:

| Module | Resources | Inputs | Outputs | Index Type |
|--------|-----------|--------|---------|------------|
| module.vpc | 12 | 5 | 3 | none |
| module.subnet[0] | 4 | 3 | 1 | count |

Use the schema documented in [references/module-map-format.md](references/module-map-format.md).

### Step 2: Component mapping review

Default mapping: 1:1 Terraform module to Pulumi component.

Offer the user these adjustments:
- **Merge modules** — combine multiple TF modules into one component
- **Keep flat** — skip componentization for a module, leave resources at root
- **Map to existing component** — use a published component (e.g., `@pulumi/awsx:ec2:Vpc`); see [references/existing-component-integration.md](references/existing-component-integration.md)
- **Move resources** — reassign specific URNs between groups

Maintain the working plan in conversation context. Do not write intermediate files.

### Step 3: Per-module generation loop

For each module in the approved plan:

1. **Propose** to the user:
   - Component class name and type token (e.g., `my:components:Vpc`)
   - Args interface derived from `interface.inputs`
   - Child resources (list URNs from `resources`)

2. **User approves or adjusts.**

3. **Generate component class:**
   - Subclass `ComponentResource` (TS) or `pulumi.ComponentResource` (Python)
   - Constructor accepts args, creates child resources, calls `registerOutputs`
   - Component class is clean — NO migration aliases or transforms inside it

4. **Checkpoint** — confirm with user before moving to next module.

For 15+ structurally similar modules (same source, same interface), offer **batch mode**: generate one template, apply to all instances.

### Step 4: Generate main program

- Read `evaluatedValue` from module-map inputs for concrete values
- Read `expression` to understand derivation — prefer variable references over hardcoded values where the expression references a `var.*` or another module output
- Instantiate each component with appropriate args
- Wire migration aliases using the transform pattern from [references/alias-wiring-pattern.md](references/alias-wiring-pattern.md)
- Generate `migration-aliases.json` mapping new child resource names to old flat URNs

### Step 5: Verification

Run this sequence with the user:

1. `pulumi preview` — expect zero changes (aliases resolve old URNs to new component children)
2. `pulumi up` — state updated with component hierarchy
3. Delete `migration-aliases.json` and remove transform code from main program
4. `pulumi preview` — still zero changes (state now reflects new URNs)

## Notes

- Reference the `pulumi-component` skill for component authoring patterns if available in the workspace.
- Do not embed code templates in this skill. The agent generates code from module-map data at runtime.
- Component classes must be migration-unaware. All alias wiring is external via transforms on the component instantiation.
