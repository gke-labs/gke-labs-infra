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
	"context"
	"encoding/binary"
	"fmt"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/recordfile"
)

const (
	doneHeaderSize = recordfile.HeaderSize
	doneVersion    = uint16(1)
	doneRecordSize = uint16(36)
	hashSize       = recordfile.KeySize
	flagsSize      = 4
)

var doneMagic = [8]byte{'a', 'p', 'd', 'o', 'n', 'e', '\n', 0}

// Step identifies a build/codestyle step tracked in done.bin flags.
type Step uint8

const (
	StepGofmt Step = iota // bit 0
	StepTidy              // bit 1
)

// DoneConfig is the recordfile configuration for done.bin.
var DoneConfig = recordfile.Config{
	Magic:      doneMagic,
	Version:    doneVersion,
	RecordSize: doneRecordSize,
	Merge: func(existing, pending []byte) {
		f1 := binary.LittleEndian.Uint32(existing)
		f2 := binary.LittleEndian.Uint32(pending)
		binary.LittleEndian.PutUint32(existing, f1|f2)
	},
}

// DoneIndex maintains a flat, sorted, fixed-record index of completed steps
// keyed by raw 32-byte SHA-256 hash. The record region is kept as a raw []byte slice
// starting at offset 64 (cache-line boundary) and searched in place with zero heap allocations per lookup.
type DoneIndex struct {
	rf      recordfile.File
	records []byte
}

// NewDoneIndex creates an empty DoneIndex.
func NewDoneIndex() *DoneIndex {
	idx := &DoneIndex{}
	idx.rf = *recordfile.New(DoneConfig)
	return idx
}

// LoadDone loads a DoneIndex from a byte slice containing the contents of done.bin.
// Only the header is parsed; the records region is retained as a sub-slice starting at byte 64.
// Exactly one allocation occurs when the caller receives the newly allocated *DoneIndex struct.
func LoadDone(data []byte) (*DoneIndex, error) {
	idx := &DoneIndex{}
	if err := recordfile.LoadInto(&idx.rf, data, DoneConfig); err != nil {
		return nil, err
	}
	idx.records = idx.rf.Records()
	return idx, nil
}

// IsDone returns true if the specified step has been completed for the given hash.
// Performs zero heap allocations.
func (idx *DoneIndex) IsDone(step Step, hash string) bool {
	if step >= 32 {
		return false
	}
	var rawHash [hashSize]byte
	if !recordfile.DecodeHex32(&rawHash, hash) {
		return false
	}

	payload, ok := idx.rf.Lookup(rawHash)
	if !ok || len(payload) < 4 {
		return false
	}
	flags := binary.LittleEndian.Uint32(payload)
	return (flags & (uint32(1) << step)) != 0
}

// MarkDone marks a step as completed for the given hash in the pending list.
func (idx *DoneIndex) MarkDone(step Step, hash string) error {
	if step >= 32 {
		return fmt.Errorf("step %d exceeds maximum 31", step)
	}
	var rawHash [hashSize]byte
	if !recordfile.DecodeHex32(&rawHash, hash) {
		return fmt.Errorf("invalid hash %q: expected 64 hex characters", hash)
	}

	var payload [flagsSize]byte
	binary.LittleEndian.PutUint32(payload[:], uint32(1)<<step)
	return idx.rf.Set(rawHash, payload[:])
}

// Encode merges pending records with existing records and encodes the entire done.bin format.
func (idx *DoneIndex) Encode() ([]byte, error) {
	return idx.rf.Encode()
}

// Save writes the updated done.bin atomically using fileutils.WriteFileAtomic.
func (idx *DoneIndex) Save(ctx context.Context, path string) error {
	if err := idx.rf.Save(ctx, path); err != nil {
		return err
	}
	idx.records = idx.rf.Records()
	return nil
}
