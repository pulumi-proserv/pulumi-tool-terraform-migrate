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

// Package batchimport orchestrates importing a prepared Pulumi import file in
// batches, isolating per-resource failures so a single bad import ID does not
// hide the outcome of the rest of the batch.
package batchimport

import "strings"

// ResourceKey identifies a resource independently of the stack and project
// segments of its URN. Resources are matched on type AND name: several Pulumi
// resources can legitimately share a name when they derive it from one source
// logical ID, so name alone conflates them.
type ResourceKey struct {
	Type string
	Name string
}

// ParseURN extracts the leaf type and name from a Pulumi URN of the form
// urn:pulumi:<stack>::<project>::<typePath>::<name>, where <typePath> is
// parentType$childType for parented resources. It reports false for a URN with
// too few segments.
func ParseURN(urn string) (ResourceKey, bool) {
	parts := strings.SplitN(urn, "::", 4)
	if len(parts) < 4 {
		return ResourceKey{}, false
	}
	typePath, name := parts[2], parts[3]
	if i := strings.LastIndex(typePath, "$"); i >= 0 {
		typePath = typePath[i+1:]
	}
	if typePath == "" || name == "" {
		return ResourceKey{}, false
	}
	return ResourceKey{Type: typePath, Name: name}, true
}
