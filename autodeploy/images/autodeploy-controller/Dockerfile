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

FROM golang:1.26.6-trixie AS builder

WORKDIR /workspace
COPY . .

# Build autodeploy
RUN go build -o /usr/local/bin/autodeploy .

FROM golang:1.26.6-trixie
COPY --from=builder /usr/local/bin/autodeploy /usr/local/bin/autodeploy

# autodeploy needs git to clone repos
RUN apt-get update && apt-get install -y git ca-certificates curl gnupg && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
ENTRYPOINT ["autodeploy"]
