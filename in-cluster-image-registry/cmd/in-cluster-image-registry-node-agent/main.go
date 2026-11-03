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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/in-cluster-image-registry/pkg/linediff"
	"github.com/gke-labs/gke-labs-infra/in-cluster-image-registry/pkg/toml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	defaultCertsDDirectory      = "/etc/containerd/certs.d"
	defaultContainerdConfigPath = "/etc/containerd/config.toml"
	registryHost                = "images.local"
	namespace                   = "in-cluster-image-registry-system"
	serviceName                 = "in-cluster-image-registry"
)

var (
	certsDDirectory         = defaultCertsDDirectory
	containerdConfigPath    = defaultContainerdConfigPath
	containerdHealthTimeout = 30 * time.Second
	lastPreWriteContent     string
	validateConfigFn        = defaultValidateConfig
	restartContainerdFn     = defaultRestartContainerd
	checkContainerdActiveFn = defaultCheckContainerdActive
	dirExistsFn             = defaultDirExists
)

func defaultDirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func resolveConfigPath(raw string) string {
	parts := strings.Split(raw, ":")
	var candidates []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			candidates = append(candidates, trimmed)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	for _, c := range candidates {
		if dirExistsFn(c) {
			return c
		}
	}
	return candidates[0]
}

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

func reconcile(ctx context.Context, clientset kubernetes.Interface) error {
	svc, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get service %s/%s: %w", namespace, serviceName, err)
	}

	clusterIP := svc.Spec.ClusterIP
	if clusterIP == "" || clusterIP == "None" {
		return fmt.Errorf("service %s/%s has no ClusterIP", namespace, serviceName)
	}

	klog.Infof("Found service %s ClusterIP: %s", serviceName, clusterIP)

	certsDir, changed, err := updateContainerdConfig(containerdConfigPath)
	if err != nil {
		return fmt.Errorf("failed to update containerd config: %w", err)
	}

	hostsPath := filepath.Join(certsDir, registryHost, "hosts.toml")
	if err := updateHostsConfig(hostsPath, clusterIP); err != nil {
		return fmt.Errorf("failed to update hosts config: %w", err)
	}

	if changed {
		if err := restartAndVerifyContainerd(containerdConfigPath, lastPreWriteContent); err != nil {
			return fmt.Errorf("failed to restart and verify containerd: %w", err)
		}
	}

	return nil
}

type transformResult struct {
	Content  string
	CertsDir string
	Changed  bool
	Mirrors  map[string][]string
}

func transformContainerdConfig(content string, certsDPath string) (*transformResult, error) {
	sections := toml.ParseSections(content)
	changed := false
	mirrorsToTranslate := make(map[string][]string)

	// 1. Version detection: read top-level `version` key only (sections[0])
	version := 0
	hasVersionKey := false
	for _, line := range sections[0].Lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if key, val, ok := strings.Cut(trimmed, "="); ok {
			if strings.TrimSpace(key) == "version" {
				hasVersionKey = true
				vStr := strings.Trim(strings.TrimSpace(val), `"'`)
				v, err := strconv.Atoi(vStr)
				if err != nil {
					return nil, fmt.Errorf("invalid version %q: %w", vStr, err)
				}
				version = v
				break
			}
		}
	}

	// containerd 2.x configuration schemas (version 3, and version 4 since
	// containerd 2.3) keep the registry settings under the cri.v1.images
	// plugin; only version 1 and 2 files use the grpc.v1.cri plugin.
	v3Schema := version >= 3
	var targetPlugin string
	if v3Schema {
		targetPlugin = "io.containerd.cri.v1.images"
	} else {
		if !hasVersionKey {
			klog.Infof("no version key found in containerd config, treating as containerd v1/v2 semantics")
		}
		targetPlugin = "io.containerd.grpc.v1.cri"
	}
	targetParts := []string{"plugins", targetPlugin, "registry"}
	targetHeader := fmt.Sprintf("[plugins.%q.registry]", targetPlugin)

	// 2. Remove stale v3 block on v2/v1 files
	if !v3Schema {
		staleV3Parts := []string{"plugins", "io.containerd.cri.v1.images", "registry"}
		var filtered []*toml.Section
		for _, sec := range sections {
			if slices.Equal(sec.Parts, staleV3Parts) {
				klog.Infof("Removing stale containerd v3 registry table from v2 config: %s", sec.HeaderLine)
				changed = true
				continue
			}
			filtered = append(filtered, sec)
		}
		sections = filtered
	}

	// 3. Handle mirrors and configs under target registry
	for _, sec := range sections {
		if len(sec.Parts) >= 4 &&
			sec.Parts[0] == "plugins" &&
			sec.Parts[1] == targetPlugin &&
			sec.Parts[2] == "registry" &&
			(sec.Parts[3] == "mirrors" || sec.Parts[3] == "configs") {

			if sec.Parts[3] == "mirrors" && len(sec.Parts) >= 5 {
				host := sec.Parts[4]
				endpoints := toml.ParseEndpoints(sec.Lines)
				if len(endpoints) > 0 {
					mirrorsToTranslate[host] = endpoints
				}
			}

			if !strings.HasPrefix(strings.TrimSpace(sec.HeaderLine), "#") {
				klog.Infof("Neutralizing registry table: %s", sec.HeaderLine)
				sec.HeaderLine = "# " + sec.HeaderLine
				changed = true
			}
			for i, line := range sec.Lines {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					klog.Infof("Neutralizing registry table line: %s", line)
					sec.Lines[i] = "# " + line
					changed = true
				}
			}
		}
	}

	// 4. Handle target table
	var targetSec *toml.Section
	for _, sec := range sections {
		if slices.Equal(sec.Parts, targetParts) {
			targetSec = sec
			break
		}
	}

	var effectiveCertsDDir string
	if targetSec != nil {
		configPathIdx := -1
		for i, line := range targetSec.Lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || trimmed == "" {
				continue
			}
			if key, val, ok := strings.Cut(trimmed, "="); ok {
				if strings.TrimSpace(key) == "config_path" {
					configPathIdx = i
					cleanVal := strings.Trim(strings.TrimSpace(val), `"'`)
					resolved := resolveConfigPath(cleanVal)
					if resolved == "" {
						indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
						if indent == "" {
							indent = "  "
						}
						targetSec.Lines[i] = fmt.Sprintf("%sconfig_path = %q", indent, certsDPath)
						changed = true
						effectiveCertsDDir = certsDPath
					} else {
						effectiveCertsDDir = resolved
					}
					break
				}
			}
		}

		if configPathIdx == -1 {
			targetSec.Lines = append([]string{fmt.Sprintf("  config_path = %q", certsDPath)}, targetSec.Lines...)
			changed = true
			effectiveCertsDDir = certsDPath
		}
	} else {
		lastSec := sections[len(sections)-1]
		for len(lastSec.Lines) > 0 && lastSec.Lines[len(lastSec.Lines)-1] == "" {
			lastSec.Lines = lastSec.Lines[:len(lastSec.Lines)-1]
		}
		lastSec.Lines = append(lastSec.Lines, "")

		newSec := &toml.Section{
			HeaderLine: targetHeader,
			Parts:      targetParts,
			Lines: []string{
				fmt.Sprintf("  config_path = %q", certsDPath),
				"",
			},
		}
		sections = append(sections, newSec)
		changed = true
		effectiveCertsDDir = certsDPath
	}

	if !changed {
		return &transformResult{
			Content:  content,
			CertsDir: effectiveCertsDDir,
			Changed:  false,
			Mirrors:  mirrorsToTranslate,
		}, nil
	}

	var allLines []string
	for _, sec := range sections {
		allLines = append(allLines, sec.Render()...)
	}
	newContent := strings.Join(allLines, "\n")
	return &transformResult{
		Content:  newContent,
		CertsDir: effectiveCertsDDir,
		Changed:  true,
		Mirrors:  mirrorsToTranslate,
	}, nil
}

func translateMirrorConfig(certsDPath string, host string, endpoints []string) error {
	dir := filepath.Join(certsDPath, host)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	hostsPath := filepath.Join(dir, "hosts.toml")

	// Upstream registry endpoint for docker.io is registry-1.docker.io
	serverHost := host
	if serverHost == "docker.io" {
		serverHost = "registry-1.docker.io"
	}
	serverURL := serverHost
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		serverURL = "https://" + serverURL
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("server = %q\n\n", serverURL))
	for _, ep := range endpoints {
		epURL := ep
		if !strings.HasPrefix(epURL, "http://") && !strings.HasPrefix(epURL, "https://") {
			epURL = "https://" + epURL
		}
		sb.WriteString(fmt.Sprintf("[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n\n", epURL))
	}
	desired := sb.String()

	existing, err := os.ReadFile(hostsPath)
	if err == nil && string(existing) == desired {
		return nil
	}
	klog.Infof("Writing translated mirror config to %s", hostsPath)
	return os.WriteFile(hostsPath, []byte(desired), 0644)
}

func copyFile(src, dst string) (err error) {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := sourceFile.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close %s: %w", src, closeErr)
		}
	}()

	destFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := destFile.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close %s: %w", dst, closeErr)
		}
	}()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}
	return destFile.Sync()
}

func updateContainerdConfig(path string) (string, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("containerd config file %s does not exist", path)
			return certsDDirectory, false, nil
		}
		return "", false, fmt.Errorf("failed to read %s: %w", path, err)
	}

	strContent := string(content)
	lastPreWriteContent = strContent
	result, err := transformContainerdConfig(strContent, certsDDirectory)
	if err != nil {
		return "", false, fmt.Errorf("failed to transform %s: %w", path, err)
	}

	// Translate mirrors into <result.CertsDir>/<host>/hosts.toml before writing config.toml.
	// If translation fails, abort before modifying config.toml so the node does not
	// silently lose mirror endpoints.
	for host, endpoints := range result.Mirrors {
		if err := translateMirrorConfig(result.CertsDir, host, endpoints); err != nil {
			return "", false, fmt.Errorf("failed to translate mirror config for %s: %w", host, err)
		}
	}

	if !result.Changed {
		return result.CertsDir, false, nil
	}

	klog.Infof("Containerd config diff:\n%s", linediff.Diff(strContent, result.Content))

	// Backup before writing if backup does not already exist
	backupPath := path + ".in-cluster-image-registry.bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		if err := copyFile(path, backupPath); err != nil {
			return "", false, fmt.Errorf("failed to create backup %s: %w", backupPath, err)
		}
		klog.Infof("Created initial backup of containerd config at %s", backupPath)
	}

	// Write candidate to temp file in same directory
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, "config.toml.tmp.*")
	if err != nil {
		return "", false, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if closeErr := tmpFile.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close %s: %v", tmpName, closeErr)
		}
		if rmErr := os.Remove(tmpName); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.Warningf("Failed to remove temporary config file %s: %v", tmpName, rmErr)
		}
	}()

	if _, err := tmpFile.WriteString(result.Content); err != nil {
		return "", false, fmt.Errorf("failed to write candidate config to %s: %w", tmpName, err)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		return "", false, fmt.Errorf("failed to chmod %s: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", false, fmt.Errorf("failed to close %s: %w", tmpName, err)
	}

	// Validate candidate: containerd config dump verifies TOML syntax and checks for duplicate
	// keys/tables, but does not execute CRI plugin validation (e.g. mirrors vs config_path conflicts);
	// runtime health is verified by polling systemctl is-active containerd post-restart.
	if err := validateConfigFn(tmpName); err != nil {
		return "", false, fmt.Errorf("candidate containerd config validation failed: %w", err)
	}

	// Rename into place
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, fmt.Errorf("failed to rename %s to %s: %w", tmpName, path, err)
	}
	klog.Infof("Successfully updated containerd config at %s", path)

	return result.CertsDir, true, nil
}

func defaultValidateConfig(tmpPath string) error {
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "containerd", "-c", tmpPath, "config", "dump")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("containerd config dump failed: %w (output: %s)", err, string(output))
	}
	return nil
}

func defaultRestartContainerd() error {
	klog.Infof("Restarting containerd...")
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "systemctl", "restart", "containerd")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart containerd: %w (output: %s)", err, string(output))
	}
	klog.Infof("Successfully initiated containerd restart")
	return nil
}

func restartContainerd() error {
	return restartContainerdFn()
}

func defaultCheckContainerdActive() bool {
	cmd := exec.Command("nsenter", "-t", "1", "-m", "-u", "-i", "-n", "systemctl", "is-active", "containerd")
	output, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(output)) == "active"
}

func waitForContainerdActive(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if checkContainerdActiveFn() {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

func rollbackConfig(path string, preWriteContent string) error {
	// First restore the pre-write content of this run if available,
	// avoiding reverting legitimate external changes between runs.
	if preWriteContent != "" {
		klog.Warningf("Rolling back containerd config %s to pre-write content", path)
		if err := os.WriteFile(path, []byte(preWriteContent), 0644); err != nil {
			klog.Errorf("Failed to write pre-write content during rollback: %v; falling back to .bak file", err)
		} else {
			return nil
		}
	}

	backupPath := path + ".in-cluster-image-registry.bak"
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup file %s not found: %w", backupPath, err)
	}
	klog.Warningf("Rolling back containerd config from %s to %s", backupPath, path)
	if err := copyFile(backupPath, path); err != nil {
		return fmt.Errorf("failed to restore backup %s: %w", backupPath, err)
	}
	return nil
}

func restartAndVerifyContainerd(configPath string, preWriteContent string) error {
	if err := restartContainerd(); err != nil {
		klog.Errorf("Initial restart of containerd failed: %v; restoring backup...", err)
		if rbErr := rollbackConfig(configPath, preWriteContent); rbErr != nil {
			klog.Errorf("Rollback failed: %v", rbErr)
		} else {
			if rErr := restartContainerd(); rErr != nil {
				klog.Errorf("Failed to restart containerd after rollback: %v", rErr)
			}
		}
		return fmt.Errorf("failed to restart containerd: %w", err)
	}

	// Because this node agent runs in a DaemonSet container with hostPID and privileged access,
	// existing container processes continue running via containerd-shim during a daemon restart.
	// If containerd fails to restart (e.g. invalid config), the daemon exits and kubelet cannot
	// communicate with CRI, but the agent process stays alive in its existing namespace for long
	// enough to detect the failure, rollback config.toml, and recover the runtime before the node
	// becomes unrecoverable.
	klog.Infof("Waiting for containerd to become active (up to %v)...", containerdHealthTimeout)
	if !waitForContainerdActive(containerdHealthTimeout) {
		klog.Errorf("containerd failed to become active within %v; restoring backup...", containerdHealthTimeout)
		if rbErr := rollbackConfig(configPath, preWriteContent); rbErr != nil {
			klog.Errorf("Rollback failed: %v", rbErr)
		} else {
			if rErr := restartContainerd(); rErr != nil {
				klog.Errorf("Failed to restart containerd after rollback: %v", rErr)
			}
		}
		return fmt.Errorf("containerd was not active after 30s following config update; restored backup")
	}
	klog.Infof("containerd is active and healthy")
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
