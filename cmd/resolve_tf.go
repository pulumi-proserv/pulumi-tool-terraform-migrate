package cmd

import "github.com/spf13/cobra"

// newResolveTfCmd is the `resolve tf` subcommand — same body as the hidden
// import-id-match alias, visible under `resolve` with Use "tf".
func newResolveTfCmd() *cobra.Command { return buildImportIDMatchCommand("tf", false) }
