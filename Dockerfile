# syntax=docker/dockerfile:1
# Multi-stage build for am-proxy (linux/amd64).
# Podman: podman build -t am-proxy .
# aarch64 / arm64 is a future target; build with --platform linux/amd64 today.

ARG GO_VERSION=1.22
ARG TARGETPLATFORM=linux/amd64

# ---- build ----
FROM --platform=$TARGETPLATFORM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/am-proxy ./cmd/am-proxy

# ---- runtime (RHEL-friendly UBI minimal) ----
FROM --platform=$TARGETPLATFORM registry.access.redhat.com/ubi9/ubi-minimal:latest

LABEL org.opencontainers.image.title="am-proxy" \
      org.opencontainers.image.description="Prometheus → Alertmanager routing proxy with custody queues" \
      org.opencontainers.image.source="https://github.com/SethDS/am-proxy"

RUN microdnf install -y ca-certificates shadow-utils \
    && microdnf clean all \
    && mkdir -p /etc/am-proxy /config \
    && useradd --uid 10001 --create-home --home-dir /home/am-proxy --shell /sbin/nologin am-proxy \
    && chown -R am-proxy:am-proxy /etc/am-proxy /config

COPY --from=build --chown=am-proxy:am-proxy /out/am-proxy /usr/local/bin/am-proxy
COPY --chown=am-proxy:am-proxy examples/container/am-proxy.yml /etc/am-proxy/am-proxy.yml

USER 10001:10001
WORKDIR /home/am-proxy

# Default listen port (config listen / --listen-address).
EXPOSE 9094

# Prefer mounted config at /etc/am-proxy/am-proxy.yml (or /config/am-proxy.yml).
ENTRYPOINT ["/usr/local/bin/am-proxy"]
CMD ["--config.file=/etc/am-proxy/am-proxy.yml"]
