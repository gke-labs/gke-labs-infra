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

package repo

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
)

var (
	walkCounter int64
	openMu      sync.Mutex
	openCache   = make(map[string]*Repo)
)

// WalkCount returns the total number of filesystem tree walks performed by the repo package.
// Intended for tests to verify the single-walk property.
func WalkCount() int64 {
	return atomic.LoadInt64(&walkCounter)
}

// ResetWalkCount resets the walk counter to zero. Intended for tests.
func ResetWalkCount() {
	atomic.StoreInt64(&walkCounter, 0)
}

// ResetCache clears the in-memory cached repo trees and resets the walk counter. Intended for tests.
func ResetCache() {
	openMu.Lock()
	defer openMu.Unlock()
	openCache = make(map[string]*Repo)
	ResetWalkCount()
}

// Filter is a predicate function for filtering files.
type Filter func(*File) bool

// DirFilter is a predicate function for filtering directories.
type DirFilter func(string) bool

// Option configures repo loading.
type Option func(*options)

type options struct {
	skips   []string
	ignores []string
}

// WithSkip adds skip globs (such as from .ap/go.yaml).
func WithSkip(skips ...string) Option {
	return func(o *options) {
		o.skips = append(o.skips, skips...)
	}
}

// WithIgnore adds ignore patterns.
func WithIgnore(patterns ...string) Option {
	return func(o *options) {
		o.ignores = append(o.ignores, patterns...)
	}
}

// Repo represents an in-memory snapshot of a repository directory tree.
type Repo struct {
	mu sync.RWMutex

	root     string // root directory of this view
	repoRoot string // anchor root of the entire repository

	files       []*File
	filesByPath map[string]*File

	dirs    []string
	dirsSet map[string]bool

	// Precomputed indices for fast queries:
	filesByDir   map[string][]*File // directory path -> files directly within it
	hasTestsDirs map[string]bool    // directory path -> true if dir or any descendant has a test file

	apRoots []string // sorted
	modules []string // sorted

	ignoreList *IgnoreList
}

// Open opens an in-memory snapshot of the repository. If root has already been opened,
// it returns the cached in-memory view without walking the filesystem again.
func Open(root string, opts ...Option) (*Repo, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", root, err)
	}

	openMu.Lock()
	defer openMu.Unlock()

	if r, ok := openCache[absRoot]; ok {
		return r, nil
	}

	r, err := loadRepo(absRoot, opts...)
	if err != nil {
		return nil, err
	}

	openCache[absRoot] = r
	return r, nil
}

// Load loads a fresh in-memory view of the repository by walking root, bypassing the cache.
func Load(root string, opts ...Option) (*Repo, error) {
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for %s: %w", root, err)
	}
	return loadRepo(absRoot, opts...)
}

func loadRepo(root string, opts ...Option) (*Repo, error) {
	var opt options
	for _, o := range opts {
		o(&opt)
	}

	// Combine default ignores with skips and custom ignores
	allIgnores := []string{".git", "vendor", "node_modules"}
	allIgnores = append(allIgnores, opt.skips...)
	allIgnores = append(allIgnores, opt.ignores...)

	ignoreList := NewIgnoreList(allIgnores)

	atomic.AddInt64(&walkCounter, 1)

	var (
		files   []*File
		dirs    []string
		apRoots []string
		modules []string
	)

	// Check if root itself has .ap or go.mod
	if fi, err := os.Stat(filepath.Join(root, ".ap")); err == nil && fi.IsDir() {
		apRoots = append(apRoots, root)
	}
	if fi, err := os.Stat(filepath.Join(root, "go.mod")); err == nil && !fi.IsDir() {
		modules = append(modules, root)
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("failed to determine relative path for %s: %w", path, err)
		}
		if relPath == "." {
			return nil
		}

		relSlash := filepath.ToSlash(relPath)

		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if ignoreList.ShouldIgnore(relSlash, true) {
				return filepath.SkipDir
			}

			dirs = append(dirs, path)
			if d.Name() == ".ap" {
				parent := filepath.Dir(path)
				apRoots = append(apRoots, parent)
			}
			return nil
		}

		if ignoreList.ShouldIgnore(relSlash, false) {
			return nil
		}

		if d.Name() == "go.mod" {
			parent := filepath.Dir(path)
			modules = append(modules, parent)
		}

		files = append(files, &File{
			Path:    path,
			RelPath: relSlash,
			Name:    d.Name(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			// Directory does not exist on disk, return empty repo
			return newRepo(root, root, nil, nil, nil, nil, ignoreList), nil
		}
		return nil, fmt.Errorf("failed to walk repository %s: %w", root, err)
	}

	apRoots = uniqueSorted(apRoots)
	modules = uniqueSorted(modules)

	// Fallback: if no .ap root was discovered anywhere, treat root as the default AP root
	if len(apRoots) == 0 {
		apRoots = []string{root}
	}

	// Classify each file's enclosing APRoot and Module
	for _, f := range files {
		f.apRoot = closestAncestor(f.Path, apRoots)
		f.module = closestAncestor(f.Path, modules)
	}

	return newRepo(root, root, files, dirs, apRoots, modules, ignoreList), nil
}

func newRepo(root, repoRoot string, files []*File, dirs, apRoots, modules []string, ignoreList *IgnoreList) *Repo {
	filesByPath := make(map[string]*File, len(files))
	filesByDir := make(map[string][]*File)
	hasTestsDirs := make(map[string]bool)

	dirsSet := make(map[string]bool, len(dirs)+1)
	dirsSet[root] = true
	for _, d := range dirs {
		dirsSet[d] = true
	}

	for _, f := range files {
		filesByPath[f.Path] = f
		parentDir := filepath.Dir(f.Path)
		filesByDir[parentDir] = append(filesByDir[parentDir], f)

		if f.IsTest() {
			// Mark parent directory and all ancestors up to root as containing tests
			for curr := parentDir; ; {
				hasTestsDirs[curr] = true
				if curr == root || curr == filepath.Dir(curr) {
					break
				}
				curr = filepath.Dir(curr)
			}
		}
	}

	for _, list := range filesByDir {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Path < list[j].Path
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	sort.Strings(dirs)

	return &Repo{
		root:         root,
		repoRoot:     repoRoot,
		files:        files,
		filesByPath:  filesByPath,
		dirs:         dirs,
		dirsSet:      dirsSet,
		filesByDir:   filesByDir,
		hasTestsDirs: hasTestsDirs,
		apRoots:      apRoots,
		modules:      modules,
		ignoreList:   ignoreList,
	}
}

func closestAncestor(path string, ancestors []string) string {
	best := ""
	for _, a := range ancestors {
		if path == a || strings.HasPrefix(path, a+string(filepath.Separator)) {
			if len(a) > len(best) {
				best = a
			}
		}
	}
	return best
}

func uniqueSorted(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	var res []string
	for _, item := range items {
		clean := filepath.Clean(item)
		if !seen[clean] {
			seen[clean] = true
			res = append(res, clean)
		}
	}
	sort.Strings(res)
	return res
}

// Root returns the root path for this view.
func (r *Repo) Root() string {
	return r.root
}

// RepoRoot returns the repository root path.
func (r *Repo) RepoRoot() string {
	return r.repoRoot
}

// Files returns all files matching the given filters.
func (r *Repo) Files(filters ...Filter) []*File {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(filters) == 0 {
		res := make([]*File, len(r.files))
		copy(res, r.files)
		return res
	}
	var res []*File
	for _, f := range r.files {
		match := true
		for _, filter := range filters {
			if filter != nil && !filter(f) {
				match = false
				break
			}
		}
		if match {
			res = append(res, f)
		}
	}
	return res
}

// FilePaths returns the paths of all files matching the given filters.
func (r *Repo) FilePaths(filters ...Filter) []string {
	files := r.Files(filters...)
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths
}

// File looks up a file by absolute path or path relative to the repo root / view root.
func (r *Repo) File(path string) *File {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clean := filepath.Clean(path)
	if f, ok := r.filesByPath[clean]; ok {
		return f
	}
	if !filepath.IsAbs(clean) {
		abs := filepath.Join(r.root, clean)
		if f, ok := r.filesByPath[abs]; ok {
			return f
		}
	}
	return nil
}

// FileExists reports whether a file at the given path exists in this repo tree.
func (r *Repo) FileExists(path string) bool {
	return r.File(path) != nil
}

// TrackFile ensures that a file at path is known to the Repo tree, adding it if not already present.
// If the file is already tracked, TrackFile returns the existing *File.
func (r *Repo) TrackFile(path string) *File {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trackFileLocked(clean)
}

func (r *Repo) trackFileLocked(clean string) *File {
	if f, ok := r.filesByPath[clean]; ok {
		return f
	}
	fi, err := os.Stat(clean)
	if err != nil {
		return nil
	}
	relPath, err := filepath.Rel(r.repoRoot, clean)
	if err != nil {
		return nil
	}
	relSlash := filepath.ToSlash(relPath)
	if r.ignoreList != nil && r.ignoreList.ShouldIgnore(relSlash, false) {
		return nil
	}

	parentDir := filepath.Dir(clean)
	r.ensureDirs(parentDir)

	f := &File{
		Path:      clean,
		RelPath:   relPath,
		Name:      filepath.Base(clean),
		apRoot:    closestAncestor(clean, r.apRoots),
		module:    closestAncestor(clean, r.modules),
		info:      fi,
		infoValid: true,
	}
	r.files = append(r.files, f)
	sort.Slice(r.files, func(i, j int) bool {
		return r.files[i].Path < r.files[j].Path
	})
	r.filesByPath[clean] = f
	if r.filesByDir == nil {
		r.filesByDir = make(map[string][]*File)
	}
	r.filesByDir[parentDir] = append(r.filesByDir[parentDir], f)
	sort.Slice(r.filesByDir[parentDir], func(i, j int) bool {
		return r.filesByDir[parentDir][i].Path < r.filesByDir[parentDir][j].Path
	})

	if f.IsTest() {
		if r.hasTestsDirs == nil {
			r.hasTestsDirs = make(map[string]bool)
		}
		for curr := parentDir; ; {
			r.hasTestsDirs[curr] = true
			if curr == r.root || curr == filepath.Dir(curr) {
				break
			}
			curr = filepath.Dir(curr)
		}
	}

	if f.Name == "go.mod" {
		r.modules = uniqueSorted(append(r.modules, parentDir))
	}
	if f.Name == ".ap" || filepath.Base(parentDir) == ".ap" {
		apCandidate := parentDir
		if f.Name != ".ap" {
			apCandidate = filepath.Dir(parentDir)
		}
		r.apRoots = uniqueSorted(append(r.apRoots, apCandidate))
	}

	return f
}

func (r *Repo) ensureDirs(dir string) {
	if r.dirsSet == nil {
		r.dirsSet = make(map[string]bool)
	}
	for curr := dir; ; {
		if r.dirsSet[curr] {
			break
		}
		r.dirsSet[curr] = true
		r.dirs = append(r.dirs, curr)
		if curr == r.root || curr == filepath.Dir(curr) {
			break
		}
		curr = filepath.Dir(curr)
	}
	sort.Strings(r.dirs)
}

func (r *Repo) untrackFileLocked(clean string) {
	f, ok := r.filesByPath[clean]
	if !ok {
		return
	}
	delete(r.filesByPath, clean)

	for i, file := range r.files {
		if file.Path == clean {
			r.files = append(r.files[:i], r.files[i+1:]...)
			break
		}
	}

	parentDir := filepath.Dir(clean)
	dirFiles := r.filesByDir[parentDir]
	for i, file := range dirFiles {
		if file.Path == clean {
			r.filesByDir[parentDir] = append(dirFiles[:i], dirFiles[i+1:]...)
			break
		}
	}
	if len(r.filesByDir[parentDir]) == 0 {
		delete(r.filesByDir, parentDir)
	}

	if f.IsTest() {
		r.recomputeHasTestsDirs()
	}
}

func (r *Repo) recomputeHasTestsDirs() {
	r.hasTestsDirs = make(map[string]bool)
	for _, f := range r.files {
		if f.IsTest() {
			for curr := filepath.Dir(f.Path); ; {
				r.hasTestsDirs[curr] = true
				if curr == r.root || curr == filepath.Dir(curr) {
					break
				}
				curr = filepath.Dir(curr)
			}
		}
	}
}

// UntrackFile removes path from the repo tree's in-memory snapshot.
// It should be called when a file has been removed or deleted externally outside
// the tree's own write methods, so that subsequent tree queries (e.g. Files,
// File, FileExists) no longer report it as present. To delete a file from both
// disk and the tree, callers should prefer Remove.
func (r *Repo) UntrackFile(path string) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.untrackFileLocked(clean)
}

// Remove is equivalent to calling os.Remove, but will update any Repo state appropriately.
func (r *Repo) Remove(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
		return err
	}
	r.UntrackFile(clean)
	return nil
}

// WriteFile writes content to path atomically. If the file already exists with
// identical content, it is left untouched so mtime is preserved. The repo tree
// is updated to reflect the new file state.
func (r *Repo) WriteFile(path string, content []byte, perm os.FileMode) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	existing, err := os.ReadFile(clean)
	if err == nil && bytes.Equal(existing, content) {
		return nil
	}
	if err := fileutils.WriteFileAtomic(clean, content, perm); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.filesByPath[clean]; ok {
		f.InvalidateCachedInfo()
		return nil
	}
	r.trackFileLocked(clean)
	return nil
}

// WriteFileAtomic writes data to path atomically and ensures the file is tracked in the repo tree.
func (r *Repo) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	if err := fileutils.WriteFileAtomic(clean, data, perm); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.filesByPath[clean]; ok {
		f.InvalidateCachedInfo()
		return nil
	}
	r.trackFileLocked(clean)
	return nil
}

// CopyFile copies a file from src to dst atomically and ensures dst is tracked in the repo tree.
func (r *Repo) CopyFile(src, dst string, perm os.FileMode) error {
	cleanDst := filepath.Clean(dst)
	if !filepath.IsAbs(cleanDst) {
		cleanDst = filepath.Join(r.root, cleanDst)
	}
	if err := fileutils.CopyFile(src, cleanDst, perm); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.filesByPath[cleanDst]; ok {
		f.InvalidateCachedInfo()
		return nil
	}
	r.trackFileLocked(cleanDst)
	return nil
}

// InvalidateAll drops memoized state for every file in the repository tree,
// re-stats lazily, and discovers newly created and deleted files across the entire repo.
func (r *Repo) InvalidateAll() error {
	return r.InvalidateTree(r.root)
}

// InvalidateTree drops memoized state for every file under dir, re-stats lazily,
// and picks up newly created and deleted files.
func (r *Repo) InvalidateTree(dir string) error {
	cleanDir := filepath.Clean(dir)
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Join(r.root, cleanDir)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	dirPrefix := cleanDir + string(filepath.Separator)
	isUnder := func(p string) bool {
		return p == cleanDir || strings.HasPrefix(p, dirPrefix)
	}

	diskFiles := make(map[string]fs.DirEntry)
	diskDirs := make(map[string]bool)

	if fi, err := os.Stat(cleanDir); err == nil && fi.IsDir() {
		diskDirs[cleanDir] = true
		if walkErr := filepath.WalkDir(cleanDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			relPath, err := filepath.Rel(r.repoRoot, path)
			if err != nil {
				return nil
			}
			if relPath == "." {
				return nil
			}
			relSlash := filepath.ToSlash(relPath)
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				if r.ignoreList != nil && r.ignoreList.ShouldIgnore(relSlash, true) {
					return filepath.SkipDir
				}
				diskDirs[path] = true
				return nil
			}
			if r.ignoreList != nil && r.ignoreList.ShouldIgnore(relSlash, false) {
				return nil
			}
			diskFiles[path] = d
			return nil
		}); walkErr != nil {
			return fmt.Errorf("failed to walk %s for invalidation: %w", cleanDir, walkErr)
		}
	}

	// 1. Untrack deleted files and refresh existing files under cleanDir
	var toUntrack []string
	for _, f := range r.files {
		if isUnder(f.Path) {
			if _, ok := diskFiles[f.Path]; !ok {
				toUntrack = append(toUntrack, f.Path)
			} else {
				f.InvalidateCachedInfo()
			}
		}
	}
	for _, path := range toUntrack {
		r.untrackFileLocked(path)
	}

	// 2. Track new files discovered on disk
	for path, d := range diskFiles {
		if _, ok := r.filesByPath[path]; !ok {
			relPath, err := filepath.Rel(r.repoRoot, path)
			if err != nil {
				continue
			}
			parentDir := filepath.Dir(path)
			r.ensureDirs(parentDir)

			f := &File{
				Path:    path,
				RelPath: relPath,
				Name:    d.Name(),
				apRoot:  closestAncestor(path, r.apRoots),
				module:  closestAncestor(path, r.modules),
			}
			r.files = append(r.files, f)
			r.filesByPath[path] = f
			if r.filesByDir == nil {
				r.filesByDir = make(map[string][]*File)
			}
			r.filesByDir[parentDir] = append(r.filesByDir[parentDir], f)

			if d.Name() == "go.mod" {
				r.modules = uniqueSorted(append(r.modules, parentDir))
			}
			if d.Name() == ".ap" {
				r.apRoots = uniqueSorted(append(r.apRoots, parentDir))
			}
		}
	}

	// Sort files
	sort.Slice(r.files, func(i, j int) bool {
		return r.files[i].Path < r.files[j].Path
	})
	for _, list := range r.filesByDir {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Path < list[j].Path
		})
	}

	// 3. Update dirs
	for d := range diskDirs {
		r.ensureDirs(d)
	}
	var activeDirs []string
	for _, d := range r.dirs {
		if isUnder(d) && !diskDirs[d] {
			delete(r.dirsSet, d)
			continue
		}
		activeDirs = append(activeDirs, d)
	}
	r.dirs = activeDirs

	// 4. Recompute test directories
	r.recomputeHasTestsDirs()

	return nil
}

// Track ensures that each file in paths is updated in the Repo tree: adding it
// if newly created, invalidating its cached info if already tracked, or untracking
// it if deleted on disk.
func (r *Repo) Track(paths ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range paths {
		clean := filepath.Clean(path)
		if !filepath.IsAbs(clean) {
			clean = filepath.Join(r.root, clean)
		}
		if f, ok := r.filesByPath[clean]; ok {
			if _, err := os.Stat(clean); err != nil && os.IsNotExist(err) {
				r.untrackFileLocked(clean)
			} else {
				f.InvalidateCachedInfo()
			}
			continue
		}
		r.trackFileLocked(clean)
	}
}

// FilesInDir returns all files located directly within dir (non-recursive).
func (r *Repo) FilesInDir(dir string) []*File {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(r.root, clean)
	}
	files := r.filesByDir[clean]
	res := make([]*File, len(files))
	copy(res, files)
	return res
}

// Dirs returns all directory paths matching the given filters.
func (r *Repo) Dirs(filters ...DirFilter) []string {
	if len(filters) == 0 {
		res := make([]string, len(r.dirs))
		copy(res, r.dirs)
		return res
	}
	var res []string
	for _, d := range r.dirs {
		match := true
		for _, filter := range filters {
			if filter != nil && !filter(d) {
				match = false
				break
			}
		}
		if match {
			res = append(res, d)
		}
	}
	return res
}

// DirExists reports whether the given directory exists in this repo tree.
func (r *Repo) DirExists(path string) bool {
	clean := filepath.Clean(path)
	if r.dirsSet[clean] {
		return true
	}
	if !filepath.IsAbs(clean) {
		abs := filepath.Join(r.root, clean)
		return r.dirsSet[abs]
	}
	return false
}

// Modules returns all Go module root directory paths in this view (sorted).
func (r *Repo) Modules() []string {
	res := make([]string, len(r.modules))
	copy(res, r.modules)
	return res
}

// APRoots returns all AP root directory paths in this view (sorted).
func (r *Repo) APRoots() []string {
	res := make([]string, len(r.apRoots))
	copy(res, r.apRoots)
	return res
}

// APRootFor returns the closest enclosing AP root for the given path.
func (r *Repo) APRootFor(path string) string {
	clean := filepath.Clean(path)
	return closestAncestor(clean, r.apRoots)
}

// ModuleFor returns the closest enclosing Go module root for the given path.
func (r *Repo) ModuleFor(path string) string {
	clean := filepath.Clean(path)
	return closestAncestor(clean, r.modules)
}

// Under returns a subview of the repository rooted at dir.
func (r *Repo) Under(dir string) *Repo {
	cleanDir := filepath.Clean(dir)
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Join(r.root, cleanDir)
	}
	if cleanDir == r.root {
		return r
	}

	prefix := cleanDir + string(filepath.Separator)

	// Collect subDirs efficiently
	var subDirs []string
	if r.dirsSet[cleanDir] {
		subDirs = append(subDirs, cleanDir)
	}
	for _, d := range r.dirs {
		if strings.HasPrefix(d, prefix) {
			subDirs = append(subDirs, d)
		}
	}

	// Collect files directly from the collected subdirectories
	var subFiles []*File
	for _, d := range subDirs {
		subFiles = append(subFiles, r.filesByDir[d]...)
	}

	var subAPRoots []string
	for _, root := range r.apRoots {
		if root == cleanDir || strings.HasPrefix(root, prefix) {
			subAPRoots = append(subAPRoots, root)
		}
	}
	if len(subAPRoots) == 0 {
		subAPRoots = []string{cleanDir}
	}

	var subModules []string
	for _, m := range r.modules {
		if m == cleanDir || strings.HasPrefix(m, prefix) {
			subModules = append(subModules, m)
		}
	}

	return newRepo(cleanDir, r.repoRoot, subFiles, subDirs, subAPRoots, subModules, r.ignoreList)
}

// Glob returns absolute file paths matching the given glob pattern.
func (r *Repo) Glob(pattern string) ([]string, error) {
	var matches []string
	for _, f := range r.files {
		relToView, err := filepath.Rel(r.root, f.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to determine relative path for %s: %w", f.Path, err)
		}
		relSlash := filepath.ToSlash(relToView)

		if strings.Contains(pattern, "/") {
			if MatchPath(pattern, relSlash, false) {
				matches = append(matches, f.Path)
			}
		} else {
			matched, err := filepath.Match(pattern, f.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if matched {
				matches = append(matches, f.Path)
			}
		}
	}
	sort.Strings(matches)
	return matches, nil
}

// HasGoTests reports whether the view (or any subdirectory under dir if specified) contains a _test.go file.
func (r *Repo) HasGoTests(dir ...string) bool {
	target := r.root
	if len(dir) > 0 && dir[0] != "" {
		target = filepath.Clean(dir[0])
		if !filepath.IsAbs(target) {
			target = filepath.Join(r.root, target)
		}
	}
	return r.hasTestsDirs[target]
}

// HasGoFiles reports whether the view (or any subdirectory under dir if specified) contains
// a .go file belonging to this module (excluding subdirectories that are separate Go modules).
func (r *Repo) HasGoFiles(dir ...string) bool {
	view := r
	if len(dir) > 0 && dir[0] != "" {
		view = r.Under(dir[0])
	}
	for _, f := range view.files {
		if strings.HasSuffix(f.Name, ".go") {
			// Ensure it belongs to this module root, not a nested Go module
			if f.Module() == view.root || f.Module() == "" {
				return true
			}
		}
	}
	return false
}

// FindDockerfiles returns all Dockerfile paths under this view that belong to this AP root
// (excluding those belonging to child AP roots).
func (r *Repo) FindDockerfiles() []string {
	var dockerfiles []string
	for _, f := range r.files {
		if f.Name == "Dockerfile" && f.APRoot() == r.root {
			dockerfiles = append(dockerfiles, f.Path)
		}
	}
	sort.Strings(dockerfiles)
	return dockerfiles
}

// FindManifests returns all Kubernetes manifest paths under k8s directories that belong to this AP root
// (excluding those belonging to child AP roots).
func (r *Repo) FindManifests() []string {
	var manifests []string
	for _, f := range r.files {
		if f.APRoot() != r.root {
			continue
		}
		relToView, err := filepath.Rel(r.root, f.Path)
		if err != nil {
			continue
		}
		slash := filepath.ToSlash(relToView)
		parts := strings.Split(slash, "/")
		inK8s := false
		for _, p := range parts[:len(parts)-1] {
			if p == "k8s" {
				inK8s = true
				break
			}
		}
		if !inK8s {
			continue
		}

		ext := filepath.Ext(f.Name)
		if ext == ".yaml" || ext == ".yml" {
			manifests = append(manifests, f.Path)
		}
	}
	sort.Strings(manifests)
	return manifests
}

// Walk walks the directory tree rooted at root over the in-memory repo tree.
func (r *Repo) Walk(root string, ignore *IgnoreList, filter func(path string, info os.FileInfo) bool) ([]string, error) {
	sub := r.Under(root)
	var files []string
	for _, f := range sub.files {
		relPath, err := filepath.Rel(root, f.Path)
		if err != nil {
			continue
		}
		if ignore != nil && ignore.ShouldIgnore(relPath, false) {
			continue
		}
		if filter != nil && !filter(f.Path, f.Info()) {
			continue
		}
		files = append(files, f.Path)
	}
	sort.Strings(files)
	return files, nil
}

// ExpandPaths expands target paths into individual file paths using the in-memory tree snapshot.
func (r *Repo) ExpandPaths(repoRoot string, paths []string, ignore *IgnoreList) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{repoRoot}
	}

	var files []string
	seen := make(map[string]bool)

	for _, p := range paths {
		absPath := p
		if !filepath.IsAbs(p) {
			absPath = filepath.Join(repoRoot, p)
		}
		absPath = filepath.Clean(absPath)

		if f := r.File(absPath); f != nil {
			relPath, err := filepath.Rel(repoRoot, f.Path)
			if err != nil {
				return nil, err
			}
			if ignore != nil && ignore.ShouldIgnore(relPath, false) {
				continue
			}
			if !seen[f.Path] {
				seen[f.Path] = true
				files = append(files, f.Path)
			}
			continue
		}

		if r.DirExists(absPath) || absPath == repoRoot {
			sub := r.Under(absPath)
			for _, f := range sub.files {
				relPath, err := filepath.Rel(repoRoot, f.Path)
				if err != nil {
					return nil, err
				}
				if ignore != nil && ignore.ShouldIgnore(relPath, false) {
					continue
				}
				if !seen[f.Path] {
					seen[f.Path] = true
					files = append(files, f.Path)
				}
			}
		}
	}

	return files, nil
}

// WalkFileView traverses files under dir calling the callback for each file.
func (r *Repo) WalkFileView(dir string, ignore *IgnoreList, callback func(f *File, relPath string) error) error {
	sub := r.Under(dir)
	for _, f := range sub.files {
		relPath, err := filepath.Rel(dir, f.Path)
		if err != nil {
			continue
		}
		if ignore != nil && ignore.ShouldIgnore(relPath, false) {
			continue
		}
		if err := callback(f, relPath); err != nil {
			return err
		}
	}
	return nil
}
