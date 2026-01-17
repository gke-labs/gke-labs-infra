package cache

import (
	"os"
	"testing"
)

func TestGetMetadata(t *testing.T) {
	f, err := os.CreateTemp("", "test_metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	meta, err := GetMetadata(f.Name())
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}

	if meta.Path != f.Name() {
		t.Errorf("expected path %s, got %s", f.Name(), meta.Path)
	}

	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if meta.Size != fi.Size() {
		t.Errorf("expected size %d, got %d", fi.Size(), meta.Size)
	}

	// Allow for small differences if any, but they should be exact for UnixNano usually
    // However, if the FS implementation doesn't support nanoseconds, both might be truncated, which is fine as long as they match.
	if meta.Mtime != fi.ModTime().UnixNano() {
		t.Errorf("expected mtime %d, got %d", fi.ModTime().UnixNano(), meta.Mtime)
	}

	if meta.Inode == 0 {
		t.Logf("Warning: Inode is 0, might be expected on some systems but usually not on Linux")
	}
}
