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
	"os"
	"runtime"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

const (
	recommendedMaxUserInstances = 512
	recommendedMaxUserWatches   = 524288
)

// warnInotifyLimits reads inotify limits directly from /proc on Linux and warns
// if they are below kind's recommended thresholds.
func warnInotifyLimits() {
	if runtime.GOOS != "linux" {
		return
	}

	instances, err := readProcInt("/proc/sys/fs/inotify/max_user_instances")
	if err != nil {
		klog.V(2).Infof("failed to read /proc/sys/fs/inotify/max_user_instances: %v", err)
	}

	watches, err := readProcInt("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		klog.V(2).Infof("failed to read /proc/sys/fs/inotify/max_user_watches: %v", err)
	}

	checkInotifyLimits(instances, watches)
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
func checkInotifyLimits(instances, watches int) {
	if instances >= 0 && instances < recommendedMaxUserInstances {
		klog.Warningf("inotify limit fs.inotify.max_user_instances is %d, below kind's recommended value of %d; a kind node's kubelet may restart in a loop while other nodes are Ready; raise it with: sudo sysctl -w fs.inotify.max_user_instances=%d",
			instances, recommendedMaxUserInstances, recommendedMaxUserInstances)
	}
	if watches >= 0 && watches < recommendedMaxUserWatches {
		klog.Warningf("inotify limit fs.inotify.max_user_watches is %d, below kind's recommended value of %d; a kind node's kubelet may restart in a loop while other nodes are Ready; raise it with: sudo sysctl -w fs.inotify.max_user_watches=%d",
			watches, recommendedMaxUserWatches, recommendedMaxUserWatches)
	}
}
