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

package recordfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func testConfig() Config {
	return Config{
		Magic:      [8]byte{'t', 'e', 's', 't', 'm', 'a', 'g', 0},
		Version:    1,
		RecordSize: 36, // 32 key + 4 payload
	}
}

func makeKey(prefix string, id int) [KeySize]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%s-%08d", prefix, id)))
}

func TestRecordFile_HeaderRoundTrip(t *testing.T) {
	cfg := testConfig()
	rf := New(cfg)

	k1 := makeKey("k", 1)
	k2 := makeKey("k", 2)
	p1 := []byte{1, 2, 3, 4}
	p2 := []byte{5, 6, 7, 8}

	if err := rf.Set(k1, p1); err != nil {
		t.Fatal(err)
	}
	if err := rf.Set(k2, p2); err != nil {
		t.Fatal(err)
	}

	data, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	if len(data) != HeaderSize+2*int(cfg.RecordSize) {
		t.Fatalf("expected len %d, got %d", HeaderSize+2*int(cfg.RecordSize), len(data))
	}

	loaded, err := Load(data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("expected 2 records, got %d", loaded.Len())
	}

	res1, ok := loaded.Lookup(k1)
	if !ok || !bytes.Equal(res1, p1) {
		t.Errorf("lookup k1 failed: got %v, ok=%v", res1, ok)
	}
	res2, ok := loaded.Lookup(k2)
	if !ok || !bytes.Equal(res2, p2) {
		t.Errorf("lookup k2 failed: got %v, ok=%v", res2, ok)
	}
}

func TestRecordFile_TreatAsEmptyOnCorruptOrWrongHeader(t *testing.T) {
	cfg := testConfig()

	// 1. Data too small (< 64 bytes)
	_, err := Load([]byte("too small"), cfg)
	if err == nil {
		t.Errorf("expected error on truncated data, got nil")
	}

	// 2. Wrong magic
	rf := New(cfg)
	k := makeKey("k", 1)
	_ = rf.Set(k, []byte{1, 2, 3, 4})
	data, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	wrongMagicData := slicesClone(data)
	wrongMagicData[0] = 'X'
	_, err = Load(wrongMagicData, cfg)
	if err == nil {
		t.Errorf("expected error on wrong magic, got nil")
	}

	// 3. Wrong version
	wrongVerData := slicesClone(data)
	binary.LittleEndian.PutUint16(wrongVerData[8:10], 99)
	_, err = Load(wrongVerData, cfg)
	if err == nil {
		t.Errorf("expected error on wrong version, got nil")
	}

	// 4. Wrong record size
	wrongRecSizeData := slicesClone(data)
	binary.LittleEndian.PutUint16(wrongRecSizeData[10:12], 40)
	_, err = Load(wrongRecSizeData, cfg)
	if err == nil {
		t.Errorf("expected error on wrong record size, got nil")
	}

	// 5. Corrupt record region (extra trailing byte)
	corruptRegionData := append(slicesClone(data), 0xaa)
	_, err = Load(corruptRegionData, cfg)
	if err == nil {
		t.Errorf("expected error on corrupt record region, got nil")
	}

	// 6. Open treats corrupt file as empty without error
	tmpDir := t.TempDir()
	corruptFile := filepath.Join(tmpDir, "corrupt.bin")
	if err := os.WriteFile(corruptFile, wrongMagicData, 0644); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(t.Context(), corruptFile, cfg)
	if err != nil {
		t.Fatalf("expected Open to succeed for corrupt file, got: %v", err)
	}
	if opened.Len() != 0 {
		t.Errorf("expected 0 records for corrupt file, got %d", opened.Len())
	}
}

func TestRecordFile_MergeWithReplacement(t *testing.T) {
	cfg := testConfig()
	rf := New(cfg)

	k1 := [KeySize]byte{1}
	k2 := [KeySize]byte{2}
	k3 := [KeySize]byte{3}

	_ = rf.Set(k1, []byte{10, 0, 0, 0})
	_ = rf.Set(k2, []byte{20, 0, 0, 0})
	_ = rf.Set(k3, []byte{30, 0, 0, 0})

	data, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(data, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Replace k2 with new payload, and insert k4
	k4 := [KeySize]byte{4}
	newP2 := []byte{99, 0, 0, 0}
	p4 := []byte{40, 0, 0, 0}
	if err := loaded.Set(k2, newP2); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Set(k4, p4); err != nil {
		t.Fatal(err)
	}

	// Lookup pending before save
	p2Lookup, ok := loaded.Lookup(k2)
	if !ok || !bytes.Equal(p2Lookup, newP2) {
		t.Fatalf("expected pending replacement lookup to hit new payload: %v, ok=%v", p2Lookup, ok)
	}

	// Save and reload
	mergedData, err := loaded.Encode()
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(mergedData, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Len() != 4 {
		t.Fatalf("expected 4 records after merge, got %d", reloaded.Len())
	}

	// Verify all 4 keys
	if p, ok := reloaded.Lookup(k1); !ok || p[0] != 10 {
		t.Errorf("k1 failed: %v, ok=%v", p, ok)
	}
	if p, ok := reloaded.Lookup(k2); !ok || p[0] != 99 {
		t.Errorf("k2 failed replacement: %v, ok=%v", p, ok)
	}
	if p, ok := reloaded.Lookup(k3); !ok || p[0] != 30 {
		t.Errorf("k3 failed: %v, ok=%v", p, ok)
	}
	if p, ok := reloaded.Lookup(k4); !ok || p[0] != 40 {
		t.Errorf("k4 failed: %v, ok=%v", p, ok)
	}
}

func TestRecordFile_CustomMerge(t *testing.T) {
	cfg := testConfig()
	cfg.Merge = func(existing, pending []byte) {
		f1 := binary.LittleEndian.Uint32(existing)
		f2 := binary.LittleEndian.Uint32(pending)
		binary.LittleEndian.PutUint32(existing, f1|f2)
	}

	rf := New(cfg)
	k := [KeySize]byte{1}
	var p1 [4]byte
	binary.LittleEndian.PutUint32(p1[:], 0x01)
	_ = rf.Set(k, p1[:])

	data, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(data, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Set another flag on same key
	var p2 [4]byte
	binary.LittleEndian.PutUint32(p2[:], 0x02)
	_ = loaded.Set(k, p2[:])

	// Lookup pending before save: should have merged 0x01 | 0x02 = 0x03
	val, ok := loaded.Lookup(k)
	if !ok {
		t.Fatal("lookup failed")
	}
	flags := binary.LittleEndian.Uint32(val)
	if flags != 0x03 {
		t.Fatalf("expected flags 0x03, got 0x%x", flags)
	}

	// Save and reload
	mergedData, err := loaded.Encode()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(mergedData, cfg)
	if err != nil {
		t.Fatal(err)
	}
	val, ok = reloaded.Lookup(k)
	if !ok || binary.LittleEndian.Uint32(val) != 0x03 {
		t.Fatalf("expected flags 0x03 after reload, got 0x%x, ok=%v", binary.LittleEndian.Uint32(val), ok)
	}
}

func TestRecordFile_LookupAtBothEnds(t *testing.T) {
	cfg := testConfig()
	rf := New(cfg)

	// Lowest possible key (after 0)
	var firstKey [KeySize]byte
	firstKey[KeySize-1] = 1

	// Middle key
	var midKey [KeySize]byte
	midKey[0] = 0x80

	// Highest possible key
	var lastKey [KeySize]byte
	for i := range lastKey {
		lastKey[i] = 0xff
	}

	_ = rf.Set(firstKey, []byte{1, 1, 1, 1})
	_ = rf.Set(midKey, []byte{2, 2, 2, 2})
	_ = rf.Set(lastKey, []byte{3, 3, 3, 3})

	data, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(data, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Hits at ends and middle
	if p, ok := loaded.Lookup(firstKey); !ok || p[0] != 1 {
		t.Errorf("firstKey lookup failed: %v, ok=%v", p, ok)
	}
	if p, ok := loaded.Lookup(midKey); !ok || p[0] != 2 {
		t.Errorf("midKey lookup failed: %v, ok=%v", p, ok)
	}
	if p, ok := loaded.Lookup(lastKey); !ok || p[0] != 3 {
		t.Errorf("lastKey lookup failed: %v, ok=%v", p, ok)
	}

	// Miss before first
	var beforeFirst [KeySize]byte
	if _, ok := loaded.Lookup(beforeFirst); ok {
		t.Errorf("expected miss for beforeFirst")
	}

	// Miss between first and mid
	var between [KeySize]byte
	between[0] = 0x40
	if _, ok := loaded.Lookup(between); ok {
		t.Errorf("expected miss for between")
	}

	// Miss after last (not possible with 0xff all, but test key with last byte modified)
	var nearLast [KeySize]byte
	for i := range nearLast {
		nearLast[i] = 0xff
	}
	nearLast[KeySize-1] = 0xfe
	if _, ok := loaded.Lookup(nearLast); ok {
		t.Errorf("expected miss for nearLast")
	}
}

func TestRecordFile_AllocsPerRun(t *testing.T) {
	cfg := testConfig()
	rf := New(cfg)

	var targetKey [KeySize]byte
	copy(targetKey[:], "target-key-000000000000000000000")
	var targetPayload [4]byte
	binary.LittleEndian.PutUint32(targetPayload[:], 0x12345678)
	_ = rf.Set(targetKey, targetPayload[:])

	for i := 0; i < 1000; i++ {
		k := makeKey("key", i)
		var p [4]byte
		binary.LittleEndian.PutUint32(p[:], uint32(i))
		_ = rf.Set(k, p[:])
	}

	binData, err := rf.Encode()
	if err != nil {
		t.Fatal(err)
	}

	var loaded *File
	loadAllocs := testing.AllocsPerRun(100, func() {
		var lErr error
		loaded, lErr = Load(binData, cfg)
		if lErr != nil {
			t.Fatalf("Load failed: %v", lErr)
		}
	})
	if loadAllocs != 1.0 {
		t.Errorf("expected Load to perform exactly 1 allocation, got %f", loadAllocs)
	}

	lookupAllocs := testing.AllocsPerRun(1000, func() {
		p, ok := loaded.Lookup(targetKey)
		if !ok || len(p) != 4 {
			t.Fatalf("lookup failed")
		}
	})
	if lookupAllocs != 0.0 {
		t.Errorf("expected Lookup to perform 0 allocations, got %f", lookupAllocs)
	}
}

func TestRecordFile_SaveAndOpen(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "records.bin")
	cfg := testConfig()

	rf := New(cfg)
	k := makeKey("test", 1)
	_ = rf.Set(k, []byte{1, 2, 3, 4})

	if err := rf.Save(t.Context(), filePath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	opened, err := Open(t.Context(), filePath, cfg)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if opened.Len() != 1 {
		t.Fatalf("expected 1 record, got %d", opened.Len())
	}
	if p, ok := opened.Lookup(k); !ok || p[0] != 1 {
		t.Errorf("Lookup on opened file failed: %v, ok=%v", p, ok)
	}

	// Open non-existent file returns empty file
	nonExistent, err := Open(t.Context(), filepath.Join(tmpDir, "nonexistent.bin"), cfg)
	if err != nil {
		t.Fatalf("Open nonexistent failed: %v", err)
	}
	if nonExistent.Len() != 0 {
		t.Errorf("expected 0 records for nonexistent file, got %d", nonExistent.Len())
	}
}

func TestDecodeHex32(t *testing.T) {
	var dst [KeySize]byte
	validHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !DecodeHex32(&dst, validHex) {
		t.Fatal("DecodeHex32 failed on valid hex")
	}
	if dst[0] != 0x01 || dst[1] != 0x23 || dst[7] != 0xef {
		t.Fatalf("unexpected decoded bytes: %x", dst)
	}

	// Wrong length
	if DecodeHex32(&dst, "abcd") {
		t.Error("expected DecodeHex32 to fail on short hex")
	}

	// Invalid characters
	if DecodeHex32(&dst, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg") {
		t.Error("expected DecodeHex32 to fail on invalid char 'g'")
	}
}

func slicesClone(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func build100kTestData() ([]byte, [][KeySize]byte, Config, error) {
	cfg := testConfig()
	rf := New(cfg)

	keys := make([][KeySize]byte, 100_000)
	for i := 0; i < 100_000; i++ {
		k := makeKey("key", i)
		keys[i] = k
		var p [4]byte
		binary.LittleEndian.PutUint32(p[:], uint32(i))
		_ = rf.Set(k, p[:])
	}

	data, err := rf.Encode()
	return data, keys, cfg, err
}

func BenchmarkRecordFile_Load_100k(b *testing.B) {
	data, _, cfg, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f, err := Load(data, cfg)
		if err != nil || f == nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecordFile_Lookup_100k(b *testing.B) {
	data, keys, cfg, err := build100kTestData()
	if err != nil {
		b.Fatal(err)
	}
	f, err := Load(data, cfg)
	if err != nil {
		b.Fatal(err)
	}

	target := keys[50_000]
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p, ok := f.Lookup(target)
		if !ok || len(p) != 4 {
			b.Fatal("lookup failed")
		}
	}
}
