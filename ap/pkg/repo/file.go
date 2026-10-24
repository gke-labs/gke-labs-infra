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
	"crypto/sha256"
	"encoding/hex"
	"go/build/constraint"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var knownOS = map[string]bool{
	"aix":       true,
	"android":   true,
	"darwin":    true,
	"dragonfly": true,
	"freebsd":   true,
	"hurd":      true,
	"illumos":   true,
	"ios":       true,
	"js":        true,
	"linux":     true,
	"nacl":      true,
	"netbsd":    true,
	"openbsd":   true,
	"plan9":     true,
	"solaris":   true,
	"wasip1":    true,
	"windows":   true,
	"zos":       true,
}

var knownArch = map[string]bool{
	"386":         true,
	"amd64":       true,
	"amd64p32":    true,
	"arm":         true,
	"armbe":       true,
	"arm64":       true,
	"arm64be":     true,
	"loong64":     true,
	"mips":        true,
	"mipsle":      true,
	"mips64":      true,
	"mips64le":    true,
	"mips64p32":   true,
	"mips64p32le": true,
	"ppc":         true,
	"ppc64":       true,
	"ppc64le":     true,
	"riscv":       true,
	"riscv64":     true,
	"s390":        true,
	"s390x":       true,
	"sparc":       true,
	"sparc64":     true,
	"wasm":        true,
}

// File represents a file in the repository tree.
type File struct {
	Path    string // Absolute path on disk
	RelPath string // Path relative to view root (forward slash separated)
	Name    string // File base name

	apRoot string // Enclosing AP root directory path
	module string // Enclosing Go module directory path

	mu sync.Mutex

	isGeneratedValid bool
	isGenerated      bool
	generatedHeader  string

	hasGoGenerateValid bool
	hasGoGenerate      bool

	referencedGOOSValid bool
	referencedGOOS      []string

	hash    string
	hashErr error

	metaSize  int64
	metaMtime int64
	metaInode uint64

	infoValid bool
	info      os.FileInfo
}

// Info returns an os.FileInfo representation of the file, resolved lazily.
func (f *File) Info() os.FileInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.infoValid {
		fi, err := os.Stat(f.Path)
		if err != nil {
			f.info = &fileInfo{name: f.Name}
		} else {
			f.info = fi
		}
		f.infoValid = true
	}
	return f.info
}

// APRoot returns the path to the closest enclosing AP root directory.
func (f *File) APRoot() string {
	return f.apRoot
}

// Module returns the path to the closest enclosing Go module root directory.
func (f *File) Module() string {
	return f.module
}

// IsTest reports whether this file is a Go test file.
func (f *File) IsTest() bool {
	return strings.HasSuffix(f.Name, "_test.go")
}

// HasGoGenerate reports whether this file contains a //go:generate directive.
func (f *File) HasGoGenerate() bool {
	if !strings.HasSuffix(f.Name, ".go") {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasGoGenerateValid {
		f.hasGoGenerate = f.scanGoGenerate()
		f.hasGoGenerateValid = true
	}
	return f.hasGoGenerate
}

func (f *File) scanGoGenerate() bool {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return false
	}
	if !bytes.Contains(data, []byte("//go:generate")) {
		return false
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//go:generate") {
			return true
		}
	}
	return false
}

// IsGenerated reports whether this file is generated code.
// A file is generated if any of its first 30 lines contains a comment
// starting with "Code generated" and ending with "DO NOT EDIT."
func (f *File) IsGenerated() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.isGeneratedValid {
		f.isGenerated, f.generatedHeader = f.scanGenerated()
		f.isGeneratedValid = true
	}
	return f.isGenerated
}

// GeneratedHeader returns the matched generated header line if the file is generated.
func (f *File) GeneratedHeader() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.isGeneratedValid {
		f.isGenerated, f.generatedHeader = f.scanGenerated()
		f.isGeneratedValid = true
	}
	return f.generatedHeader
}

func (f *File) scanGenerated() (bool, string) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return false, ""
	}

	checkLen := len(data)
	if checkLen > 4096 {
		checkLen = 4096
	}

	lines := strings.Split(string(data[:checkLen]), "\n")
	if len(lines) > 30 {
		lines = lines[:30]
	}

	for _, line := range lines {
		if isGen, header := IsGeneratedLine(line); isGen {
			return true, header
		}
	}
	return false, ""
}

// IsGeneratedLine reports whether a line indicates generated code.
// It strips leading comment delimiters (//, #, <!--, -->, --, /*, *, */) and
// checks if the line starts with "Code generated" or "Generated by" and ends with "DO NOT EDIT.".
func IsGeneratedLine(line string) (bool, string) {
	trimmed := strings.TrimSpace(line)

	for {
		stripped := false
		if strings.HasPrefix(trimmed, "//") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
			stripped = true
		} else if strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			stripped = true
		} else if strings.HasPrefix(trimmed, "<!--") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "<!--"))
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "-->"))
			stripped = true
		} else if strings.HasPrefix(trimmed, "--") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "--"))
			stripped = true
		} else if strings.HasPrefix(trimmed, "/*") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "/*"))
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "*/"))
			stripped = true
		} else if strings.HasPrefix(trimmed, "*") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
			stripped = true
		}
		if !stripped {
			break
		}
	}

	if (strings.HasPrefix(trimmed, "Code generated") || strings.HasPrefix(trimmed, "Generated by")) && strings.HasSuffix(trimmed, "DO NOT EDIT.") {
		return true, trimmed
	}
	return false, ""
}

// CheckIsGenerated reports whether the file at path contains a generated code header.
func CheckIsGenerated(path string) bool {
	f := &File{Path: path}
	return f.IsGenerated()
}

// Hash returns a hex-encoded SHA-256 hash of the file content, memoized in the tree.
// It uses a fast path keyed on size, mtime (nanoseconds), and inode, re-stating on each
// call and recomputing only when they changed.
func (f *File) Hash() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.computeHashLocked()
}

func (f *File) computeHashLocked() (string, error) {
	fi, err := os.Lstat(f.Path)
	if err != nil {
		f.hash = ""
		f.hashErr = err
		return "", err
	}

	var ino uint64
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = stat.Ino
	}

	mtime := fi.ModTime().UnixNano()
	size := fi.Size()

	if f.hash != "" && f.metaSize == size && f.metaMtime == mtime && f.metaInode == ino {
		return f.hash, nil
	}

	// Content changed; invalidate memoized file attributes
	if f.hash != "" {
		f.invalidateCachedInfoLocked()
	}

	h, err := hashFilePath(f.Path)
	if err != nil {
		f.hash = ""
		f.hashErr = err
		return "", err
	}

	f.metaSize = size
	f.metaMtime = mtime
	f.metaInode = ino
	f.hash = h
	f.hashErr = nil
	return h, nil
}

func hashFilePath(path string) (retHash string, retErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// InvalidateCachedInfo clears memoized metadata, IsGenerated, HasGoGenerate,
// ReferencedGOOS, and Hash, forcing them to re-evaluate on next access.
func (f *File) InvalidateCachedInfo() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidateCachedInfoLocked()
}

func (f *File) invalidateCachedInfoLocked() {
	f.infoValid = false
	f.info = nil
	f.isGeneratedValid = false
	f.isGenerated = false
	f.generatedHeader = ""
	f.hasGoGenerateValid = false
	f.hasGoGenerate = false
	f.referencedGOOSValid = false
	f.referencedGOOS = nil
	f.hash = ""
	f.hashErr = nil
	f.metaSize = 0
	f.metaMtime = 0
	f.metaInode = 0
}

// ReferencedGOOS returns any known GOOS values referenced by this Go file
// via filename suffix or //go:build constraints.
func (f *File) ReferencedGOOS() []string {
	if !strings.HasSuffix(f.Name, ".go") {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.referencedGOOSValid {
		f.referencedGOOS = f.scanReferencedGOOS()
		f.referencedGOOSValid = true
	}
	return f.referencedGOOS
}

func (f *File) scanReferencedGOOS() []string {
	found := make(map[string]bool)

	// 1. Filename suffix matching (following Go build conventions)
	if osTag := goosFromFilename(f.Name); osTag != "" {
		found[osTag] = true
	}

	// 2. Build constraints parsed with go/build/constraint
	data, err := os.ReadFile(f.Path)
	if err != nil {
		if len(found) == 0 {
			return nil
		}
		res := make([]string, 0, len(found))
		for goos := range found {
			res = append(res, goos)
		}
		sort.Strings(res)
		return res
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "package\t") {
			break
		}
		if constraint.IsGoBuild(trimmed) || constraint.IsPlusBuild(trimmed) {
			expr, err := constraint.Parse(trimmed)
			if err != nil {
				continue
			}
			collectConstraintTags(expr, found)
		}
	}

	if len(found) == 0 {
		return nil
	}

	res := make([]string, 0, len(found))
	for goos := range found {
		res = append(res, goos)
	}
	sort.Strings(res)
	return res
}

func goosFromFilename(name string) string {
	if !strings.HasSuffix(name, ".go") {
		return ""
	}
	name = strings.TrimSuffix(name, ".go")
	i := strings.Index(name, "_")
	if i < 0 {
		return ""
	}
	name = name[i:] // ignore everything before first _

	l := strings.Split(name, "_")
	if n := len(l); n > 0 && l[n-1] == "test" {
		l = l[:n-1]
	}
	n := len(l)
	if n >= 2 && knownOS[l[n-2]] && knownArch[l[n-1]] {
		return l[n-2]
	}
	if n >= 1 && knownOS[l[n-1]] {
		return l[n-1]
	}
	return ""
}

func collectConstraintTags(expr constraint.Expr, found map[string]bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		if knownOS[e.Tag] {
			found[e.Tag] = true
		}
	case *constraint.NotExpr:
		collectConstraintTags(e.X, found)
	case *constraint.AndExpr:
		collectConstraintTags(e.X, found)
		collectConstraintTags(e.Y, found)
	case *constraint.OrExpr:
		collectConstraintTags(e.X, found)
		collectConstraintTags(e.Y, found)
	}
}

type fileInfo struct {
	name string
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return 0 }
func (fi *fileInfo) Mode() os.FileMode  { return 0644 }
func (fi *fileInfo) ModTime() time.Time { return time.Time{} }
func (fi *fileInfo) IsDir() bool        { return false }
func (fi *fileInfo) Sys() any           { return nil }
