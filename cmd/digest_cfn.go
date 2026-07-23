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
	"encoding/json"
	"fmt"
	"os"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/cfn"
	"github.com/spf13/cobra"
)

func newDigestCfnCmd() *cobra.Command {
	var stackName, region, out string
	cmd := &cobra.Command{
		Use:   "cfn",
		Short: "Digest a deployed CloudFormation stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sr, cc, lk, err := cfn.NewAWSClients(ctx, region)
			if err != nil {
				return fmt.Errorf("aws clients: %w", err)
			}
			digest, err := cfn.BuildDigest(ctx, stackName, region, sr, cc, lk)
			if err != nil {
				return fmt.Errorf("build digest: %w", err)
			}
			data, err := json.MarshalIndent(digest, "", "    ")
			if err != nil {
				return fmt.Errorf("marshal: %w", err)
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Resources: %d\nOutput written to %s\n", len(digest.Resources), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&stackName, "stack-name", "", "Deployed CloudFormation stack name")
	cmd.Flags().StringVar(&region, "region", "", "AWS region")
	cmd.Flags().StringVarP(&out, "out", "o", "", "Output digest JSON path")
	cmd.MarkFlagRequired("stack-name")
	cmd.MarkFlagRequired("region")
	cmd.MarkFlagRequired("out")
	return cmd
}
