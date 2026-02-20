// Copyright 2026 Google LLC
package unused

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestUnused(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "unused_test")
}

func TestUnusedParameters(t *testing.T) {
	testdata := analysistest.TestData()
	Analyzer.Flags.Set("check-parameters", "true")
	defer Analyzer.Flags.Set("check-parameters", "false")
	analysistest.Run(t, testdata, Analyzer, "unused_params")
}
