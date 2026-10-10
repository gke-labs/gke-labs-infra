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

package runnerpool

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

// SandboxGVR is the GroupVersionResource of the agent-sandbox Sandbox CRD.
var SandboxGVR = schema.GroupVersionResource{
	Group:    sandboxv1beta1.GroupVersion.Group,
	Version:  sandboxv1beta1.GroupVersion.Version,
	Resource: "sandboxes",
}

// KubeClient is the subset of the Kubernetes API the pool needs. All
// operations are scoped to the sandbox namespace the client was created for.
type KubeClient interface {
	ListSandboxes(ctx context.Context, labelSelector string) ([]sandboxv1beta1.Sandbox, error)
	CreateSandbox(ctx context.Context, sandbox *sandboxv1beta1.Sandbox) (*sandboxv1beta1.Sandbox, error)
	// DeleteSandbox deletes a sandbox; deleting a sandbox that does not exist
	// is not an error.
	DeleteSandbox(ctx context.Context, name string) error

	CreateSecret(ctx context.Context, secret *corev1.Secret) (*corev1.Secret, error)
	// SetSecretOwner adds an owner reference to an existing secret so that
	// it is garbage collected together with its sandbox.
	SetSecretOwner(ctx context.Context, name string, owner metav1.OwnerReference) error
	// DeleteSecret deletes a secret; deleting a secret that does not exist is
	// not an error.
	DeleteSecret(ctx context.Context, name string) error
}

type kubeClient struct {
	namespace string
	dynamic   dynamic.Interface
	core      kubernetes.Interface
}

var _ KubeClient = &kubeClient{}

// NewKubeClient returns a KubeClient scoped to the given namespace.
func NewKubeClient(namespace string, dyn dynamic.Interface, core kubernetes.Interface) KubeClient {
	return &kubeClient{namespace: namespace, dynamic: dyn, core: core}
}

func (c *kubeClient) ListSandboxes(ctx context.Context, labelSelector string) ([]sandboxv1beta1.Sandbox, error) {
	list, err := c.dynamic.Resource(SandboxGVR).Namespace(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing sandboxes in namespace %q: %w", c.namespace, err)
	}
	out := make([]sandboxv1beta1.Sandbox, 0, len(list.Items))
	for i := range list.Items {
		var sb sandboxv1beta1.Sandbox
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &sb); err != nil {
			return nil, fmt.Errorf("decoding sandbox %q: %w", list.Items[i].GetName(), err)
		}
		out = append(out, sb)
	}
	return out, nil
}

func (c *kubeClient) CreateSandbox(ctx context.Context, sandbox *sandboxv1beta1.Sandbox) (*sandboxv1beta1.Sandbox, error) {
	sandbox.APIVersion = sandboxv1beta1.GroupVersion.String()
	sandbox.Kind = "Sandbox"
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sandbox)
	if err != nil {
		return nil, fmt.Errorf("encoding sandbox %q: %w", sandbox.Name, err)
	}
	created, err := c.dynamic.Resource(SandboxGVR).Namespace(c.namespace).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating sandbox %q: %w", sandbox.Name, err)
	}
	var out sandboxv1beta1.Sandbox
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(created.Object, &out); err != nil {
		return nil, fmt.Errorf("decoding created sandbox %q: %w", sandbox.Name, err)
	}
	return &out, nil
}

func (c *kubeClient) DeleteSandbox(ctx context.Context, name string) error {
	err := c.dynamic.Resource(SandboxGVR).Namespace(c.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting sandbox %q: %w", name, err)
	}
	return nil
}

func (c *kubeClient) CreateSecret(ctx context.Context, secret *corev1.Secret) (*corev1.Secret, error) {
	created, err := c.core.CoreV1().Secrets(c.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating secret %q: %w", secret.Name, err)
	}
	return created, nil
}

func (c *kubeClient) SetSecretOwner(ctx context.Context, name string, owner metav1.OwnerReference) error {
	patch := fmt.Sprintf(`{"metadata":{"ownerReferences":[{"apiVersion":%q,"kind":%q,"name":%q,"uid":%q,"controller":true}]}}`,
		owner.APIVersion, owner.Kind, owner.Name, owner.UID)
	if _, err := c.core.CoreV1().Secrets(c.namespace).Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("setting owner of secret %q: %w", name, err)
	}
	return nil
}

func (c *kubeClient) DeleteSecret(ctx context.Context, name string) error {
	err := c.core.CoreV1().Secrets(c.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting secret %q: %w", name, err)
	}
	return nil
}
