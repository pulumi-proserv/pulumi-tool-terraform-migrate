// Copyright 2016-2025, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/batchimport"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var filePath string
	var projectDir string
	var stack string
	var batchSize int
	var noResume bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a prepared import file in batches, isolating failures",
		Long: `Import the resources in a prepared Pulumi import file, in batches, and
report every resource that failed.

A single malformed import ID aborts a whole "pulumi import" batch. When a batch
does not fully land, this command re-imports that batch's resources one at a
time to identify exactly which ones failed, records them, and carries on. One
run therefore surfaces every bad import ID instead of one per run.

Whether a resource imported is determined by reading stack state afterwards, not
by the importer's exit status. Resources already present in state are skipped, so
a run can be repeated after fixing the reported IDs; pass --no-resume to disable.

Every component entry is included in every batch so child resources can resolve
their parent through the nameTable.

Resources are always imported unprotected and without code generation, matching
the hand-authored migration workflow.

Cloud credentials come from the environment. Wrap the command if you use ESC:

  pulumi env run <esc-env> -- pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev

Examples:

  # Inspect the plan without importing
  pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev --dry-run

  # Import, 50 resources per batch
  pulumi-terraform-migrate import \
    --file imports-ready.json --project-dir . --stack dev --batch-size 50
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			file, err := batchimport.LoadImportFile(filePath)
			if err != nil {
				return err
			}

			imp, err := batchimport.NewStackImporter(ctx, projectDir, stack)
			if err != nil {
				return err
			}

			res, err := batchimport.Run(ctx, imp, file, batchimport.Options{
				BatchSize: batchSize,
				Resume:    !noResume,
				DryRun:    dryRun,
				Progress:  cmd.ErrOrStderr(),
			})
			if err != nil {
				if res != nil {
					// Show the operator what was identified before the
					// failure; still return the error so the exit code is
					// non-zero.
					_, _ = fmt.Fprint(cmd.OutOrStdout(), formatResult(res, dryRun))
				}
				return err
			}

			_, _ = fmt.Fprint(cmd.OutOrStdout(), formatResult(res, dryRun))

			if len(res.Failed) > 0 {
				// The printed report is the useful output, so suppress cobra's
				// error and usage noise; Execute() still exits non-zero.
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				return fmt.Errorf("%d resource(s) failed to import", len(res.Failed))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&filePath, "file", "", "Prepared import file (from the resolve or import-id-match command)")
	cmd.Flags().StringVar(&projectDir, "project-dir", ".", "Pulumi project directory")
	cmd.Flags().StringVar(&stack, "stack", "", "Pulumi stack name")
	cmd.Flags().IntVar(&batchSize, "batch-size", batchimport.DefaultBatchSize, "Resources per batch")
	cmd.Flags().BoolVar(&noResume, "no-resume", false, "Import resources even if already in stack state")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the batch plan without importing")

	cmd.MarkFlagRequired("file")
	cmd.MarkFlagRequired("stack")

	return cmd
}

// formatResult renders the end-of-run summary.
func formatResult(res *batchimport.Result, dryRun bool) string {
	var b strings.Builder

	if dryRun {
		fmt.Fprintf(&b, "\nDry run — nothing imported.\n")
		fmt.Fprintf(&b, "  Would import: %d\n", len(res.Planned))
		fmt.Fprintf(&b, "  Skipped:      %d (already in state)\n", len(res.Skipped))
		fmt.Fprintf(&b, "  Batches:      %d\n", res.BatchCount)
		return b.String()
	}

	fmt.Fprintf(&b, "\nImport summary (%d batches)\n", res.BatchCount)
	fmt.Fprintf(&b, "  Imported: %d\n", len(res.Imported))
	fmt.Fprintf(&b, "  Skipped:  %d (already in state)\n", len(res.Skipped))
	fmt.Fprintf(&b, "  Failed:   %d\n", len(res.Failed))

	if len(res.Failed) > 0 {
		fmt.Fprintf(&b, "\nFAILED RESOURCES\n")
		for _, f := range res.Failed {
			fmt.Fprintf(&b, "  %s (%s)\n", f.Key.Name, f.Key.Type)
			fmt.Fprintf(&b, "    id:    %s\n", f.ID)
			fmt.Fprintf(&b, "    error: %s\n", strings.TrimSpace(f.Err))
		}
		fmt.Fprintf(&b, "\nFix the import IDs above and re-run; imported resources are skipped.\n")
	}

	return b.String()
}

func init() { rootCmd.AddCommand(newImportCmd()) }
