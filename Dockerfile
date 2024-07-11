# Build the manager binary
FROM docker.io/golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM docker.io/ubuntu:22.04
WORKDIR /

ENV DOCKER_VERSION=26.1.3
ENV NERDCTL_VERSION=1.7.6

RUN apt-get update && apt-get install -y curl && apt-get clean && \
    curl -fsSL https://download.docker.com/linux/static/stable/$(uname -m)/docker-${DOCKER_VERSION}.tgz | tar -xzC /usr/local/bin --strip-components=1 docker/docker && \
    curl -fsSL https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-$(uname -m | sed 's/x86_64/amd64/g;s/aarch64/arm64/g').tar.gz \
    | tar -zxC /usr/local/bin nerdctl

COPY --from=builder /workspace/manager .

ENTRYPOINT ["/manager"]
