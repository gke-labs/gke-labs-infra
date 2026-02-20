// Copyright 2026 Google LLC
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

package manifests

import (
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Object represents a single YAML document.
type Object struct {
	Node *yaml.Node
}

// Parse parses a multi-document YAML file.
func Parse(r io.Reader) ([]*Object, error) {
	dec := yaml.NewDecoder(r)
	var objects []*Object
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// yaml.v3 Decode of a document usually gives a DocumentNode
		objects = append(objects, &Object{Node: &node})
	}
	return objects, nil
}

// GetString returns the string value at the given path (e.g., "spec.updateStrategy.type").
func (o *Object) GetString(path string) (string, bool, error) {
	node, err := o.findNode(path)
	if err != nil {
		return "", false, err
	}
	if node == nil {
		return "", false, nil
	}
	if node.Kind != yaml.ScalarNode {
		return "", false, fmt.Errorf("node at path %q is not a scalar", path)
	}
	return node.Value, true, nil
}

func (o *Object) findNode(path string) (*yaml.Node, error) {
	parts := strings.Split(path, ".")
	curr := o.Node
	// If it's a DocumentNode, move to its first child (the actual content)
	if curr.Kind == yaml.DocumentNode && len(curr.Content) > 0 {
		curr = curr.Content[0]
	}

	for _, part := range parts {
		if curr.Kind != yaml.MappingNode {
			return nil, nil // or error? usually nil if path doesn't exist
		}
		found := false
		for i := 0; i < len(curr.Content); i += 2 {
			keyNode := curr.Content[i]
			if keyNode.Value == part {
				curr = curr.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil, nil
		}
	}
	return curr, nil
}

// GetLine returns the line number for the node at the given path.
func (o *Object) GetLine(path string) (int, error) {
	node, err := o.findNode(path)
	if err != nil {
		return 0, err
	}
	if node == nil {
		return 0, fmt.Errorf("path %q not found", path)
	}
	return node.Line, nil
}

// Kind returns the Kind of the object.
func (o *Object) Kind() (string, bool, error) {
	return o.GetString("kind")
}

// ApiVersion returns the apiVersion of the object.
func (o *Object) ApiVersion() (string, bool, error) {
	return o.GetString("apiVersion")
}
