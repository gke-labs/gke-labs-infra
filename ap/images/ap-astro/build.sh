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

SRC="$1"
DIR="$2"
OUT="$3"

# Copy the repository source view into /app so node_modules is accessible
# and the build can write caches and resolve dependencies hermetically.
cp -r "${SRC}/." /app/

SITE_DIR="/app/${DIR}"
if [ ! -d "${SITE_DIR}" ]; then
  echo "site directory ${SITE_DIR} does not exist" >&2
  exit 1
fi

cd "${SITE_DIR}"

/app/node_modules/.bin/astro build --outDir "${OUT}/dist"
