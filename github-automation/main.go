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
