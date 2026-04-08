# Context.Eval() Spike Findings

## Setup

- Fork: `github.com/pulumi/opentofu@v0.0.0-20250318202137-3146daceaf73` (branch `pulumi-main`)
- All needed packages exported at top level (not `internal/`)
- Requires HCL replace: `github.com/hashicorp/hcl/v2 => github.com/opentofu/hcl/v2 v2.20.2-0.20250121132637-504036cd70e7`

## API Signatures

### Config loading

```go
loader, err := configload.NewLoader(&configload.Config{
    ModulesDir: filepath.Join(tfDir, ".terraform/modules"),
})
config, diags := loader.LoadConfig(tfDir, configs.RootModuleCallForTesting())
// 2 args, no context. Module Dir paths in modules.json are relative to tfDir.
```

### State loading

```go
sf, err := statefile.Read(bytes.NewReader(data), encryption.StateEncryptionDisabled())
// sf.State is *states.State
```

### Provider loading

```go
providerDir := providercache.NewDir(filepath.Join(tfDir, ".terraform/providers"))
allProviders := providerDir.AllAvailablePackages()
// Returns map[addrs.Provider][]CachedProvider

// For each provider, create a Factory:
factories[provAddr] = func() (providers.Interface, error) {
    execFile, _ := cached.ExecutableFile()
    clientConfig := &goplugin.ClientConfig{
        HandshakeConfig:  tfplugin.Handshake,
        Logger:           logging.NewProviderLogger(""),
        AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
        Managed:          true,
        Cmd:              exec.Command(execFile),
        AutoMTLS:         true,
        VersionedPlugins: tfplugin.VersionedPlugins,
    }
    client := goplugin.NewClient(clientConfig)
    rpcClient, _ := client.Client()
    raw, _ := rpcClient.Dispense(tfplugin.ProviderPluginName)
    p := raw.(*plugin.GRPCProvider)
    p.PluginClient = client
    return p, nil
}
```

### Context.Eval()

```go
tofuCtx, diags := tofu.NewContext(&tofu.ContextOpts{
    Providers: factories,
})

// Per child module instance:
childAddr := addrs.RootModuleInstance.Child("pet", addrs.IntKey(0))
childScope, diags := tofuCtx.Eval(ctx, config, sf.State, childAddr, &tofu.EvalOpts{})

// Evaluate var.<name> in child scope:
expr, _ := hclsyntax.ParseExpression([]byte("var.prefix"), "<eval>", hcl.Pos{Line: 1, Column: 1})
val, diags := childScope.EvalExpr(expr, cty.DynamicPseudoType)
// val = cty.StringVal("test-0") for instance 0
```

## Key Finding: Evaluate var.* in child scope

**Do NOT evaluate call-site expressions in the parent scope.** The root scope cannot evaluate expressions with `count.index` because it's not in a counted context.

Instead: call `Eval()` per child module instance, then read `var.<name>` from the child scope. OpenTofu's graph already resolved the variable assignments.

```
Root scope:     prefix = "test-${count.index}"  → ERROR: count in non-counted context
Child[0] scope: var.prefix                      → "test-0"  ✓
Child[1] scope: var.prefix                      → "test-1"  ✓
```

## Call-site expressions (for `expression` field)

Call-site expression text can still be obtained from `call.Config.JustAttributes()` for the `expression` field in module-map.json. This is pure config parsing, no evaluation needed.

## tfvars

`EvalOpts{}` with no explicit `SetVariables` works — OpenTofu auto-loads `terraform.tfvars` from the config directory.
