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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestServiceProxyTransport(t *testing.T) {
	var receivedPaths []string
	var receivedQueries []string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedPaths = append(receivedPaths, request.URL.Path)
		receivedQueries = append(receivedQueries, request.URL.RawQuery)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse failed: %v", err)
	}

	proxyPrefix := "/api/v1/namespaces/test-namespace/services/test-service:5000/proxy"
	transport := &serviceProxyTransport{
		delegate:    server.Client().Transport,
		targetURL:   serverURL,
		proxyPrefix: proxyPrefix,
		namespace:   "test-namespace",
		service:     "test-service",
	}

	client := &http.Client{Transport: transport}

	// 1. Plain path gets the prefix and preserves the query
	request1, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://images.local/v2/myrepo/blobs/uploads/?digest=sha256:12345", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	response1, err := client.Do(request1)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	_ = response1.Body.Close()

	wantPath1 := proxyPrefix + "/v2/myrepo/blobs/uploads/"
	if len(receivedPaths) < 1 || receivedPaths[0] != wantPath1 {
		t.Errorf("expected path %q, got %q", wantPath1, receivedPaths[0])
	}
	wantQuery1 := "digest=sha256:12345"
	if len(receivedQueries) < 1 || receivedQueries[0] != wantQuery1 {
		t.Errorf("expected query %q, got %q", wantQuery1, receivedQueries[0])
	}

	// 2. Path that already has the prefix is not doubled
	request2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://images.local"+wantPath1, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	response2, err := client.Do(request2)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	_ = response2.Body.Close()

	if len(receivedPaths) < 2 || receivedPaths[1] != wantPath1 {
		t.Errorf("expected path not to be doubled %q, got %q", wantPath1, receivedPaths[1])
	}
}

func TestServiceProxyTransport_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse failed: %v", err)
	}

	transport := &serviceProxyTransport{
		delegate:    server.Client().Transport,
		targetURL:   serverURL,
		proxyPrefix: "/proxy",
		namespace:   "test-namespace",
		service:     "test-service",
	}

	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://images.local/v2/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	_, err = client.Do(request)
	if err == nil {
		t.Fatal("expected error on HTTP 403, got nil")
	}
	if !strings.Contains(err.Error(), "services/proxy") {
		t.Errorf("expected error to mention 'services/proxy', got: %v", err)
	}
}
