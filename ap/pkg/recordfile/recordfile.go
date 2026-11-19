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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"k8s.io/klog/v2"
)

const (
	// HeaderSize is the fixed 64-byte header size aligning the record region on a cache line.
	HeaderSize = 64
	// KeySize is the fixed 32-byte key size per record.
	KeySize = 32
)

// Config specifies the configuration for a recordfile format.
type Config struct {
	Magic      [8]byte
	Version    uint16
	RecordSize uint16 // Must be >= KeySize
	// Merge is an optional function to merge existing and pending payloads when keys match.
	// If nil, pending replaces existing.
	Merge func(existing, pending []byte)
}

type pendingRecord struct {
	key     [KeySize]byte
	payload []byte
}

// File represents an in-memory view of a sorted, fixed-size-record file.
type File struct {
	cfg     Config
	data    []byte
	records []byte
	pending []pendingRecord
	mu      sync.RWMutex
}

// New creates a new empty File.
func New(cfg Config) *File {
	return &File{
		cfg: cfg,
	}
}

// LoadInto loads a File in-place into dst from data.
// It returns an error if data is too small, magic/version/record size mismatch,
// or record region is corrupt. Performs zero heap allocations.
func LoadInto(dst *File, data []byte, cfg Config) error {
	if len(data) == 0 {
		*dst = *New(cfg)
		return nil
	}
	if len(data) < HeaderSize {
		return fmt.Errorf("recordfile: data too small (%d bytes, minimum %d)", len(data), HeaderSize)
	}
	if *(*[8]byte)(data[:8]) != cfg.Magic {
		return fmt.Errorf("recordfile: invalid magic: %q", data[:8])
	}
	ver := binary.LittleEndian.Uint16(data[8:10])
	if ver != cfg.Version {
		return fmt.Errorf("recordfile: unsupported version: %d (expected %d)", ver, cfg.Version)
	}
	recSize := binary.LittleEndian.Uint16(data[10:12])
	if recSize != cfg.RecordSize {
		return fmt.Errorf("recordfile: unsupported record size: %d (expected %d)", recSize, cfg.RecordSize)
	}
	recCount := binary.LittleEndian.Uint32(data[12:16])
	records := data[HeaderSize:]
	if len(records)%int(cfg.RecordSize) != 0 || uint32(len(records)/int(cfg.RecordSize)) != recCount {
		return fmt.Errorf("recordfile: corrupt record region: %d bytes for count %d", len(records), recCount)
	}

	dst.cfg = cfg
	dst.data = data
	dst.records = records
	dst.pending = nil
	return nil
}

// Load loads a File from a byte slice containing the file contents.
// Only the header is parsed; the records region is retained as a sub-slice.
// Performs exactly one heap allocation.
func Load(data []byte, cfg Config) (*File, error) {
	f := &File{}
	if err := LoadInto(f, data, cfg); err != nil {
		return nil, err
	}
	return f, nil
}

// Open reads a recordfile from path and loads it. If the file does not exist
// or is corrupt/invalid, it logs at V(2) and returns an empty File without returning an error.
func Open(path string, cfg Config) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(cfg), nil
		}
		return nil, err
	}
	rf, err := Load(data, cfg)
	if err != nil {
		klog.V(2).Infof("recordfile: invalid file %s (%v), treating as empty", path, err)
		return New(cfg), nil
	}
	return rf, nil
}

// Records returns a slice of the records region in memory.
func (f *File) Records() []byte {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.records
}

// Lookup finds the payload for a given key.
// Performs zero heap allocations.
func (f *File) Lookup(key [KeySize]byte) ([]byte, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Check pending list first (from newest to oldest)
	for i := len(f.pending) - 1; i >= 0; i-- {
		if f.pending[i].key == key {
			return f.pending[i].payload, true
		}
	}

	if len(f.records) == 0 {
		return nil, false
	}

	recSize := int(f.cfg.RecordSize)
	n := len(f.records) / recSize
	low, high := 0, n
	for low < high {
		mid := int(uint(low+high) >> 1)
		offset := mid * recSize
		if bytes.Compare(f.records[offset:offset+KeySize], key[:]) < 0 {
			low = mid + 1
		} else {
			high = mid
		}
	}
	if low < n {
		offset := low * recSize
		if bytes.Equal(f.records[offset:offset+KeySize], key[:]) {
			return f.records[offset+KeySize : offset+recSize], true
		}
	}

	return nil, false
}

// Set stores a pending record for the given key and payload.
func (f *File) Set(key [KeySize]byte, payload []byte) error {
	expectedPayloadSize := int(f.cfg.RecordSize - KeySize)
	if len(payload) != expectedPayloadSize {
		return fmt.Errorf("recordfile: payload size %d does not match expected %d", len(payload), expectedPayloadSize)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Check if already in pending
	for i := len(f.pending) - 1; i >= 0; i-- {
		if f.pending[i].key == key {
			if f.cfg.Merge != nil {
				f.cfg.Merge(f.pending[i].payload, payload)
			} else {
				copy(f.pending[i].payload, payload)
			}
			return nil
		}
	}

	// If Merge is configured, check if key is in existing records to merge base payload
	if f.cfg.Merge != nil && len(f.records) > 0 {
		recSize := int(f.cfg.RecordSize)
		n := len(f.records) / recSize
		low, high := 0, n
		for low < high {
			mid := int(uint(low+high) >> 1)
			offset := mid * recSize
			if bytes.Compare(f.records[offset:offset+KeySize], key[:]) < 0 {
				low = mid + 1
			} else {
				high = mid
			}
		}
		if low < n {
			offset := low * recSize
			if bytes.Equal(f.records[offset:offset+KeySize], key[:]) {
				p := make([]byte, len(payload))
				copy(p, f.records[offset+KeySize:offset+recSize])
				f.cfg.Merge(p, payload)
				f.pending = append(f.pending, pendingRecord{
					key:     key,
					payload: p,
				})
				return nil
			}
		}
	}

	p := make([]byte, len(payload))
	copy(p, payload)
	f.pending = append(f.pending, pendingRecord{
		key:     key,
		payload: p,
	})
	return nil
}

// PendingLen returns the count of pending records.
func (f *File) PendingLen() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.pending)
}

// Len returns the count of records stored in the existing record region.
func (f *File) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.cfg.RecordSize == 0 {
		return 0
	}
	return len(f.records) / int(f.cfg.RecordSize)
}

// Encode merges pending records into existing records and produces the full binary representation.
func (f *File) Encode() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.encodeLocked()
}

func (f *File) encodeLocked() ([]byte, error) {
	recSize := int(f.cfg.RecordSize)

	// Sort pending records ascending by key
	slices.SortFunc(f.pending, func(a, b pendingRecord) int {
		return bytes.Compare(a.key[:], b.key[:])
	})

	// Coalesce duplicate pending records
	pending := f.pending
	if len(pending) > 1 {
		dedup := pending[:1]
		for i := 1; i < len(pending); i++ {
			if dedup[len(dedup)-1].key == pending[i].key {
				if f.cfg.Merge != nil {
					f.cfg.Merge(dedup[len(dedup)-1].payload, pending[i].payload)
				} else {
					copy(dedup[len(dedup)-1].payload, pending[i].payload)
				}
			} else {
				dedup = append(dedup, pending[i])
			}
		}
		pending = dedup
	}

	// Two-pointer merge of existing records and sorted pending records
	existingRecords := f.records
	merged := make([]byte, 0, len(existingRecords)+len(pending)*recSize)
	i := 0
	j := 0
	for i < len(existingRecords) && j < len(pending) {
		existKey := existingRecords[i : i+KeySize]
		pendKey := pending[j].key[:]
		cmp := bytes.Compare(existKey, pendKey)
		if cmp < 0 {
			merged = append(merged, existingRecords[i:i+recSize]...)
			i += recSize
		} else if cmp > 0 {
			merged = append(merged, pendKey...)
			merged = append(merged, pending[j].payload...)
			j++
		} else {
			// Keys match
			merged = append(merged, existKey...)
			if f.cfg.Merge != nil {
				mergedPayload := make([]byte, recSize-KeySize)
				copy(mergedPayload, existingRecords[i+KeySize:i+recSize])
				f.cfg.Merge(mergedPayload, pending[j].payload)
				merged = append(merged, mergedPayload...)
			} else {
				// Replacement
				merged = append(merged, pending[j].payload...)
			}
			i += recSize
			j++
		}
	}
	if i < len(existingRecords) {
		merged = append(merged, existingRecords[i:]...)
	}
	for ; j < len(pending); j++ {
		merged = append(merged, pending[j].key[:]...)
		merged = append(merged, pending[j].payload...)
	}

	header := make([]byte, HeaderSize)
	copy(header[0:8], f.cfg.Magic[:])
	binary.LittleEndian.PutUint16(header[8:10], f.cfg.Version)
	binary.LittleEndian.PutUint16(header[10:12], f.cfg.RecordSize)
	recCount := uint32(len(merged) / recSize)
	binary.LittleEndian.PutUint32(header[12:16], recCount)
	// Remaining bytes in header (16..63) remain zero padding

	out := make([]byte, 0, HeaderSize+len(merged))
	out = append(out, header...)
	out = append(out, merged...)
	return out, nil
}

// Save encodes the File and writes it to path atomically using fileutils.WriteFileAtomic.
func (f *File) Save(ctx context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// If no pending changes and file already exists, nothing to save
	if len(f.pending) == 0 && len(f.data) > 0 {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}

	data, err := f.encodeLocked()
	if err != nil {
		return err
	}

	if err := fileutils.WriteFileAtomic(ctx, path, data, 0644); err != nil {
		return err
	}

	f.data = data
	f.records = data[HeaderSize:]
	f.pending = nil
	return nil
}

// DecodeHex32 decodes a 64-character hexadecimal string into dst without heap allocations.
func DecodeHex32(dst *[KeySize]byte, s string) bool {
	if len(s) != KeySize*2 {
		return false
	}
	for i := 0; i < KeySize; i++ {
		hi := fromHexChar(s[i*2])
		lo := fromHexChar(s[i*2+1])
		if hi == 255 || lo == 255 {
			return false
		}
		dst[i] = (hi << 4) | lo
	}
	return true
}

func fromHexChar(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	}
	return 255
}
