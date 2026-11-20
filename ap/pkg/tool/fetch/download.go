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

package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

// Download downloads a file from downloadURL to a temporary file in dir, verifying its SHA-256 digest
// if expectedDigest is non-empty. On success, it returns the path to the downloaded temporary file.
// The caller is responsible for removing or renaming the file when finished.
func Download(ctx context.Context, downloadURL, expectedDigest, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, "download-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpName := tmpFile.Name()
	var closed bool
	defer func() {
		if !closed {
			if closeErr := tmpFile.Close(); closeErr != nil {
				scoped.Log(ctx).Fine("failed to close temporary file", "path", tmpName, "error", closeErr)
			}
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request for %s: %w", downloadURL, err)
	}

	resp, err := scoped.HTTPClient(ctx).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download from %s: %w", downloadURL, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			scoped.Log(ctx).Fine("failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download from %s: HTTP status %s", downloadURL, resp.Status)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		return "", fmt.Errorf("failed writing downloaded file: %w", err)
	}

	closed = true
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed closing downloaded file: %w", err)
	}

	if expectedDigest != "" {
		expectedHash := strings.TrimPrefix(expectedDigest, "sha256:")
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualHash, expectedHash) {
			if rmErr := os.Remove(tmpName); rmErr != nil && !os.IsNotExist(rmErr) {
				scoped.Log(ctx).Fine("failed to remove temporary file", "path", tmpName, "error", rmErr)
			}
			return "", fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
		}
	}

	return tmpName, nil
}

// Digest downloads from downloadURL, computes its SHA-256 hash without saving to disk,
// and returns it in "sha256:<hex>" format.
func Digest(ctx context.Context, downloadURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request for %s: %w", downloadURL, err)
	}
	req.Header.Set("User-Agent", "gke-labs-ap")

	resp, err := scoped.HTTPClient(ctx).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download from %s: %w", downloadURL, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			scoped.Log(ctx).Fine("failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download from %s: HTTP status %s", downloadURL, resp.Status)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, resp.Body); err != nil {
		return "", fmt.Errorf("failed computing digest for %s: %w", downloadURL, err)
	}

	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}
