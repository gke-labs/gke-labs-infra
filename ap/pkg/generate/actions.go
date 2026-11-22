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

package generate

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed action.yml
var embeddedActionYAML []byte

// actionVersions holds the GitHub Actions references emitted into generated workflows.
type actionVersions struct {
	checkout       string
	setupGo        string
	uploadArtifact string
	cacheRestore   string
	cacheSave      string
}

var (
	actions = func() actionVersions {
		versions, err := parseActionVersions(embeddedActionYAML)
		if err != nil {
			panic(fmt.Sprintf("failed to parse embedded action.yml: %v", err))
		}
		return versions
	}()
	actionCheckout       = actions.checkout
	actionSetupGo        = actions.setupGo
	actionUploadArtifact = actions.uploadArtifact
	actionCacheRestore   = actions.cacheRestore
	actionCacheSave      = actions.cacheSave
)

// parseActionVersions parses action references from action.yml using plain string parsing.
func parseActionVersions(content []byte) (actionVersions, error) {
	var versions actionVersions
	for lineNum, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		var ref string
		if strings.HasPrefix(trimmed, "- uses:") {
			ref = strings.TrimSpace(strings.TrimPrefix(trimmed, "- uses:"))
		} else if strings.HasPrefix(trimmed, "uses:") {
			ref = strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:"))
		} else {
			continue
		}
		if ref == "" {
			return actionVersions{}, fmt.Errorf("line %d: empty uses reference", lineNum+1)
		}
		at := strings.Index(ref, "@")
		if at < 0 {
			return actionVersions{}, fmt.Errorf("line %d: action ref %q missing '@'", lineNum+1, ref)
		}
		actionName, rest := strings.TrimSpace(ref[:at]), ref[at+1:]
		sha, _, _ := strings.Cut(rest, " ")
		if len(sha) != 40 {
			return actionVersions{}, fmt.Errorf("line %d: action ref %q is not pinned to a full 40-char commit SHA (got %q)", lineNum+1, ref, sha)
		}
		if !strings.Contains(rest, "# ratchet:") {
			return actionVersions{}, fmt.Errorf("line %d: action ref %q missing ratchet comment", lineNum+1, ref)
		}
		switch actionName {
		case "actions/checkout":
			versions.checkout = ref
		case "actions/setup-go":
			versions.setupGo = ref
		case "actions/upload-artifact":
			versions.uploadArtifact = ref
		case "actions/cache/restore":
			versions.cacheRestore = ref
		case "actions/cache/save":
			versions.cacheSave = ref
		default:
			return actionVersions{}, fmt.Errorf("line %d: unknown action %q", lineNum+1, actionName)
		}
	}
	if versions.checkout == "" {
		return actionVersions{}, fmt.Errorf("missing action actions/checkout in action.yml")
	}
	if versions.setupGo == "" {
		return actionVersions{}, fmt.Errorf("missing action actions/setup-go in action.yml")
	}
	if versions.uploadArtifact == "" {
		return actionVersions{}, fmt.Errorf("missing action actions/upload-artifact in action.yml")
	}
	if versions.cacheRestore == "" {
		return actionVersions{}, fmt.Errorf("missing action actions/cache/restore in action.yml")
	}
	if versions.cacheSave == "" {
		return actionVersions{}, fmt.Errorf("missing action actions/cache/save in action.yml")
	}
	return versions, nil
}
