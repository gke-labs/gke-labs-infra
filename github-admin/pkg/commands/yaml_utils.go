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

package commands

import (
	"bytes"
)

// SplitYAML splits a multi-document YAML file into individual documents.
// It splits on "---" which may be followed by a newline or end of file.
func SplitYAML(data []byte) [][]byte {
	// If the file starts with "---" followed by newline, skip it (it's a separator at start)
	startSep := []byte{'-', '-', '-', '\n'}
	if bytes.HasPrefix(data, startSep) {
		data = data[4:]
	} else if bytes.Equal(data, []byte{'-', '-', '-'}) {
		return nil
	}

	// We primarily split by "\n---"
	// This covers standard separators between documents

	midSep := []byte{'\n', '-', '-', '-', '\n'}
	parts := bytes.Split(data, midSep)

	var docs [][]byte
	for i, part := range parts {
		// For the last part, it might end with "\n---" (EOF case)
		// bytes.Split won't catch this because we split by "\n---"
		if i == len(parts)-1 {
			endSep := []byte{'\n', '-', '-', '-'}
			if bytes.HasSuffix(part, endSep) {
				part = part[:len(part)-4]
			}
		}

		// Filter empty docs
		if len(bytes.TrimSpace(part)) > 0 {
			docs = append(docs, part)
		}
	}
	return docs
}
