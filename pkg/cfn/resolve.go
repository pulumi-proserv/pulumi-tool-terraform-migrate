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
