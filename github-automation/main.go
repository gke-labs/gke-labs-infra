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
	"net/http"
	"os"
	"strconv"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/gke-labs/gke-labs-infra/github-automation/pkg/automation"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)

	appIDStr := os.Getenv("GITHUB_APP_ID")
	if appIDStr == "" {
		klog.Fatal("GITHUB_APP_ID environment variable is required")
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		klog.Fatalf("Invalid GITHUB_APP_ID: %v", err)
	}

	privateKeyPath := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH")
	if privateKeyPath == "" {
		klog.Fatal("GITHUB_APP_PRIVATE_KEY_PATH environment variable is required")
	}

	webhookSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if webhookSecret == "" {
		klog.Fatal("GITHUB_WEBHOOK_SECRET environment variable is required")
	}

	// Create a shared transport to reuse for authenticated clients
	// We will create specific clients per installation ID when processing events
	atr, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, appID, privateKeyPath)
	if err != nil {
		klog.Fatalf("Failed to create GitHub App transport: %v", err)
	}

	handler := &automation.WebhookHandler{
		AppsTransport: atr,
		WebhookSecret: []byte(webhookSecret),
	}

	http.HandleFunc("/webhook", handler.HandleWebhook)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	klog.Infof("Server listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		klog.Fatalf("Server failed: %v", err)
	}
}
