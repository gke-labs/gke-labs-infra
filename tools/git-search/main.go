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
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

type options struct {
	repo string
	ref  string
}

func main() {
	opt := &options{}
	cmd := &cobra.Command{
		Use:   "git-search [flags] <regex>",
		Short: "Search for a regex in a remote git repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearch(cmd.Context(), opt, args[0])
		},
	}

	cmd.Flags().StringVar(&opt.repo, "repo", "", "The git repository URL")
	cmd.Flags().StringVar(&opt.ref, "ref", "main", "The git ref to search in")
	_ = cmd.MarkFlagRequired("repo")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runSearch(ctx context.Context, opt *options, needle string) error {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	repoCacheRoot := filepath.Join(cacheDir, "git-search", "repos")
	if err := os.MkdirAll(repoCacheRoot, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	repoHash := fmt.Sprintf("%x", sha256.Sum256([]byte(opt.repo)))
	barePath := filepath.Join(repoCacheRoot, repoHash)

	if _, err := os.Stat(barePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Cloning %s...\n", opt.repo)
		cloneCmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--depth", "1", "--branch", opt.ref, opt.repo, barePath)
		cloneCmd.Stdout = os.Stdout
		cloneCmd.Stderr = os.Stderr
		if err := cloneCmd.Run(); err != nil {
			return fmt.Errorf("failed to clone repo: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Updating %s (ref %s)...\n", opt.repo, opt.ref)
		fetchCmd := exec.CommandContext(ctx, "git", "--git-dir", barePath, "fetch", "origin", opt.ref+":"+opt.ref, "--depth", "1")
		_ = fetchCmd.Run()
	}

	tempDir, err := os.MkdirTemp("", "git-search-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	fmt.Fprintf(os.Stderr, "Checking out %s to %s...\n", opt.ref, tempDir)
	archiveCmd := exec.CommandContext(ctx, "git", "--git-dir", barePath, "archive", opt.ref)
	archiveOut, err := archiveCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create archive pipe: %w", err)
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-x", "-C", tempDir)
	tarCmd.Stdin = archiveOut
	tarCmd.Stderr = os.Stderr

	if err := archiveCmd.Start(); err != nil {
		return fmt.Errorf("failed to start git archive: %w", err)
	}
	if err := tarCmd.Run(); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	if err := archiveCmd.Wait(); err != nil {
		return fmt.Errorf("git archive failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Searching for \"%s\"...\n", needle)
	grepCmd := exec.CommandContext(ctx, "grep", "-E", "-r", "-n", needle, ".")
	grepCmd.Dir = tempDir
	grepCmd.Stdout = os.Stdout
	grepCmd.Stderr = os.Stderr

	err = grepCmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("grep failed: %w", err)
	}

	return nil
}
