package cmd

import "github.com/spf13/cobra"

func newDigestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Produce an agent-safe digest of Terraform state (tf) or a deployed CloudFormation stack (cfn)",
	}
	cmd.AddCommand(newDigestTfCmd())
	cmd.AddCommand(newDigestCfnCmd())
	return cmd
}

func init() { rootCmd.AddCommand(newDigestCmd()) }
