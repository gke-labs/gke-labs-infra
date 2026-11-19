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

package registrypush

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"k8s.io/client-go/rest"
)

// NewTransport returns an http.RoundTripper that sends registry requests through
// the Kubernetes API server's service proxy for the given service.
func NewTransport(cluster k8s.KubeCluster, namespace, service string, port int) (http.RoundTripper, error) {
	clientConfig, err := cluster.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading client configuration for cluster %s: %w", cluster, err)
	}

	delegate, err := rest.TransportFor(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("creating rest transport: %w", err)
	}

	baseURL, err := url.Parse(clientConfig.Host)
	if err != nil {
		return nil, fmt.Errorf("parsing host URL %s: %w", clientConfig.Host, err)
	}

	proxyPrefix := fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%d/proxy", namespace, service, port)
	return &serviceProxyTransport{
		delegate:    delegate,
		targetURL:   baseURL,
		proxyPrefix: proxyPrefix,
		namespace:   namespace,
		service:     service,
	}, nil
}

type serviceProxyTransport struct {
	delegate    http.RoundTripper
	targetURL   *url.URL
	proxyPrefix string
	namespace   string
	service     string
}

func (transport *serviceProxyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clonedRequest := request.Clone(request.Context())

	// Strip the prefix if the API server already added it (e.g. in Location header rewrites).
	cleanedPath := strings.TrimPrefix(request.URL.Path, transport.proxyPrefix)
	if !strings.HasPrefix(cleanedPath, "/") {
		cleanedPath = "/" + cleanedPath
	}

	targetURL := *request.URL
	targetURL.Scheme = transport.targetURL.Scheme
	targetURL.Host = transport.targetURL.Host
	targetURL.Path = strings.TrimRight(transport.targetURL.Path, "/") + transport.proxyPrefix + cleanedPath

	clonedRequest.URL = &targetURL
	clonedRequest.Host = transport.targetURL.Host

	response, err := transport.delegate.RoundTrip(clonedRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusForbidden {
		if closeErr := response.Body.Close(); closeErr != nil {
			return nil, fmt.Errorf("closing response body: %w", closeErr)
		}
		return nil, fmt.Errorf("access forbidden by API server (HTTP 403): user requires 'services/proxy' permission for service %s in namespace %s", transport.service, transport.namespace)
	}
	return response, nil
}
