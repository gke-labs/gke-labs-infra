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

package cache

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func makeHexHash(prefix byte, id uint64) string {
	var raw [hashSize]byte
	raw[0] = prefix
	binary.BigEndian.PutUint64(raw[24:32], id)
	return hex.EncodeToString(raw[:])
}

// TestDoneIndex_HeaderRoundTrip tests header encoding and decoding with the fixed 64-byte header.
func TestDoneIndex_HeaderRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "done.bin")

	idx := NewDoneIndex()
	hash1 := makeHexHash(0x11, 1)
	hash2 := makeHexHash(0x22, 2)

	if err := idx.MarkDone(StepGofmt, hash1); err != nil {
		t.Fatalf("MarkDone(StepGofmt) failed: %v", err)
	}
	if err := idx.MarkDone(StepTidy, hash1); err != nil {
		t.Fatalf("MarkDone(StepTidy) failed: %v", err)
	}
	if err := idx.MarkDone(StepGofmt, hash2); err != nil {
		t.Fatalf("MarkDone(StepGofmt) failed: %v", err)
	}

	if err := idx.Save(binPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(data) != doneHeaderSize+2*int(doneRecordSize) {
		t.Fatalf("unexpected file size: %d, expected %d", len(data), doneHeaderSize+2*int(doneRecordSize))
	}

	// Verify header layout
	if !bytes.Equal(data[:8], doneMagic[:]) {
		t.Errorf("magic mismatch: got %q, want %q", data[:8], doneMagic)
	}
	ver := binary.LittleEndian.Uint16(data[8:10])
	if ver != doneVersion {
		t.Errorf("version mismatch: got %d, want %d", ver, doneVersion)
	}
	recSize := binary.LittleEndian.Uint16(data[10:12])
	if recSize != doneRecordSize {
		t.Errorf("recordSize mismatch: got %d, want %d", recSize, doneRecordSize)
	}
	recCount := binary.LittleEndian.Uint32(data[12:16])
	if recCount != 2 {
		t.Errorf("recordCount mismatch: got %d, want 2", recCount)
	}
	// Zero padding in bytes 16..64
	for i := 16; i < 64; i++ {
		if data[i] != 0 {
			t.Errorf("header padding at byte %d not zero: %d", i, data[i])
			break
		}
	}

	loaded, err := LoadDone(data)
	if err != nil {
		t.Fatalf("LoadDone failed: %v", err)
	}

	if !loaded.IsDone(StepGofmt, hash1) {
		t.Errorf("expected IsDone(StepGofmt, hash1) to be true")
	}
	if !loaded.IsDone(StepTidy, hash1) {
		t.Errorf("expected IsDone(StepTidy, hash1) to be true")
	}
	if !loaded.IsDone(StepGofmt, hash2) {
		t.Errorf("expected IsDone(StepGofmt, hash2) to be true")
	}
	if loaded.IsDone(StepTidy, hash2) {
		t.Errorf("expected IsDone(StepTidy, hash2) to be false")
	}
}

// TestDoneIndex_TreatAsEmpty tests that invalid headers return errors in LoadDone
// and are safely treated as empty caches in Manager.load() without failing.
func TestDoneIndex_TreatAsEmpty(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"too small", []byte("short")},
		{"wrong magic", make([]byte, 64)},
		{"wrong version", func() []byte {
			b := make([]byte, 64)
			copy(b[:8], doneMagic[:])
			binary.LittleEndian.PutUint16(b[8:10], 99)
			binary.LittleEndian.PutUint16(b[10:12], doneRecordSize)
			return b
		}()},
		{"wrong record size", func() []byte {
			b := make([]byte, 64)
			copy(b[:8], doneMagic[:])
			binary.LittleEndian.PutUint16(b[8:10], doneVersion)
			binary.LittleEndian.PutUint16(b[10:12], 40)
			return b
		}()},
		{"truncated records", func() []byte {
			b := make([]byte, 64+10)
			copy(b[:8], doneMagic[:])
			binary.LittleEndian.PutUint16(b[8:10], doneVersion)
			binary.LittleEndian.PutUint16(b[10:12], doneRecordSize)
			binary.LittleEndian.PutUint32(b[12:16], 1) // expects 36 bytes, got 10
			return b
		}()},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadDone(tc.data)
			if err == nil {
				t.Errorf("expected LoadDone to fail for %s", tc.name)
			}

			// In Manager.load(), corrupt done.bin must be treated as empty without error
			tmpDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmpDir, "done.bin"), tc.data, 0644); err != nil {
				t.Fatal(err)
			}
			m := &Manager{
				dir:  tmpDir,
				done: NewDoneIndex(),
				log:  slog.New(slog.DiscardHandler),
			}
			if err := m.load(); err != nil {
				t.Fatalf("expected m.load() to succeed (treat as empty) for %s, got: %v", tc.name, err)
			}
			if m.done == nil || len(m.done.records) != 0 {
				t.Errorf("expected empty done cache for %s", tc.name)
			}
		})
	}
}

// TestDoneIndex_MergePending tests merging pending records into existing records:
// new hashes (before, middle, after) and existing hashes with a new bit.
func TestDoneIndex_MergePending(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "done.bin")

	// Create initial records
	hashA := makeHexHash(0x20, 1)
	hashC := makeHexHash(0x40, 3)

	idx := NewDoneIndex()
	if err := idx.MarkDone(StepGofmt, hashA); err != nil {
		t.Fatal(err)
	}
	if err := idx.MarkDone(StepGofmt, hashC); err != nil {
		t.Fatal(err)
	}
	if err := idx.Save(binPath); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDone(data)
	if err != nil {
		t.Fatal(err)
	}

	// Pending additions:
	// hashStart: smaller than hashA (inserted at start)
	// hashA: existing hash, new bit StepTidy
	// hashB: between hashA and hashC
	// hashEnd: larger than hashC (inserted at end)
	hashStart := makeHexHash(0x10, 0)
	hashB := makeHexHash(0x30, 2)
	hashEnd := makeHexHash(0x50, 4)

	if err := loaded.MarkDone(StepTidy, hashStart); err != nil {
		t.Fatal(err)
	}
	if err := loaded.MarkDone(StepTidy, hashA); err != nil {
		t.Fatal(err)
	}
	if err := loaded.MarkDone(StepGofmt, hashB); err != nil {
		t.Fatal(err)
	}
	if err := loaded.MarkDone(StepTidy, hashEnd); err != nil {
		t.Fatal(err)
	}

	// Before save: pending lookups should succeed
	if !loaded.IsDone(StepTidy, hashStart) {
		t.Errorf("pending lookup hashStart failed")
	}
	if !loaded.IsDone(StepGofmt, hashA) || !loaded.IsDone(StepTidy, hashA) {
		t.Errorf("pending lookup hashA failed")
	}
	if !loaded.IsDone(StepGofmt, hashB) {
		t.Errorf("pending lookup hashB failed")
	}
	if !loaded.IsDone(StepTidy, hashEnd) {
		t.Errorf("pending lookup hashEnd failed")
	}

	if err := loaded.Save(binPath); err != nil {
		t.Fatal(err)
	}

	// Reload from disk
	savedData, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadDone(savedData)
	if err != nil {
		t.Fatal(err)
	}

	nRecords := len(reloaded.records) / int(doneRecordSize)
	if nRecords != 5 {
		t.Fatalf("expected 5 records, got %d", nRecords)
	}

	// Verify all records and flags
	if !reloaded.IsDone(StepTidy, hashStart) || reloaded.IsDone(StepGofmt, hashStart) {
		t.Errorf("hashStart flags incorrect")
	}
	// hashA has BOTH StepGofmt and StepTidy
	if !reloaded.IsDone(StepGofmt, hashA) || !reloaded.IsDone(StepTidy, hashA) {
		t.Errorf("hashA expected both gofmt and tidy to be done")
	}
	if !reloaded.IsDone(StepGofmt, hashB) || reloaded.IsDone(StepTidy, hashB) {
		t.Errorf("hashB flags incorrect")
	}
	if !reloaded.IsDone(StepGofmt, hashC) || reloaded.IsDone(StepTidy, hashC) {
		t.Errorf("hashC flags incorrect")
	}
	if !reloaded.IsDone(StepTidy, hashEnd) || reloaded.IsDone(StepGofmt, hashEnd) {
		t.Errorf("hashEnd flags incorrect")
	}
}

// TestDoneIndex_LookupHitMiss tests hit and miss behaviour at both ends of the range,
// within the range, for unknown steps, and for empty caches.
func TestDoneIndex_LookupHitMiss(t *testing.T) {
	// Empty index
	empty := NewDoneIndex()
	dummyHash := makeHexHash(0x10, 1)
	if empty.IsDone(StepGofmt, dummyHash) {
		t.Errorf("expected empty index lookup to return false")
	}

	h1 := makeHexHash(0x20, 10)
	h2 := makeHexHash(0x30, 20)
	h3 := makeHexHash(0x40, 30)

	idx := NewDoneIndex()
	if err := idx.MarkDone(StepGofmt, h1); err != nil {
		t.Fatal(err)
	}
	if err := idx.MarkDone(StepGofmt, h2); err != nil {
		t.Fatal(err)
	}
	if err := idx.MarkDone(StepGofmt, h3); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "done.bin")
	if err := idx.Save(binPath); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDone(data)
	if err != nil {
		t.Fatal(err)
	}

	// Hit at start
	if !loaded.IsDone(StepGofmt, h1) {
		t.Errorf("expected hit at start (h1)")
	}
	// Hit in middle
	if !loaded.IsDone(StepGofmt, h2) {
		t.Errorf("expected hit in middle (h2)")
	}
	// Hit at end
	if !loaded.IsDone(StepGofmt, h3) {
		t.Errorf("expected hit at end (h3)")
	}

	// Miss before start
	hBefore := makeHexHash(0x10, 1)
	if loaded.IsDone(StepGofmt, hBefore) {
		t.Errorf("expected miss before start")
	}
	// Miss between h1 and h2
	hBetween12 := makeHexHash(0x25, 15)
	if loaded.IsDone(StepGofmt, hBetween12) {
		t.Errorf("expected miss between h1 and h2")
	}
	// Miss between h2 and h3
	hBetween23 := makeHexHash(0x35, 25)
	if loaded.IsDone(StepGofmt, hBetween23) {
		t.Errorf("expected miss between h2 and h3")
	}
	// Miss after end
	hAfter := makeHexHash(0x50, 40)
	if loaded.IsDone(StepGofmt, hAfter) {
		t.Errorf("expected miss after end")
	}

	// Miss for unknown step (> 31)
	if loaded.IsDone(Step(99), h2) {
		t.Errorf("expected miss for unknown step")
	}

	// Miss for invalid hash strings
	if loaded.IsDone(StepGofmt, "short") {
		t.Errorf("expected miss for short hash")
	}
	if loaded.IsDone(StepGofmt, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdefZZ") {
		t.Errorf("expected miss for invalid hex chars")
	}
}

func build100kTestData() (binData []byte, jsonData []byte, sampleHashes []string, err error) {
	const count = 100_000
	sampleHashes = make([]string, count)

	idx := NewDoneIndex()
	doneJSON := map[string]map[string]bool{
		"gofmt": make(map[string]bool, count),
		"tidy":  make(map[string]bool, count),
	}

	for i := 0; i < count; i++ {
		var raw [hashSize]byte
		binary.BigEndian.PutUint64(raw[0:8], uint64(i))
		hStr := hex.EncodeToString(raw[:])
		sampleHashes[i] = hStr

		// gofmt on all, tidy on even entries
		if err := idx.MarkDone(StepGofmt, hStr); err != nil {
			return nil, nil, nil, err
		}
		doneJSON["gofmt"][hStr] = true
		if i%2 == 0 {
			if err := idx.MarkDone(StepTidy, hStr); err != nil {
				return nil, nil, nil, err
			}
			doneJSON["tidy"][hStr] = true
		}
	}

	binData, err = idx.Encode()
	if err != nil {
		return nil, nil, nil, err
	}

	jsonData, err = json.Marshal(doneJSON)
	if err != nil {
		return nil, nil, nil, err
	}

	return binData, jsonData, sampleHashes, nil
}

// TestDoneIndex_Allocs_100k verifies that LoadDone performs exactly one allocation
// and IsDone performs zero allocations on a 100k entry dataset.
func TestDoneIndex_Allocs_100k(t *testing.T) {
	binData, _, sampleHashes, err := build100kTestData()
	if err != nil {
		t.Fatalf("build100kTestData failed: %v", err)
	}

	// Load is one allocation
	var loaded *DoneIndex
	loadAllocs := testing.AllocsPerRun(100, func() {
		var lErr error
		loaded, lErr = LoadDone(binData)
		if lErr != nil {
			t.Fatalf("LoadDone failed: %v", lErr)
		}
	})
	if loadAllocs != 1.0 {
		t.Errorf("expected LoadDone to perform exactly 1 allocation, got %f", loadAllocs)
	}

	// Lookup is zero allocations
	midHash := sampleHashes[50_000]
	lookupAllocs := testing.AllocsPerRun(1000, func() {
		if !loaded.IsDone(StepGofmt, midHash) {
			t.Fatalf("expected IsDone(StepGofmt, midHash) to be true")
		}
	})
	if lookupAllocs != 0.0 {
		t.Errorf("expected IsDone to perform 0 allocations, got %f", lookupAllocs)
	}
}

func BenchmarkDoneBin_Load_100k(b *testing.B) {
	binData, _, _, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx, err := LoadDone(binData)
		if err != nil || idx == nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoneBin_Lookup_100k(b *testing.B) {
	binData, _, sampleHashes, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	idx, err := LoadDone(binData)
	if err != nil {
		b.Fatal(err)
	}
	target := sampleHashes[50_000]

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !idx.IsDone(StepGofmt, target) {
			b.Fatal("lookup failed")
		}
	}
}

func BenchmarkDoneJSON_Load_100k(b *testing.B) {
	_, jsonData, _, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var m map[string]map[string]bool
		if err := json.Unmarshal(jsonData, &m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoneJSON_Lookup_100k(b *testing.B) {
	_, jsonData, sampleHashes, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	var m map[string]map[string]bool
	if err := json.Unmarshal(jsonData, &m); err != nil {
		b.Fatal(err)
	}
	target := sampleHashes[50_000]

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !m["gofmt"][target] {
			b.Fatal("lookup failed")
		}
	}
}
