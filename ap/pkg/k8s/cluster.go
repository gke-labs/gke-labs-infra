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

package k8s

// KubeCluster identifies the cluster a deploy talks to. Today that is a
// kubeconfig context; later it can carry a rest.Config or a tunnelled transport.
type KubeCluster struct {
	kubeconfigContext string // "" means the current context
	kindCluster       string // set when the target is kind://<name>; context is then "kind-<name>"
}

// CurrentCluster returns a KubeCluster targeting the current kubeconfig context.
func CurrentCluster() KubeCluster {
	return KubeCluster{}
}

// CurrentKindCluster returns a KubeCluster targeting the current kubeconfig context,
// which is known to be a kind cluster with the given name.
func CurrentKindCluster(name string) KubeCluster {
	return KubeCluster{
		kindCluster: name,
	}
}

// ClusterFromContext returns a KubeCluster targeting a specific kubeconfig context.
func ClusterFromContext(kubeconfigContext string) KubeCluster {
	return KubeCluster{
		kubeconfigContext: kubeconfigContext,
	}
}

// KindCluster returns a KubeCluster targeting a kind cluster by name.
func KindCluster(name string) KubeCluster {
	if name == "" {
		return KubeCluster{}
	}
	return KubeCluster{
		kubeconfigContext: "kind-" + name,
		kindCluster:       name,
	}
}

// KubectlArgs returns the kubectl arguments needed to target this cluster,
// or nil if targeting the current context.
func (c KubeCluster) KubectlArgs() []string {
	if c.kubeconfigContext == "" {
		return nil
	}
	return []string{"--context", c.kubeconfigContext}
}

// IsKind reports whether this cluster is a kind cluster.
func (c KubeCluster) IsKind() bool {
	return c.kindCluster != ""
}

// KindClusterName returns the name of the kind cluster, if IsKind() is true.
func (c KubeCluster) KindClusterName() string {
	return c.kindCluster
}

// String returns a string representation for cache keys and task names.
func (c KubeCluster) String() string {
	return c.kubeconfigContext
}
