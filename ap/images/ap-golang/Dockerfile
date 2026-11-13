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
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /usr/local/bin/ap ./ap

FROM golang:1.27.4-trixie
COPY --from=builder /usr/local/bin/ap /usr/local/bin/ap

RUN apt-get update && apt-get install -y git ca-certificates curl gnupg && \
    KUBECTL_VERSION="$(curl -fsSL --retry 5 --retry-all-errors https://dl.k8s.io/release/stable.txt)" && \
    curl -fLO --retry 5 --retry-all-errors "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/$(dpkg --print-architecture)/kubectl" && \
    install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl && \
    rm kubectl && \
    curl -fL --retry 5 --retry-all-errors "https://github.com/moby/buildkit/releases/download/v0.28.0/buildkit-v0.28.0.linux-$(dpkg --print-architecture).tar.gz" -o buildkit.tar.gz && \
    case "$(dpkg --print-architecture)" in \
        amd64) echo "bc5a158c97c1f7ace1af81c375e8c28bd27e6afeb283fc5b3bfb10777f48fc83  buildkit.tar.gz" | sha256sum -c ;; \
        arm64) echo "02a6f8c1ea519b11d312ea2fb2f45cf3a7a34814a0e61d55d2532adb211ce400  buildkit.tar.gz" | sha256sum -c ;; \
        ppc64le) echo "37da59368048c0de15ff2cbfcf91b974871f0bdf9612a4110c28ceb279b1ed5c  buildkit.tar.gz" | sha256sum -c ;; \
        riscv64) echo "f40e6a49f15902212caa661b7dbe4d3dbe1a5d308412512424e973f3d0ef423a  buildkit.tar.gz" | sha256sum -c ;; \
        s390x) echo "ded0ba91d0ebe7ef01b77031c84ba9ec4451767925118f9400e1d3ad55d306fd  buildkit.tar.gz" | sha256sum -c ;; \
        *) echo "unsupported architecture $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac && \
    tar -tzf buildkit.tar.gz >/dev/null && \
    tar -xz -C /usr/local/bin --strip-components=1 bin/buildctl -f buildkit.tar.gz && \
    rm buildkit.tar.gz && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
ENTRYPOINT ["ap"]

