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

package protoc

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

// extractZip extracts all entries from a zip archive at zipPath into destDir.
func extractZip(ctx context.Context, zipPath, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip file %s: %w", zipPath, err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			scoped.Log(ctx).Fine("failed to close zip reader", "path", zipPath, "error", closeErr)
		}
	}()

	for _, file := range reader.File {
		if err := extractZipEntry(ctx, file, destDir); err != nil {
			return err
		}
	}

	return nil
}

func extractZipEntry(ctx context.Context, file *zip.File, destDir string) error {
	cleanName := filepath.Clean(file.Name)
	if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
		return fmt.Errorf("illegal relative file path in zip: %s", file.Name)
	}

	targetPath := filepath.Join(destDir, cleanName)

	if file.FileInfo().IsDir() {
		if err := os.MkdirAll(targetPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
		}
		return nil
	}

	return extractZipFile(ctx, file, targetPath)
}

func extractZipFile(ctx context.Context, file *zip.File, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory for %s: %w", targetPath, err)
	}

	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("failed to open entry %s in zip: %w", file.Name, err)
	}
	defer func() {
		if closeErr := rc.Close(); closeErr != nil {
			scoped.Log(ctx).Fine("failed to close zip entry", "entry", file.Name, "error", closeErr)
		}
	}()

	outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
	if err != nil {
		return fmt.Errorf("failed to open destination file %s: %w", targetPath, err)
	}

	_, copyErr := io.Copy(outFile, rc)
	closeErr := outFile.Close()

	if copyErr != nil {
		return fmt.Errorf("failed to extract file %s: %w", targetPath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close output file %s: %w", targetPath, closeErr)
	}

	return nil
}
