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

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"gopkg.in/yaml.v3"
)

var serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// IsInCluster returns true if the current process is running inside a Kubernetes cluster.
func IsInCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return false
	}
	exists, err := fileutils.FileExists(serviceAccountTokenPath)
	if err != nil {
		scoped.Log(context.Background()).Warn("failed to check for service account token", "path", serviceAccountTokenPath, "error", err)
		return false
	}
	return exists
}

func decodeYAMLDocuments(content string) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to decode YAML: %w", err)
		}
		docs = append(docs, &doc)
	}
	return docs, nil
}

func encodeYAMLDocuments(docs []*yaml.Node) (string, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return "", fmt.Errorf("failed to encode YAML: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("failed to close YAML encoder: %w", err)
	}
	return buffer.String(), nil
}
