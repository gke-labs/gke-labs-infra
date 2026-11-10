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

// Package kubectl provides the tool specification and embedded versions for kubectl.
package kubectl

import (
	_ "embed"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/spec"
)

//go:embed versions.yaml
var versionsYAML []byte

// Spec is the tool specification for kubectl.
var Spec = &spec.Spec{
	Name: "kubectl",
	Platforms: []string{
		"darwin-amd64",
		"darwin-arm64",
		"linux-amd64",
		"linux-arm64",
		"windows-amd64",
	},
	Source:       &spec.KubernetesRelease{},
	Archive:      spec.Bare{},
	VersionsYAML: versionsYAML,
}
