package commands

import (
	"bytes"
	"regexp"
)

// SplitYAML splits a multi-document YAML file into individual documents.
// It splits on "---" which may be followed by a newline or end of file.
func SplitYAML(data []byte) [][]byte {
	// We want to split by "---" that appears on its own line.
	// The regex (?m)^---$ matches --- on a line by itself.
	re := regexp.MustCompile(`(?m)^---$`) // Note: Using raw string literal here to avoid escaping issues
	indexes := re.FindAllIndex(data, -1)

	if len(indexes) == 0 {
		return [][]byte{data}
	}

	var docs [][]byte
	lastPos := 0
	for _, idx := range indexes {
		// idx[0] is start of match (---), idx[1] is end

		// Content before ---
		if idx[0] > lastPos {
			docs = append(docs, data[lastPos:idx[0]])
		}
		lastPos = idx[1]
	}
	// Content after last ---
	if lastPos < len(data) {
		docs = append(docs, data[lastPos:])
	}

	// Filter empty docs
	var result [][]byte
	for _, doc := range docs {
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) > 0 {
			result = append(result, doc)
		}
	}
	return result
}
