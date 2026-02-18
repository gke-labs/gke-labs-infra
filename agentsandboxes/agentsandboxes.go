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

package agentsandboxes

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// Sandbox represents a sandbox environment.
type Sandbox struct {
	Name string
}

// Delete deletes the sandbox.
func (s *Sandbox) Delete(ctx context.Context) error {
	return Delete(ctx, s.Name)
}

// SandboxBuilder is a builder for creating a new Sandbox.
type SandboxBuilder struct {
	name  string
	image string
}

// New creates a new SandboxBuilder.
func New(name string) *SandboxBuilder {
	return &SandboxBuilder{
		name:  name,
		image: "local/ap-golang:latest", // Default image
	}
}

// WithImage sets the image for the sandbox.
func (b *SandboxBuilder) WithImage(image string) *SandboxBuilder {
	b.image = image
	return b
}

// Create creates the sandbox.
func (b *SandboxBuilder) Create(ctx context.Context) (*Sandbox, error) {
	klog.Infof("Creating sandbox %s with image %s...", b.name, b.image)
	cmd := exec.CommandContext(ctx, "kubectl", "run", b.name,
		"--image="+b.image,
		"--restart=Never",
		"--labels=app=agent-sandbox",
		"--", "serve")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}

	return &Sandbox{Name: b.name}, nil
}

// List lists all sandboxes.
func List(ctx context.Context) ([]*Sandbox, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", "app=agent-sandbox", "-o", "jsonpath={.items[*].metadata.name}")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list sandboxes: %w", err)
	}

	names := strings.Fields(string(out))
	var sandboxes []*Sandbox
	for _, name := range names {
		sandboxes = append(sandboxes, &Sandbox{Name: name})
	}
	return sandboxes, nil
}

// Get retrieves a sandbox by name.
func Get(ctx context.Context, name string) (*Sandbox, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pod", name, "--no-headers")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to get sandbox: %w", err)
	}
	return &Sandbox{Name: name}, nil
}

// Delete deletes a sandbox by name.
func Delete(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "pod", name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete sandbox: %w", err)
	}
	return nil
}
