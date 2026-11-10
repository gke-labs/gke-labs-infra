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

FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /usr/local/bin/ap ./ap

FROM golang:1.27.3-trixie

RUN apt-get update && apt-get install -y git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=registry.k8s.io/kubectl:v1.34.1 /bin/kubectl /usr/local/bin/kubectl
COPY --from=moby/buildkit:v0.28.0 /usr/bin/buildctl /usr/local/bin/buildctl
COPY --from=builder /usr/local/bin/ap /usr/local/bin/ap

WORKDIR /workspace
ENTRYPOINT ["ap"]
