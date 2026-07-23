package cmd

import "github.com/spf13/cobra"

func newResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Fill a Pulumi import file from a Terraform (tf) or CloudFormation (cfn) digest",
	}
	cmd.AddCommand(newResolveTfCmd())
	cmd.AddCommand(newResolveCfnCmd())
	return cmd
}

func init() { rootCmd.AddCommand(newResolveCmd()) }
