# meta arg for final base image
ARG FINAL_IMAGE=docker.io/moby/buildkit:v0.21.1

FROM golang:1.23-alpine3.21 AS build-base

ARG GIT_TAG
ENV VERSION=${GIT_TAG:-dev}

WORKDIR /src

# Install build dependencies 
RUN apk update && \
	apk add --no-cache ca-certificates && \
	update-ca-certificates

# Copy dependency manifests and vendor directory first for better caching
COPY go.mod go.sum ./
COPY vendor vendor

# Copy the rest of the application source code
COPY *.go .
COPY ./pkg ./pkg

# Ensure TARGETARCH and TARGETOS are set if cross-compiling (e.g., via --build-arg)
# Defaulting to arm64/linux if not set
RUN GOARCH=${TARGETARCH:-arm64} GOOS=${TARGETOS:-linux} CGO_ENABLED=0 go build \
	-v \
	-ldflags "-w -s -extldflags '-static' -X main.VERSION=${VERSION}" \
	-tags "osusergo netgo static_build seccomp" \
	-o /usr/local/bin/container-builder-shim

# Final Image
FROM ${FINAL_IMAGE} AS final
LABEL org.opencontainers.image.source=https://github.com/apple/container-builder-shim 
RUN apk add --no-cache ca-certificates
COPY --from=build-base /usr/local/bin/container-builder-shim /usr/local/bin/container-builder-shim

ENTRYPOINT ["/usr/local/bin/container-builder-shim"]
