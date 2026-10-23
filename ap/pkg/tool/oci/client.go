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

package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// Accepted media types for OCI and Docker image manifests.
const (
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeOCILayer       = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeDockerLayer    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

// ImageRef represents a parsed container image reference.
type ImageRef struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

// ParseImageRef parses a container image reference string into an ImageRef.
// Examples:
//
//	"ghcr.io/gke-labs/tools/protoc:21.12"
//	"ghcr.io/gke-labs/tools/protoc@sha256:abc..."
//	"gke-labs/tools/protoc:21.12" -> defaults registry to "ghcr.io"
func ParseImageRef(raw string) (*ImageRef, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty image reference")
	}

	ref := &ImageRef{
		Registry: "ghcr.io",
	}

	var remainder string
	// Split digest if present
	if atIdx := strings.Index(raw, "@"); atIdx != -1 {
		ref.Digest = raw[atIdx+1:]
		remainder = raw[:atIdx]
	} else if colonIdx := strings.LastIndex(raw, ":"); colonIdx != -1 && !strings.Contains(raw[colonIdx:], "/") {
		ref.Tag = raw[colonIdx+1:]
		remainder = raw[:colonIdx]
	} else {
		remainder = raw
		ref.Tag = "latest"
	}

	// Check if remainder has a registry domain (contains '.' or ':' before the first '/')
	firstSlash := strings.Index(remainder, "/")
	if firstSlash != -1 {
		prefix := remainder[:firstSlash]
		if strings.Contains(prefix, ".") || strings.Contains(prefix, ":") || prefix == "localhost" {
			ref.Registry = prefix
			ref.Repository = remainder[firstSlash+1:]
		} else {
			ref.Repository = remainder
		}
	} else {
		ref.Repository = remainder
	}

	return ref, nil
}

// Platform represents the target OS and architecture.
type Platform struct {
	OS           string
	Architecture string
}

// CurrentPlatform returns the runtime Platform.
func CurrentPlatform() Platform {
	return Platform{
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
	}
}

// Descriptor represents an OCI / Docker content descriptor.
type Descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

// ManifestList represents an OCI image index or Docker manifest list.
type ManifestList struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []Descriptor `json:"manifests"`
}

// Manifest represents an OCI or Docker image manifest.
type Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

// PullOptions configures an image layer pull operation.
type PullOptions struct {
	ImageRef       string
	Platform       Platform
	DestDir        string
	ExpectedDigest string
	InsecureHTTP   bool
}

// PullResult contains metadata about the pulled layer.
type PullResult struct {
	ManifestDigest string
	LayerDigest    string
	LayerSize      int64
	FilesExtracted int
	Cached         bool
}

// Client is a minimal, standard-library-only OCI distribution registry client.
type Client struct {
	HTTPClient *http.Client
	tokenCache map[string]string // realm:scope -> token
	mu         sync.Mutex
}

// NewClient returns a new OCI distribution Client.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}
	return &Client{
		HTTPClient: httpClient,
		tokenCache: make(map[string]string),
	}
}

// PullLayer fetches a multi-arch container image from an OCI registry, selects the layer
// for the requested platform, verifies its SHA-256 digest, and extracts the contents
// directly into destDir without requiring a Docker daemon.
func (c *Client) PullLayer(ctx context.Context, opts PullOptions) (*PullResult, error) {
	ref, err := ParseImageRef(opts.ImageRef)
	if err != nil {
		return nil, fmt.Errorf("invalid image ref %q: %w", opts.ImageRef, err)
	}

	targetPlatform := opts.Platform
	if targetPlatform.OS == "" {
		targetPlatform = CurrentPlatform()
	}

	scheme := "https"
	if opts.InsecureHTTP {
		scheme = "http"
	}

	reference := ref.Digest
	if reference == "" {
		reference = ref.Tag
	}

	// 1. Fetch Manifest / Index
	manifestBytes, mediaType, manifestDigest, err := c.fetchManifest(ctx, scheme, ref.Registry, ref.Repository, reference)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest for %s/%s:%s: %w", ref.Registry, ref.Repository, reference, err)
	}

	if opts.ExpectedDigest != "" && manifestDigest != opts.ExpectedDigest {
		return nil, fmt.Errorf("manifest digest mismatch: expected %s, got %s", opts.ExpectedDigest, manifestDigest)
	}

	// 2. Resolve target platform manifest if this is an index / manifest list
	childManifestDigest := manifestDigest
	if mediaType == MediaTypeOCIIndex || mediaType == MediaTypeDockerList {
		var index ManifestList
		if err := json.Unmarshal(manifestBytes, &index); err != nil {
			return nil, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		matchedDigest, err := resolvePlatformManifest(&index, targetPlatform)
		if err != nil {
			return nil, fmt.Errorf("could not resolve platform %s/%s for image %s: %w", targetPlatform.OS, targetPlatform.Architecture, opts.ImageRef, err)
		}
		childManifestDigest = matchedDigest

		// Fetch the platform-specific image manifest
		mBytes, mType, _, err := c.fetchManifest(ctx, scheme, ref.Registry, ref.Repository, childManifestDigest)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch child manifest %s: %w", childManifestDigest, err)
		}
		manifestBytes = mBytes
		mediaType = mType
	}

	// 3. Parse platform image manifest
	var imgManifest Manifest
	if err := json.Unmarshal(manifestBytes, &imgManifest); err != nil {
		return nil, fmt.Errorf("failed to parse image manifest: %w", err)
	}

	if len(imgManifest.Layers) == 0 {
		return nil, fmt.Errorf("image manifest has no layers")
	}

	// Single-layer toolchain image: extract the layer
	layerDesc := imgManifest.Layers[0]
	if layerDesc.Digest == "" {
		return nil, fmt.Errorf("image layer has no digest")
	}

	// 4. Check cache marker
	markerFile := filepath.Join(opts.DestDir, ".ap_layer_digest")
	cachedData, err := os.ReadFile(markerFile)
	if err == nil {
		if strings.TrimSpace(string(cachedData)) == layerDesc.Digest {
			klog.V(2).Infof("Layer %s already cached in %s", layerDesc.Digest, opts.DestDir)
			return &PullResult{
				ManifestDigest: manifestDigest,
				LayerDigest:    layerDesc.Digest,
				LayerSize:      layerDesc.Size,
				Cached:         true,
			}, nil
		}
	} else if !os.IsNotExist(err) {
		klog.V(2).Infof("failed to read cache marker %s: %v", markerFile, err)
	}

	// 5. Download blob and unpack
	filesCount, err := c.downloadAndExtractLayer(ctx, scheme, ref.Registry, ref.Repository, layerDesc.Digest, opts.DestDir)
	if err != nil {
		return nil, fmt.Errorf("failed to download and extract layer %s: %w", layerDesc.Digest, err)
	}

	// Write cache marker
	if err := os.WriteFile(markerFile, []byte(layerDesc.Digest), 0644); err != nil {
		klog.V(2).Infof("failed to write layer cache marker: %v", err)
	}

	return &PullResult{
		ManifestDigest: manifestDigest,
		LayerDigest:    layerDesc.Digest,
		LayerSize:      layerDesc.Size,
		FilesExtracted: filesCount,
		Cached:         false,
	}, nil
}

func resolvePlatformManifest(index *ManifestList, target Platform) (string, error) {
	for _, m := range index.Manifests {
		if m.Platform != nil {
			if strings.EqualFold(m.Platform.OS, target.OS) && strings.EqualFold(m.Platform.Architecture, target.Architecture) {
				return m.Digest, nil
			}
		}
	}
	return "", fmt.Errorf("no matching manifest for os=%s arch=%s", target.OS, target.Architecture)
}

func (c *Client) fetchManifest(ctx context.Context, scheme, registry, repo, reference string) ([]byte, string, string, error) {
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, registry, repo, reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, "", "", err
	}

	acceptHeaders := strings.Join([]string{
		MediaTypeOCIIndex,
		MediaTypeDockerList,
		MediaTypeOCIManifest,
		MediaTypeDockerManifest,
	}, ", ")
	req.Header.Set("Accept", acceptHeaders)

	resp, err := c.doWithAuth(ctx, req, registry, repo)
	if err != nil {
		return nil, "", "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close manifest response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return nil, "", "", fmt.Errorf("registry returned HTTP %d (failed to read body: %w)", resp.StatusCode, readErr)
		}
		return nil, "", "", fmt.Errorf("registry returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("reading manifest response: %w", err)
	}

	mediaType := resp.Header.Get("Content-Type")
	if semi := strings.Index(mediaType, ";"); semi != -1 {
		mediaType = strings.TrimSpace(mediaType[:semi])
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		hash := sha256.Sum256(data)
		digest = "sha256:" + hex.EncodeToString(hash[:])
	}

	return data, mediaType, digest, nil
}

func (c *Client) downloadAndExtractLayer(ctx context.Context, scheme, registry, repo, digest, destDir string) (int, error) {
	blobURL := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", scheme, registry, repo, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.doWithAuth(ctx, req, registry, repo)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close blob response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return 0, fmt.Errorf("fetching blob returned HTTP %d (failed to read body: %w)", resp.StatusCode, readErr)
		}
		return 0, fmt.Errorf("fetching blob returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	parentDir := filepath.Dir(destDir)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return 0, fmt.Errorf("creating parent directory: %w", err)
	}

	tmpExtractDir, err := os.MkdirTemp(parentDir, ".tmp-extract-*")
	if err != nil {
		return 0, fmt.Errorf("creating temporary extract directory: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpExtractDir); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to clean up temporary directory %s: %v", tmpExtractDir, rmErr)
		}
	}()

	// Verify SHA-256 while streaming through gzip and tar
	hasher := sha256.New()
	teeReader := io.TeeReader(resp.Body, hasher)

	gr, err := gzip.NewReader(teeReader)
	if err != nil {
		return 0, fmt.Errorf("initializing gzip reader: %w", err)
	}
	defer func() {
		if closeErr := gr.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close gzip reader: %v", closeErr)
		}
	}()

	tr := tar.NewReader(gr)
	filesExtracted := 0

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("reading tar stream: %w", err)
		}

		cleanPath := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
			return 0, fmt.Errorf("illegal file path in layer tar: %s", header.Name)
		}

		target := filepath.Join(tmpExtractDir, cleanPath)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return 0, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return 0, err
			}
			mode := header.FileInfo().Mode()
			if strings.HasPrefix(cleanPath, "bin/") || strings.Contains(cleanPath, "/bin/") {
				mode |= 0111
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return 0, err
			}
			_, copyErr := io.Copy(outFile, tr)
			closeErr := outFile.Close()
			if copyErr != nil {
				return 0, copyErr
			}
			if closeErr != nil {
				return 0, closeErr
			}
			filesExtracted++
		}
	}

	// Read remaining bytes if any to complete hash calculation
	if _, discardErr := io.Copy(io.Discard, teeReader); discardErr != nil {
		return 0, fmt.Errorf("reading layer stream: %w", discardErr)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualDigest, digest) {
		return 0, fmt.Errorf("layer checksum mismatch: expected %s, got %s", digest, actualDigest)
	}

	// Move verified extracted contents into destDir
	if rmErr := os.RemoveAll(destDir); rmErr != nil && !os.IsNotExist(rmErr) {
		return 0, fmt.Errorf("clearing existing destination directory %s: %w", destDir, rmErr)
	}
	if err := os.Rename(tmpExtractDir, destDir); err != nil {
		return 0, fmt.Errorf("finalizing extracted directory: %w", err)
	}

	return filesExtracted, nil
}

func (c *Client) doWithAuth(ctx context.Context, req *http.Request, registry, repo string) (*http.Response, error) {
	c.mu.Lock()
	cachedToken := c.tokenCache[registry+":"+repo]
	c.mu.Unlock()

	if cachedToken != "" {
		req.Header.Set("Authorization", "Bearer "+cachedToken)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Handle 401 Unauthorized by parsing Www-Authenticate header
	authHeader := resp.Header.Get("Www-Authenticate")
	if closeErr := resp.Body.Close(); closeErr != nil {
		klog.V(2).Infof("failed to close 401 response body: %v", closeErr)
	}

	if authHeader == "" {
		return nil, fmt.Errorf("registry returned 401 Unauthorized without Www-Authenticate header")
	}

	token, err := c.obtainToken(ctx, authHeader, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain bearer token: %w", err)
	}

	c.mu.Lock()
	c.tokenCache[registry+":"+repo] = token
	c.mu.Unlock()

	// Clone request and retry with token
	retryReq := req.Clone(ctx)
	retryReq.Header.Set("Authorization", "Bearer "+token)
	return c.HTTPClient.Do(retryReq)
}

func (c *Client) obtainToken(ctx context.Context, authHeader, repo string) (string, error) {
	// Parse Bearer realm="...",service="...",scope="..."
	authHeader = strings.TrimSpace(authHeader)
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return "", fmt.Errorf("unsupported auth challenge: %s", authHeader)
	}

	params := parseChallengeParams(strings.TrimSpace(authHeader[7:]))
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("missing realm in bearer challenge")
	}

	tokenURL, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("invalid realm URL %q: %w", realm, err)
	}

	q := tokenURL.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	if scope := params["scope"]; scope != "" {
		q.Set("scope", scope)
	} else if repo != "" {
		q.Set("scope", "repository:"+repo+":pull")
	}
	tokenURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return "", err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close token response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		if readErr != nil {
			return "", fmt.Errorf("token request returned HTTP %d (failed to read body: %w)", resp.StatusCode, readErr)
		}
		return "", fmt.Errorf("token request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}

	if tokenResp.Token != "" {
		return tokenResp.Token, nil
	}
	if tokenResp.AccessToken != "" {
		return tokenResp.AccessToken, nil
	}
	return "", fmt.Errorf("token response did not contain token or access_token")
}

func parseChallengeParams(s string) map[string]string {
	params := make(map[string]string)
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if eq := strings.Index(part, "="); eq != -1 {
			key := strings.TrimSpace(part[:eq])
			val := strings.Trim(strings.TrimSpace(part[eq+1:]), "\"")
			params[key] = val
		}
	}
	return params
}
