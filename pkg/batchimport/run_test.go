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

package batchimport

import (
	"context"
	"fmt"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFile builds an import file with one component and n bare resources named
// res-0..res-(n-1).
func testFile(n int) *ImportFile {
	f := &ImportFile{
		NameTable: map[string]string{"comp": "urn:pulumi:dev::proj::example:index:Comp::comp"},
		Resources: []*optimport.ImportResource{
			{Type: "example:index:Comp", Name: "comp", Component: true},
		},
	}
	for i := 0; i < n; i++ {
		f.Resources = append(f.Resources, &optimport.ImportResource{
			Type:   "aws:s3/bucket:Bucket",
			Name:   fmt.Sprintf("res-%d", i),
			ID:     fmt.Sprintf("id-%d", i),
			Parent: "comp",
		})
	}
	return f
}

func TestRun_CleanRun(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	assert.Len(t, res.Imported, 5)
	assert.Empty(t, res.Failed)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, 3, res.BatchCount)
	assert.Equal(t, 3, f.callCount, "one ImportBatch per batch, no isolation")
}

func TestRun_ComponentsInEveryBatch(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	_, err := Run(context.Background(), f, testFile(5), Options{BatchSize: 2, Resume: true})
	require.NoError(t, err)

	require.Len(t, f.payloads, 3)
	for i, p := range f.payloads {
		var components int
		for _, r := range p {
			if r.Component {
				components++
			}
		}
		assert.Equal(t, 1, components, "batch %d must carry every component entry", i)
	}
	assert.Equal(t, map[string]string{
		"comp": "urn:pulumi:dev::proj::example:index:Comp::comp",
	}, f.nameTable)
}

func TestRun_BatchSizeDefaults(t *testing.T) {
	t.Parallel()

	f := newFakeImporter()
	res, err := Run(context.Background(), f, testFile(3), Options{Resume: true})
	require.NoError(t, err)

	assert.Equal(t, 1, res.BatchCount, "3 resources fit one default-size batch")
	assert.Equal(t, 1, f.callCount)
}
