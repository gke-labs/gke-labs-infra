#!/bin/sh
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

case "$1" in
  describe)
    cat <<'EOF'
{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "astro",
  "activation": [
    "**/astro.config.*"
  ],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build Astro documentation site",
      "outputs": [
        "dist"
      ]
    }
  ]
}
EOF
    exit 0
    ;;
  run)
    shift
    task="$1"
    shift

    if [ "$task" != "build" ]; then
      echo "unknown task: $task" >&2
      exit 1
    fi

    src=""
    out=""
    dir=""

    while [ $# -gt 0 ]; do
      case "$1" in
        --src)
          src="$2"
          shift 2
          ;;
        --out)
          out="$2"
          shift 2
          ;;
        --dir)
          dir="$2"
          shift 2
          ;;
        *)
          echo "unknown flag: $1" >&2
          exit 1
          ;;
      esac
    done

    if [ -z "$src" ] || [ -z "$out" ] || [ -z "$dir" ]; then
      echo "missing required flags: --src, --out, and --dir must all be specified" >&2
      exit 1
    fi

    exec /build.sh "$src" "$dir" "$out"
    ;;
  *)
    echo "unknown command: $1" >&2
    exit 1
    ;;
esac
