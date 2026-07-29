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

package tofu

import (
	"github.com/zclconf/go-cty/cty"
)

// SensitiveObjToCtyPath converts from the sensitive values object format stored in the Terraform state to a list of CTY paths for each sensitive value.
//
// The sensitive values object format is of this form:
//
//	{
//	  "path1": true,
//	  "path2": {
//	    "subpath1": true,
//	    "subpath2": true,
//	  },
//	  "path3": [
//	    {
//	      "subpath1": true,
//	      "subpath2": true,
//	    },
//	  ],
//	}
//
// Each value is either a boolean, a map or a list. A boolean value indicates if the value is sensitive or not and should be masked. A map or a list value indicates that the values is a map or list which might contain sensitive values.
func SensitiveObjToCtyPath(obj map[string]interface{}) []cty.Path {
	return sensitiveObjToCtyPathMap(cty.Path{}, obj)
}

func sensitiveObjToCtyPathMap(currentPath cty.Path, obj map[string]interface{}) []cty.Path {
	paths := []cty.Path{}
	for key, value := range obj {
		if value, ok := value.(bool); ok && value {
			paths = append(paths, currentPath.GetAttr(key))
		}
		if value, ok := value.(map[string]interface{}); ok {
			mapPaths := sensitiveObjToCtyPathMap(currentPath.GetAttr(key), value)
			paths = append(paths, mapPaths...)
		}
		if value, ok := value.([]interface{}); ok {
			subpaths := sensitiveListToCtyPathList(currentPath.GetAttr(key), value)
			paths = append(paths, subpaths...)
		}
	}
	return paths
}

func sensitiveListToCtyPathList(currentPath cty.Path, obj []interface{}) []cty.Path {
	paths := []cty.Path{}
	for idx, value := range obj {
		if value, ok := value.(bool); ok && value {
			paths = append(paths, currentPath.IndexInt(idx))
		}
		if value, ok := value.(map[string]interface{}); ok {
			subpaths := sensitiveObjToCtyPathMap(currentPath.IndexInt(idx), value)
			paths = append(paths, subpaths...)
		}
		if value, ok := value.([]interface{}); ok {
			subpaths := sensitiveListToCtyPathList(currentPath.IndexInt(idx), value)
			paths = append(paths, subpaths...)
		}
	}
	return paths
}
