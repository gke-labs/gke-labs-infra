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

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
COPY ap/ ap/

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /usr/local/bin/ap-astro-plugin ./ap/images/ap-astro

FROM node:22-bookworm-slim@sha256:e9f565d32859f0cbfb52125eafe8be39390f24dac302dc3cdf566e8ba3ab21ee

WORKDIR /app
COPY ap/images/ap-astro/package.json ap/images/ap-astro/package-lock.json ./
RUN npm ci

# Build in a work directory (/app/work) instead of copying the site over /app.
# Because /app/work is a subdirectory of /app, Node.js and Vite module resolution
# naturally walks up the directory tree to find dependencies installed at image build time
# in /app/node_modules without modifying /app.
ENV TMPDIR=/app/work
ENV PATH="/app/node_modules/.bin:$PATH"
ENV NODE_PATH="/app/node_modules"
ENV ASTRO_TELEMETRY_DISABLED=1

RUN mkdir -p /app/work && ln -s /app/node_modules /node_modules

COPY --from=builder /usr/local/bin/ap-astro-plugin /usr/local/bin/ap-astro-plugin

ENTRYPOINT ["/usr/local/bin/ap-astro-plugin"]
