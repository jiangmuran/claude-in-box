# claude-in-box image.
#
# Multi-stage:
#   1. golang:1.25-bookworm  → cross-compiles the cib control-plane binary.
#   2. debian:bookworm-slim  → the runtime: full dev env (Node, Python, Go,
#      Rust, claude-code) + the cib binary + the entrypoint shim.
#
# Multi-arch: linux/amd64 and linux/arm64. armv7 is intentionally not built;
# the server target is a real machine, not an SBC.
#
# Build:
#   docker buildx build \
#     --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     -t ghcr.io/jiangmuran/claude-in-box:latest \
#     -f docker/Dockerfile \
#     --push .

# ---------- stage 1: build ----------
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

# go.mod first for layer caching; no deps yet so go.sum is absent.
COPY go.mod ./
RUN go mod download

COPY cmd/ ./cmd/

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/cib ./cmd/cib

# ---------- stage 2: runtime ----------
FROM debian:bookworm-slim AS runtime

ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    GOPATH=/home/coder/go \
    PATH=/usr/local/go/bin:/home/coder/go/bin:/usr/local/bin:/usr/bin:/bin

# Base toolchain + transparent-SOCKS5 prerequisites.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git jq sudo gnupg lsb-release \
      build-essential pkg-config \
      python3 python3-pip python3-venv \
      rustc cargo \
      redsocks nftables iproute2 dnsutils \
      tini \
  && rm -rf /var/lib/apt/lists/*

# Node.js LTS (for Claude Code).
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
  && apt-get update && apt-get install -y --no-install-recommends nodejs \
  && rm -rf /var/lib/apt/lists/*

# Recent Go toolchain (Debian ships an older one; this gives users a current Go
# inside the sessions). Selected per target architecture.
ENV GO_VERSION=1.25.7
RUN set -e; \
    case "${TARGETARCH:-amd64}" in \
      amd64) ARCH=amd64 ;; \
      arm64) ARCH=arm64 ;; \
      *) echo "unsupported arch ${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" \
      | tar -C /usr/local -xz

# Claude Code CLI.
RUN npm install -g @anthropic-ai/claude-code \
  && npm cache clean --force

# Non-root user.
RUN useradd -m -s /bin/bash -G sudo coder \
  && printf 'coder ALL=(ALL) NOPASSWD: ALL\n' > /etc/sudoers.d/coder \
  && chmod 0440 /etc/sudoers.d/coder

# Persistent dirs.
RUN install -d -o coder -g coder /workspace \
                                /var/lib/claude-in-box/sessions \
                                /home/coder/.claude

# Control-plane binary + entrypoint scripts.
COPY --from=build /out/cib /usr/local/bin/cib
COPY docker/entrypoint.sh /usr/local/bin/entrypoint
COPY docker/redsocks.conf.template /etc/claude-in-box/redsocks.conf.template
RUN chmod +x /usr/local/bin/entrypoint /usr/local/bin/cib

# Sensible Claude Code env defaults; user can override.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    CIB_MODE="" \
    CIB_PROXY_URL=""

EXPOSE 8080
WORKDIR /workspace

# tini reaps zombies and forwards signals cleanly to the control plane.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint"]
CMD ["cib"]
