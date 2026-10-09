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

package runnerpool

import (
	"fmt"
	"io"
	"time"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	"sigs.k8s.io/yaml"
)

// PrintSandboxTemplate writes the Sandbox (and the Secret it references) that
// the pool would create for one runner, as a YAML stream. It is meant for
// inspection and for validating the template against a cluster with
// "kubectl apply --dry-run=server".
func PrintSandboxTemplate(w io.Writer, opts Options, namespace string) error {
	name := opts.Name + "-example"

	secret := buildSecret(opts.Name, name, "<generated per runner>")
	secret.APIVersion = "v1"
	secret.Kind = "Secret"
	secret.Namespace = namespace

	sb := buildSandbox(opts.Name, name, opts.Runner, time.Now())
	sb.APIVersion = sandboxv1beta1.GroupVersion.String()
	sb.Kind = "Sandbox"
	sb.Namespace = namespace
	sb.Annotations = map[string]string{RunnerIDAnnotation: "0"}

	for _, obj := range []any{secret, sb} {
		b, err := yaml.Marshal(obj)
		if err != nil {
			return fmt.Errorf("marshalling template: %w", err)
		}
		if _, err := fmt.Fprintf(w, "---\n%s", b); err != nil {
			return err
		}
	}
	return nil
}
