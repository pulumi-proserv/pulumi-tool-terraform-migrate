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
	"testing"

	"github.com/pulumi/pulumi-tool-terraform-migrate/pkg/batchimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatResult_Success(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Imported: []batchimport.ResourceKey{
			{Type: "aws:s3/bucket:Bucket", Name: "b1"},
			{Type: "aws:ec2/vpc:Vpc", Name: "vpc"},
		},
		Skipped:    []batchimport.ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "b0"}},
		BatchCount: 1,
	}, false)

	assert.Contains(t, out, "Imported: 2")
	assert.Contains(t, out, "Skipped:  1")
	assert.Contains(t, out, "Failed:   0")
	assert.NotContains(t, out, "FAILED RESOURCES")
}

func TestFormatResult_Failures(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Imported: []batchimport.ResourceKey{{Type: "aws:ec2/vpc:Vpc", Name: "vpc"}},
		Failed: []batchimport.Failure{
			{
				Key: batchimport.ResourceKey{Type: "aws:s3/bucket:Bucket", Name: "b1"},
				ID:  "bad-id",
				Err: "resource does not exist",
			},
			{
				Key: batchimport.ResourceKey{Type: "aws:lambda/function:Function", Name: "fn2"},
				ID:  "arn:aws:lambda:us-west-2:123456789:function:invalid",
				Err: "InvalidParameterValueException: The role defined for the function cannot be assumed by Lambda.",
			},
		},
		BatchCount: 1,
	}, false)

	assert.Contains(t, out, "Failed:   2")
	assert.Contains(t, out, "FAILED RESOURCES")
	// First failure assertions
	assert.Contains(t, out, "b1")
	assert.Contains(t, out, "aws:s3/bucket:Bucket")
	assert.Contains(t, out, "bad-id")
	assert.Contains(t, out, "resource does not exist")
	// Second failure assertions
	assert.Contains(t, out, "fn2")
	assert.Contains(t, out, "aws:lambda/function:Function")
	assert.Contains(t, out, "arn:aws:lambda:us-west-2:123456789:function:invalid")
	assert.Contains(t, out, "InvalidParameterValueException")
}

func TestFormatResult_DryRun(t *testing.T) {
	t.Parallel()

	out := formatResult(&batchimport.Result{
		Planned:    []batchimport.ResourceKey{{Type: "aws:ec2/vpc:Vpc", Name: "vpc"}},
		Skipped:    []batchimport.ResourceKey{{Type: "aws:s3/bucket:Bucket", Name: "b0"}},
		BatchCount: 1,
	}, true)

	assert.Contains(t, out, "Dry run")
	assert.Contains(t, out, "Would import: 1")
	assert.Contains(t, out, "Batches:      1")
	assert.NotContains(t, out, "Imported:")
}

func TestNewImportCmd_Flags(t *testing.T) {
	t.Parallel()

	cmd := newImportCmd()
	require.NotNil(t, cmd.Flags().Lookup("file"))
	require.NotNil(t, cmd.Flags().Lookup("project-dir"))
	require.NotNil(t, cmd.Flags().Lookup("stack"))
	require.NotNil(t, cmd.Flags().Lookup("batch-size"))
	require.NotNil(t, cmd.Flags().Lookup("no-resume"))
	require.NotNil(t, cmd.Flags().Lookup("dry-run"))
	assert.Equal(t, "import", cmd.Use)
}
