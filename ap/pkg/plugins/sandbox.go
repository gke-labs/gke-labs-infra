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

package plugins

import (
	"context"

	"github.com/gke-labs/clix/sandbox"
)

// SelectSandboxBackend returns the first usable sandbox backend on the current host
// through the clix sandbox library, evaluating backends in fixed order:
// docker, apple-container, chroot, and proot.
func SelectSandboxBackend(ctx context.Context, options ...sandbox.Option) (sandbox.Sandbox, error) {
	return sandbox.FirstUsable(ctx, options...)
}
