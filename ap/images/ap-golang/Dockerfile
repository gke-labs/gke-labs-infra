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

FROM golang:1.27rc2-trixie AS builder

WORKDIR /workspace
COPY . .
RUN go build -o /usr/local/bin/ap ./ap

FROM golang:1.27rc2-trixie
COPY --from=builder /usr/local/bin/ap /usr/local/bin/ap

RUN apt-get update && apt-get install -y git ca-certificates curl gnupg && \
    curl -LO --retry 5 "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/$(dpkg --print-architecture)/kubectl" && \
    install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl && \
    rm kubectl && \
    curl -L --retry 5 "https://github.com/moby/buildkit/releases/download/v0.28.0/buildkit-v0.28.0.linux-$(dpkg --print-architecture).tar.gz" -o buildkit.tar.gz && \
    tar -xz -C /usr/local/bin --strip-components=1 bin/buildctl -f buildkit.tar.gz && \
    rm buildkit.tar.gz && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /workspace
ENTRYPOINT ["ap"]
