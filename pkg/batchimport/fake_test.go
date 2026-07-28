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
	"errors"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/optimport"
)

// fakeImporter simulates pulumi import against an in-memory state.
//
// failKeys are resources that never land. batchErr, when set, is returned from
// every ImportBatch call regardless of outcome — this models SDK v3.222.0
// returning "failed to read generated code" after a successful import.
type fakeImporter struct {
	state    map[ResourceKey]bool
	failKeys map[ResourceKey]bool
	batchErr error

	// recorded for assertions
	payloads  [][]*optimport.ImportResource
	nameTable map[string]string
	callCount int
}

func newFakeImporter() *fakeImporter {
	return &fakeImporter{
		state:    map[ResourceKey]bool{},
		failKeys: map[ResourceKey]bool{},
	}
}

func (f *fakeImporter) ImportBatch(
	_ context.Context,
	rs []*optimport.ImportResource,
	nameTable map[string]string,
) error {
	f.callCount++
	f.payloads = append(f.payloads, rs)
	f.nameTable = nameTable

	failed := false
	for _, r := range rs {
		if r.Component {
			continue
		}
		if f.failKeys[keyOf(r)] {
			failed = true
			continue
		}
		f.state[keyOf(r)] = true
	}
	if f.batchErr != nil {
		return f.batchErr
	}
	if failed {
		return errors.New("import failed: resource does not exist")
	}
	return nil
}

func (f *fakeImporter) ExistingResources(_ context.Context) (map[ResourceKey]bool, error) {
	out := make(map[ResourceKey]bool, len(f.state))
	for k, v := range f.state {
		out[k] = v
	}
	return out, nil
}

// nonComponentPayloads returns each recorded payload's non-component entries.
func (f *fakeImporter) nonComponentPayloads() [][]ResourceKey {
	var out [][]ResourceKey
	for _, p := range f.payloads {
		var keys []ResourceKey
		for _, r := range p {
			if !r.Component {
				keys = append(keys, keyOf(r))
			}
		}
		out = append(out, keys)
	}
	return out
}
