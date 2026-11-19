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

package e2e

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

const (
	recommendedMaxUserInstances = 512
	recommendedMaxUserWatches   = 524288
)

// warnInotifyLimits reads inotify limits directly from /proc on Linux and warns
// if they are below kind's recommended thresholds.
func warnInotifyLimits(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}

	instances, err := readProcInt("/proc/sys/fs/inotify/max_user_instances")
	if err != nil {
		scoped.Log(ctx).Fine("failed to read /proc/sys/fs/inotify/max_user_instances", "error", err)
	}

	watches, err := readProcInt("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		scoped.Log(ctx).Fine("failed to read /proc/sys/fs/inotify/max_user_watches", "error", err)
	}

	checkInotifyLimits(ctx, instances, watches)
}

func readProcInt(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1, err
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1, err
	}
	return val, nil
}

// checkInotifyLimits checks if the given inotify instances and watches limits
// meet kind's recommended thresholds and logs warnings if they do not.
func checkInotifyLimits(ctx context.Context, instances, watches int) {
	if instances >= 0 && instances < recommendedMaxUserInstances {
		scoped.Log(ctx).Warn("inotify limit fs.inotify.max_user_instances is below kind's recommended value; a kind node's kubelet may restart in a loop while other nodes are Ready; raise it with: sudo sysctl -w fs.inotify.max_user_instances=512",
			"instances", instances, "recommended", recommendedMaxUserInstances)
	}
	if watches >= 0 && watches < recommendedMaxUserWatches {
		scoped.Log(ctx).Warn("inotify limit fs.inotify.max_user_watches is below kind's recommended value; a kind node's kubelet may restart in a loop while other nodes are Ready; raise it with: sudo sysctl -w fs.inotify.max_user_watches=524288",
			"watches", watches, "recommended", recommendedMaxUserWatches)
	}
}
