package cmd

import "github.com/spf13/cobra"

// newDigestTfCmd is the `digest tf` subcommand — same body as the hidden
// tf-digest alias, but visible under the `digest` parent with Use "tf".
func newDigestTfCmd() *cobra.Command { return buildTfDigestCommand("tf", false) }
