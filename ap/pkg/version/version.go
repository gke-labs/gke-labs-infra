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

package version

import (
	"context"
	"fmt"
	"runtime/debug"
)

// Run prints version information
func Run(ctx context.Context, root string) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("failed to read build info")
	}

	fmt.Printf("Module: %s\n", info.Main.Path)
	if info.Main.Version != "" {
		fmt.Printf("Version: %s\n", info.Main.Version)
	}

	var revision string
	var modified bool

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision != "" {
		fmt.Printf("Git SHA: %s", revision)
		if modified {
			fmt.Printf(" (modified)")
		}
		fmt.Println()
	} else {
		fmt.Println("Git SHA: unknown")
	}

	return nil
}
