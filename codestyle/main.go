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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gke-labs-infra/codestyle/pkg/fileheaders"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	ctx := klog.NewContext(context.Background(), klog.Background())

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	args := flag.Args()
	if len(args) == 0 {
		return fmt.Errorf("subcommand is required")
	}
	subcommand := args[0]
	args = args[1:]

	switch subcommand {
	case "file-headers":
		options := &fileheaders.Options{}
		options.InitDefaults()
		options.Files = args
		return fileheaders.Run(ctx, options)
	default:
		return fmt.Errorf("unknown subcommand %q", subcommand)
	}
}
