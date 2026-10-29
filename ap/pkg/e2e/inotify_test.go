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
	"bytes"
	"os"
	"strings"
	"testing"

	"k8s.io/klog/v2"
)

func TestCheckInotifyLimits(t *testing.T) {
	tests := []struct {
		name          string
		instances     int
		watches       int
		wantInstances bool
		wantWatches   bool
	}{
		{
			name:          "both below recommendation",
			instances:     128,
			watches:       8192,
			wantInstances: true,
			wantWatches:   true,
		},
		{
			name:          "exactly at recommendation",
			instances:     512,
			watches:       524288,
			wantInstances: false,
			wantWatches:   false,
		},
		{
			name:          "above recommendation",
			instances:     1024,
			watches:       1048576,
			wantInstances: false,
			wantWatches:   false,
		},
		{
			name:          "only instances below recommendation",
			instances:     256,
			watches:       524288,
			wantInstances: true,
			wantWatches:   false,
		},
		{
			name:          "only watches below recommendation",
			instances:     512,
			watches:       65536,
			wantInstances: false,
			wantWatches:   true,
		},
		{
			name:          "instances read failed (-1)",
			instances:     -1,
			watches:       524288,
			wantInstances: false,
			wantWatches:   false,
		},
		{
			name:          "watches read failed (-1)",
			instances:     512,
			watches:       -1,
			wantInstances: false,
			wantWatches:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			klog.SetOutput(&logBuf)
			klog.LogToStderr(false)
			defer func() {
				klog.SetOutput(os.Stderr)
				klog.LogToStderr(true)
			}()

			checkInotifyLimits(tc.instances, tc.watches)

			out := logBuf.String()
			hasInstancesWarn := strings.Contains(out, "max_user_instances")
			hasWatchesWarn := strings.Contains(out, "max_user_watches")

			if hasInstancesWarn != tc.wantInstances {
				t.Errorf("instances warning: got %v, want %v (output: %q)", hasInstancesWarn, tc.wantInstances, out)
			}
			if hasWatchesWarn != tc.wantWatches {
				t.Errorf("watches warning: got %v, want %v (output: %q)", hasWatchesWarn, tc.wantWatches, out)
			}

			if tc.wantInstances {
				if !strings.Contains(out, "sudo sysctl -w fs.inotify.max_user_instances=512") {
					t.Errorf("expected max_user_instances sysctl command in warning, got: %q", out)
				}
				if !strings.Contains(out, "a kind node's kubelet may restart in a loop while other nodes are Ready") {
					t.Errorf("expected symptom in max_user_instances warning, got: %q", out)
				}
			}
			if tc.wantWatches {
				if !strings.Contains(out, "sudo sysctl -w fs.inotify.max_user_watches=524288") {
					t.Errorf("expected max_user_watches sysctl command in warning, got: %q", out)
				}
				if !strings.Contains(out, "a kind node's kubelet may restart in a loop while other nodes are Ready") {
					t.Errorf("expected symptom in max_user_watches warning, got: %q", out)
				}
			}
		})
	}
}
