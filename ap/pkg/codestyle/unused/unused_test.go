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

func TestUnused_GeneratedDefault(t *testing.T) {
	testdata := analysistest.TestData()
	// unused defaults to skipping generated files
	analysistest.Run(t, testdata, Analyzer, "generated_skip")
}

func TestUnused_GeneratedCheckWhenNotSkipped(t *testing.T) {
	testdata := analysistest.TestData()
	Analyzer.Flags.Set("skip-generated", "false")
	defer Analyzer.Flags.Set("skip-generated", "true")
	analysistest.Run(t, testdata, Analyzer, "generated_check")
}
