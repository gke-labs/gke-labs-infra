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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestUnused(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, NewAnalyzer(Config{}), "unused_test")
}

func TestUnusedParameters(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, NewAnalyzer(Config{CheckParameters: true}), "unused_params")
}

func TestUnused_GeneratedDefault(t *testing.T) {
	testdata := analysistest.TestData()
	// unused defaults to skipping generated files
	p, err := fileset.NewPolicy(true, false, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, NewAnalyzer(Config{Policy: p}), "generated_skip")
}

func TestUnused_GeneratedCheckWhenNotSkipped(t *testing.T) {
	testdata := analysistest.TestData()
	p, err := fileset.NewPolicy(false, false, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	analysistest.Run(t, testdata, NewAnalyzer(Config{Policy: p}), "generated_check")
}
