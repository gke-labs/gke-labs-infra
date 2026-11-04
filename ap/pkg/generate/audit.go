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

package generate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// AuditOptions holds settings for running the audit.
type AuditOptions struct {
	Strict     bool
	DryRun     bool
	AuditPaths []string
}

// AuditFinding records a generated file that lacks a generated header.
type AuditFinding struct {
	RelPath        string
	Generator      string
	Classification string
}

// AuditResult contains the full results of an audit run.
type AuditResult struct {
	Findings             []AuditFinding
	SkippedUncommentable []string
	GeneratorErrors      []string
	CandidateFiles       int
	PerturbedFiles       int
	Duration             time.Duration
}

type candidateFileInfo struct {
	path            string
	relPath         string
	originalContent []byte
	mode            os.FileMode
	marker          string
	file            *sourcetree.File
}

// RunAudit performs the audit to find generated files lacking a generated-file header.
// It executes in a scratch git worktree to avoid mutating the user's working copy.
func RunAudit(ctx context.Context, repoRoot string, scopes []*tasks.APScope, opts AuditOptions) error {
	start := time.Now()

	cfg, err := config.LoadAPConfig(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load AP config: %w", err)
	}

	auditPaths := opts.AuditPaths
	if len(auditPaths) == 0 && cfg != nil && cfg.Generate != nil {
		auditPaths = cfg.Generate.AuditPaths
	}

	// 1. Identify candidate scopes
	var targetScopes []*tasks.APScope
	if len(scopes) > 0 {
		targetScopes = scopes
	}

	// 2. Identify candidate files in live repo
	var r *sourcetree.SourceTree
	if len(targetScopes) > 0 {
		r = targetScopes[0].SourceTree
	}

	candidates, skippedUncommentable, err := findAuditCandidates(repoRoot, r, auditPaths)
	if err != nil {
		return fmt.Errorf("failed to discover audit candidate files: %w", err)
	}

	if opts.DryRun {
		fmt.Printf("[dry-run] Audit mode would perturb %d candidate file(s) across %d scope(s):\n", len(candidates), len(targetScopes))
		for _, cf := range candidates {
			fmt.Printf("  - %s\n", cf.relPath)
		}
		if len(skippedUncommentable) > 0 {
			fmt.Printf("[dry-run] %d file(s) skipped (uncommentable syntax or binary):\n", len(skippedUncommentable))
			for _, p := range skippedUncommentable {
				fmt.Printf("  - %s\n", p)
			}
		}
		return nil
	}

	// 3. Create a scratch worktree (never mutate developer's tree)
	scratchDir, cleanupWorktree, err := createScratchWorktree(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("failed to create scratch worktree: %w", err)
	}
	defer cleanupWorktree()

	// 4. Perturb candidate files in the scratch worktree
	nonceBytes := make([]byte, 8)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	for _, cf := range candidates {
		scratchFilePath := filepath.Join(scratchDir, cf.relPath)
		prefix, suffix, ok := CommentSyntax(scratchFilePath, cf.originalContent)
		if !ok {
			continue
		}

		var marker string
		if suffix != "" {
			marker = fmt.Sprintf("\n%s ap-audit %s%s\n", prefix, nonce, suffix)
		} else {
			marker = fmt.Sprintf("\n%s ap-audit %s\n", prefix, nonce)
		}
		cf.marker = marker

		perturbed := append(bytes.Clone(cf.originalContent), []byte(marker)...)
		if err := os.WriteFile(scratchFilePath, perturbed, cf.mode); err != nil {
			return fmt.Errorf("failed to perturb file %s in scratch worktree: %w", cf.relPath, err)
		}
	}

	// 5. Open repo in scratch tree and discover scopes & generators
	goCfg, err := config.Load(scratchDir)
	if err != nil && !os.IsNotExist(err) {
		klog.V(2).Infof("warning: failed to load config from %s: %v", scratchDir, err)
	}
	var skipPatterns []string
	if goCfg != nil {
		skipPatterns = goCfg.Skip
	}

	scratchRepo, err := sourcetree.Open(scratchDir, sourcetree.WithSkip(skipPatterns...))
	if err != nil {
		return fmt.Errorf("failed to open scratch repo: %w", err)
	}

	scratchScopes, err := discoverScratchScopes(scratchRepo, scratchDir)
	if err != nil {
		return fmt.Errorf("failed to discover AP scopes in scratch tree: %w", err)
	}

	knownGenerators := identifyGenerators(ctx, scratchDir, scratchScopes)

	// 6. Build and run generation tasks in scratch tree with ContinueOnError
	genTasks, err := GenerateTasks(scratchDir, scratchScopes)
	if err != nil {
		return fmt.Errorf("failed to build generation tasks in scratch tree: %w", err)
	}
	allTasks := []tasks.Task{genTasks}
	for _, sc := range scratchScopes {
		formatTasks, err := format.FormatTasks(sc.SourceTree, sc.Dir)
		if err == nil && formatTasks != nil {
			allTasks = append(allTasks, formatTasks)
		}
	}

	var generatorErrors []string
	runErr := tasks.Run(ctx, tasks.NewScope(scratchDir, scratchDir, scratchRepo), allTasks, tasks.RunOptions{
		DryRun:          false,
		ContinueOnError: true,
	})
	if runErr != nil {
		generatorErrors = append(generatorErrors, runErr.Error())
	}

	// 7. Refresh scratch repo to see current state
	if err := scratchRepo.InvalidateAll(); err != nil {
		return fmt.Errorf("failed to refresh scratch repo after generation: %w", err)
	}

	// 8. Classify candidate files
	var findings []AuditFinding
	for _, cf := range candidates {
		scratchFilePath := filepath.Join(scratchDir, cf.relPath)
		currentContent, err := os.ReadFile(scratchFilePath)
		if err != nil {
			// If file was deleted by generation, it was generated
			if os.IsNotExist(err) {
				if !cf.file.IsGenerated() {
					genName := lookupGenerator(scratchFilePath, knownGenerators)
					findings = append(findings, AuditFinding{
						RelPath:        cf.relPath,
						Generator:      genName,
						Classification: "deleted by generator",
					})
				}
			}
			continue
		}

		// Class 1: Still carries marker -> Not generated (or generator not idempotent)
		if cf.marker != "" && bytes.Contains(currentContent, []byte(cf.marker)) {
			continue
		}

		// Marker is gone: generator restored or modified this file!
		var classification string
		if bytes.Equal(currentContent, cf.originalContent) {
			// Class 2: Restored byte-identical
			classification = "restored byte-identical"
		} else {
			// Class 3: Changed to something else (generator consumed perturbed input)
			classification = "output changed from perturbed input"
		}

		// Check if it carries a generated header
		fileObj := scratchRepo.File(cf.relPath)
		hasHeader := false
		if fileObj != nil {
			hasHeader = fileObj.IsGenerated()
		} else {
			hasHeader = sourcetree.CheckIsGenerated(scratchFilePath)
		}

		if !hasHeader {
			genName := lookupGenerator(scratchFilePath, knownGenerators)
			findings = append(findings, AuditFinding{
				RelPath:        cf.relPath,
				Generator:      genName,
				Classification: classification,
			})
		}
	}

	// Sort findings by relative path
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].RelPath < findings[j].RelPath
	})

	elapsed := time.Since(start)

	// 9. Output report
	printAuditReport(findings, skippedUncommentable, generatorErrors, len(candidates), elapsed)

	if opts.Strict && len(findings) > 0 {
		return fmt.Errorf("audit-generated failed: %d generated file(s) lack a generated header (run with --strict)", len(findings))
	}

	return nil
}

func printAuditReport(findings []AuditFinding, skipped []string, genErrors []string, totalCandidates int, elapsed time.Duration) {
	fmt.Printf("\n=== ap generate --audit-generated ===\n")
	fmt.Println("Auditing against HEAD in a scratch worktree (uncommitted changes are not visible).")
	fmt.Printf("Audited %d candidate file(s) in %v.\n", totalCandidates, elapsed.Round(time.Millisecond))

	if len(genErrors) > 0 {
		fmt.Printf("\nWarning: %d generator error(s) occurred on perturbed inputs (treated as 'cannot classify'):\n", len(genErrors))
		for _, ge := range genErrors {
			fmt.Printf("  - %s\n", ge)
		}
	}

	if len(findings) == 0 {
		fmt.Println("\nNo unheadered generated files found. All generated files carry a generated header!")
		return
	}

	fmt.Printf("\nFound %d generated file(s) that do NOT carry a generated-file header:\n", len(findings))
	for _, f := range findings {
		fmt.Printf("  - %s (generator: %s) [%s]\n", f.RelPath, f.Generator, f.Classification)
	}

	if len(skipped) > 0 {
		klog.V(2).Infof("Skipped %d uncommentable or binary file(s)", len(skipped))
	}
}

func findAuditCandidates(repoRoot string, r *sourcetree.SourceTree, auditPaths []string) ([]*candidateFileInfo, []string, error) {
	var auditIgnoreList *sourcetree.IgnoreList
	if len(auditPaths) > 0 {
		auditIgnoreList = sourcetree.NewIgnoreList(auditPaths)
	}

	var candidates []*candidateFileInfo
	var skipped []string

	for _, f := range r.Files() {
		relPath := f.RelPath
		if filepath.IsAbs(relPath) {
			var err error
			relPath, err = filepath.Rel(repoRoot, f.Path)
			if err != nil {
				continue
			}
		}
		relSlash := filepath.ToSlash(relPath)

		// Never touch .git, vendor, or cache dirs
		if strings.HasPrefix(relSlash, ".git/") || strings.Contains(relSlash, "/.git/") {
			continue
		}
		if strings.HasPrefix(relSlash, "vendor/") || strings.Contains(relSlash, "/vendor/") {
			continue
		}
		if strings.HasPrefix(relSlash, ".cache/") || strings.Contains(relSlash, "/.cache/") {
			continue
		}
		if strings.HasPrefix(relSlash, ".ap/cache/") || strings.Contains(relSlash, "/.ap/cache/") {
			continue
		}

		// If auditPaths specified, filter candidate
		if auditIgnoreList != nil && !auditIgnoreList.ShouldIgnore(relSlash, false) {
			continue
		}

		info, err := os.Stat(f.Path)
		if err != nil || info.IsDir() {
			continue
		}

		data, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}

		_, _, canComment := CommentSyntax(f.Path, data)
		if !canComment {
			skipped = append(skipped, relSlash)
			continue
		}

		candidates = append(candidates, &candidateFileInfo{
			path:            f.Path,
			relPath:         relSlash,
			originalContent: data,
			mode:            info.Mode(),
			file:            f,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].relPath < candidates[j].relPath
	})
	sort.Strings(skipped)

	return candidates, skipped, nil
}

func createScratchWorktree(ctx context.Context, repoRoot string) (string, func(), error) {
	scratchDir, err := os.MkdirTemp("", "ap-audit-worktree-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Check if repoRoot is a git repository
	checkGit := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	checkGit.Dir = repoRoot
	isGit := checkGit.Run() == nil

	if isGit {
		cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", scratchDir, "HEAD")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			klog.V(2).Infof("git worktree add failed (%v: %s), falling back to file copy", err, string(out))
			if copyErr := copyDirExcludingGit(repoRoot, scratchDir); copyErr != nil {
				if rmErr := os.RemoveAll(scratchDir); rmErr != nil {
					klog.V(2).Infof("failed to remove scratch dir: %v", rmErr)
				}
				return "", nil, fmt.Errorf("copy fallback failed: %w", copyErr)
			}
			cleanup := func() {
				if rmErr := os.RemoveAll(scratchDir); rmErr != nil {
					klog.V(2).Infof("failed to remove scratch dir: %v", rmErr)
				}
			}
			return scratchDir, cleanup, nil
		}

		cleanup := func() {
			rmCmd := exec.Command("git", "worktree", "remove", "--force", scratchDir)
			rmCmd.Dir = repoRoot
			if rmErr := rmCmd.Run(); rmErr != nil {
				klog.V(2).Infof("git worktree remove failed: %v", rmErr)
			}
			if rmErr := os.RemoveAll(scratchDir); rmErr != nil {
				klog.V(2).Infof("failed to remove scratch dir: %v", rmErr)
			}
		}
		return scratchDir, cleanup, nil
	}

	// Non-git directory (e.g. unit tests)
	if err := copyDirExcludingGit(repoRoot, scratchDir); err != nil {
		if rmErr := os.RemoveAll(scratchDir); rmErr != nil {
			klog.V(2).Infof("failed to remove scratch dir: %v", rmErr)
		}
		return "", nil, fmt.Errorf("failed to copy files to scratch dir: %w", err)
	}
	cleanup := func() {
		if rmErr := os.RemoveAll(scratchDir); rmErr != nil {
			klog.V(2).Infof("failed to remove scratch dir: %v", rmErr)
		}
	}
	return scratchDir, cleanup, nil
}

func copyDirExcludingGit(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == ".git" || strings.HasPrefix(relSlash, ".git/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		return copyFile(path, targetPath, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) (retErr error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	_, err = io.Copy(out, in)
	return err
}

func discoverScratchScopes(r *sourcetree.SourceTree, scratchDir string) ([]*tasks.APScope, error) {
	scope := tasks.NewScope(scratchDir, scratchDir, r.Under(scratchDir))
	buildScripts, err := tasks.FindTaskScripts(scope.SourceTree, scratchDir, tasks.WithPrefix("build-"))
	if err != nil {
		return nil, fmt.Errorf("failed to find build scripts: %w", err)
	}
	scope.BuildTasks = append(scope.BuildTasks, buildScripts...)

	e2eScripts, err := tasks.FindTaskScripts(scope.SourceTree, scratchDir, tasks.WithPrefix("test-e2e"))
	if err != nil {
		return nil, fmt.Errorf("failed to find e2e scripts: %w", err)
	}
	scope.E2ETasks = append(scope.E2ETasks, e2eScripts...)

	return []*tasks.APScope{scope}, nil
}

func identifyGenerators(ctx context.Context, repoRoot string, scopes []*tasks.APScope) map[string]string {
	outputs := make(map[string]string)
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")
	outputs[filepath.Join(presubmitsDir, "ap-verify-generate")] = "verify-generate"
	outputs[filepath.Join(presubmitsDir, "ap-audit-generated")] = "audit-generated"
	outputs[filepath.Join(presubmitsDir, "ap-test")] = "ap-test"
	outputs[filepath.Join(presubmitsDir, "ap-lint")] = "ap-lint"
	outputs[filepath.Join(presubmitsDir, "ap-build")] = "ap-build"
	outputs[filepath.Join(repoRoot, ".github", "workflows", "ci-presubmits.yaml")] = "github-actions"

	entries, err := os.ReadDir(presubmitsDir)
	if err != nil && !os.IsNotExist(err) {
		klog.Warningf("failed to read presubmits dir %s: %v", presubmitsDir, err)
	} else if err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "ap-e2e") {
				outputs[filepath.Join(presubmitsDir, e.Name())] = "ap-e2e"
			}
		}
	}

	var legacyGenerators []string
	for _, sc := range scopes {
		taskScripts, err := tasks.FindTaskScripts(sc.SourceTree, sc.Dir, tasks.WithPrefix("generate-"))
		if err != nil {
			continue
		}
		for _, ts := range taskScripts {
			script, ok := ts.(*tasks.TaskScript)
			if !ok || script.Name == "generate-github-actions" {
				continue
			}
			genName := strings.TrimPrefix(script.Name, "generate-")
			legacyGenerators = append(legacyGenerators, genName)
			cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			cmd := exec.CommandContext(cmdCtx, script.Path, "--print-outputs")
			cmd.Dir = script.Root
			out, err := cmd.Output()
			cancel()
			if err != nil {
				continue
			}
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					p := filepath.Clean(filepath.Join(script.Root, line))
					outputs[p] = genName
				}
			}
		}
	}

	if len(legacyGenerators) == 1 {
		outputs["__sole_legacy__"] = legacyGenerators[0]
	}
	return outputs
}

func lookupGenerator(targetPath string, knownGenerators map[string]string) string {
	cleanPath := filepath.Clean(targetPath)
	if gen, ok := knownGenerators[cleanPath]; ok {
		return gen
	}
	if sole, ok := knownGenerators["__sole_legacy__"]; ok {
		return sole
	}
	return "unknown generator"
}
