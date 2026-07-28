# Draft upstream issue for pulumi/pulumi

Not filed yet. Review, then file with:

```bash
gh issue create --repo pulumi/pulumi \
  --title "auto: ImportResources returns an error after a successful import when GenerateCode(false) is set" \
  --body-file docs/upstream-issue-importresources-generated-code.md
```

(Delete this header block before filing.)

---

### What happened?

`auto.Stack.ImportResources` returns a non-nil error after an import that
**fully succeeded**, whenever `optimport.GenerateCode(false)` is passed.

The method builds a path for the generated-code file in a temp dir, passes
`--out` to the CLI **only when code generation is enabled**, and then reads that
file **unconditionally**:

```go
// sdk/go/auto/stack.go, ImportResources
generatedCodePath := filepath.Join(tempDir, "generated_code.txt")
if importOpts.GenerateCode != nil && !*importOpts.GenerateCode {
    args = append(args, "--generate-code=false")   // note: no --out
} else {
    args = append(args, "--out", generatedCodePath)
}

// ... runs the CLI, which succeeds ...

generatedCode, err := os.ReadFile(generatedCodePath)   // ENOENT
if err != nil {
    return res, fmt.Errorf("failed to read generated code: %w", err)
}
```

With `--generate-code=false`, `--out` is never passed, so the CLI never writes
`generated_code.txt`, so the unconditional `os.ReadFile` fails with ENOENT and
the successful import is reported as an error.

This makes the returned error unusable as a success signal for any caller that
disables code generation — which is the normal configuration for importing into
a hand-authored program.

### Example

```go
res := []*optimport.ImportResource{
    {Type: "random:index/randomString:RandomString", Name: "repro-one", ID: "reprooo1"},
}

_, importErr := s.ImportResources(ctx,
    optimport.Resources(res),
    optimport.Protect(false),
    optimport.GenerateCode(false),
)
fmt.Printf("ImportResources returned err: %v\n", importErr != nil)
if importErr != nil {
    fmt.Printf("error text: %v\n", importErr)
}

dep, _ := s.Export(ctx)
// ... check whether repro-one is present in dep.Deployment ...
```

Output:

```
=== ImportResources returned err: true
=== error text: failed to read generated code: open /var/folders/.../T/pulumi-import-3373502828/generated_code.txt: no such file or directory
=== resource IS in state afterwards: true
```

The resource imported successfully and is present in stack state; the method
still returned an error.

### Output of `pulumi about`

Reproduced with:

- pulumi CLI 3.242.0, darwin/arm64
- `github.com/pulumi/pulumi/sdk/v3` v3.222.0
- Go 1.25.0
- Backend: local file backend (`PULUMI_BACKEND_URL=file://...`); not
  backend-specific
- Provider: `random` (nothing provider-specific about it)

The same unconditional read is present in the SDK at v3.222.0, v3.233.0, and
v3.246.0, so this is not fixed on more recent versions.

### Additional information

**Suggested fix** — read the file only when code generation was actually
requested:

```go
var generatedCode []byte
if importOpts.GenerateCode == nil || *importOpts.GenerateCode {
    generatedCode, err = os.ReadFile(generatedCodePath)
    if err != nil {
        return res, fmt.Errorf("failed to read generated code: %w", err)
    }
}
```

**Impact.** Callers that disable code generation cannot distinguish a real
import failure from this artifact without parsing the error string. A caller
that treats the error as authoritative will report every successful import as a
failure.

**Workaround.** Ignore the returned error as a verdict and determine the outcome
from stack state instead: call `Stack.Export` afterwards and check whether each
resource's URN is present. That is what we do — it also happens to be robust
against unrelated cosmetic errors, but it costs an extra state read per batch
and is not obvious to someone reading the API.
