# syntax=docker/dockerfile:1
#
# Two images from one file:
#   docker build --target server -t khelam-server .
#   docker build --target agent  -t khelam-agent .

# Build on the host's platform and cross-compile, so multi-arch builds
# do not run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
RUN LDFLAGS="-s -w -X github.com/prashunchitkr/khelamgg/internal/version.Version=${VERSION}" && \
    go build -trimpath -ldflags "$LDFLAGS" -o /out/khelam-server ./cmd/server && \
    go build -trimpath -ldflags "$LDFLAGS" -o /out/khelam-agent ./cmd/agent && \
    mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/khelam-agent /usr/local/bin/khelam-agent
# Holds the agent ID, which keys the agent's sticky ports.
COPY --from=build --chown=nonroot:nonroot /out/state /var/lib/khelam
VOLUME /var/lib/khelam
ENTRYPOINT ["/usr/local/bin/khelam-agent", "-id-file", "/var/lib/khelam/agent-id"]

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/khelam-server /usr/local/bin/khelam-server
# Holds the self-signed certificate and sticky port reservations.
COPY --from=build --chown=nonroot:nonroot /out/state /var/lib/khelam
VOLUME /var/lib/khelam
EXPOSE 31000/udp
ENTRYPOINT ["/usr/local/bin/khelam-server", "-state-dir", "/var/lib/khelam"]
