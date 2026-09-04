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

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	certsDPath           = "/etc/containerd/certs.d"
	containerdConfigPath = "/etc/containerd/config.toml"
	etcHostsPath         = "/etc/hosts"
	registryHost         = "images.local"
	namespace            = "in-cluster-image-registry-system"
	serviceName          = "in-cluster-image-registry"
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Error getting in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Error creating clientset: %v", err)
	}

	ctx := context.Background()
	for {
		err := reconcile(ctx, clientset)
		if err != nil {
			klog.Errorf("Reconcile failed: %v", err)
		}
		time.Sleep(30 * time.Second)
	}
}

func reconcile(ctx context.Context, clientset *kubernetes.Clientset) error {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get service %s/%s: %w", namespace, serviceName, err)
	}

	clusterIP := svc.Spec.ClusterIP
	if clusterIP == "" || clusterIP == "None" {
		return fmt.Errorf("service %s/%s has no ClusterIP", namespace, serviceName)
	}

	klog.Infof("Found service %s ClusterIP: %s", serviceName, clusterIP)

	hostsPath := filepath.Join(certsDPath, registryHost, "hosts.toml")
	if err := updateHostsConfig(hostsPath, clusterIP); err != nil {
		return fmt.Errorf("failed to update hosts config: %w", err)
	}

	if err := updateHostsFile(etcHostsPath, clusterIP, registryHost); err != nil {
		return fmt.Errorf("failed to update etc hosts: %w", err)
	}

	changed, err := updateContainerdConfig(containerdConfigPath)
	if err != nil {
		return fmt.Errorf("failed to update containerd config: %w", err)
	}

	if changed {
		if err := restartContainerd(); err != nil {
			return fmt.Errorf("failed to restart containerd: %w", err)
		}
	}

	return nil
}

func updateContainerdConfig(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("containerd config file %s does not exist", path)
			return false, nil
		}
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}

	strContent := string(content)
	lines := strings.Split(strContent, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "config_path") && !strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, certsDPath) {
				return false, nil
			}
		}
	}

	// If we didn't find it, append it.
	newContent := strContent
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += "\n[plugins.\"io.containerd.cri.v1.images\".registry]\n"
	newContent += fmt.Sprintf("  config_path = %q\n", certsDPath)

	klog.Infof("Updating %s to enable certs.d support", path)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return false, fmt.Errorf("failed to write %s: %w", path, err)
	}

	return true, nil
}

func restartContainerd() error {
	klog.Infof("Restarting containerd...")
	// We use nsenter to run systemctl on the host.
	// This requires hostPID: true and privileged: true.
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "systemctl", "restart", "containerd")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart containerd: %w (output: %s)", err, string(output))
	}
	klog.Infof("Successfully restarted containerd")
	return nil
}

func updateHostsConfig(path, ip string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	urlHost := ip
	if strings.Contains(ip, ":") {
		urlHost = fmt.Sprintf("[%s]", ip)
	}

	desiredContent := fmt.Sprintf(`server = "http://%s"

[host."http://%s"]
  capabilities = ["pull", "resolve"]
`, urlHost, urlHost)

	currentContent, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}
	} else if string(currentContent) == desiredContent {
		return nil
	}

	klog.Infof("Updating %s", path)
	if err := os.WriteFile(path, []byte(desiredContent), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func updateHostsFile(path, ip, registryHost string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			content = []byte{}
		} else {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}
	}

	// Trim trailing spaces/newlines to standardize and avoid accumulating blank lines
	strContent := strings.TrimRight(string(content), " \t\r\n")
	var lines []string
	if strContent != "" {
		lines = strings.Split(strContent, "\n")
	}

	var newLines []string
	foundCorrect := false
	changed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			newLines = append(newLines, line)
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			newLines = append(newLines, line)
			continue
		}

		// Check if registryHost is one of the hostnames
		isRegistryHostLine := false
		for _, field := range fields[1:] {
			if strings.EqualFold(field, registryHost) {
				isRegistryHostLine = true
				break
			}
		}

		if isRegistryHostLine {
			if fields[0] == ip && len(fields) == 2 {
				foundCorrect = true
				newLines = append(newLines, line)
			} else {
				changed = true
				// Skip this line
			}
		} else {
			newLines = append(newLines, line)
		}
	}

	if !foundCorrect {
		newLines = append(newLines, fmt.Sprintf("%s %s", ip, registryHost))
		changed = true
	}

	if changed {
		newContent := strings.Join(newLines, "\n") + "\n"
		klog.Infof("Updating %s to point %s to %s", path, registryHost, ip)
		if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", path, err)
		}
	}

	return nil
}
