# claude-in-box image.
#
# Multi-stage:
#   1. golang:1.25-bookworm   cross-compiles the cib control-plane binary.
#   2. debian:bookworm-slim   the runtime: a complete dev environment
#      (Node, Python + FastAPI, Go, Rust, claude-code), bundled services
#      (nginx, Redis, PostgreSQL, Docker CLI), and common dev tools
#      (ripgrep, fd, bat, htop, tmux, vim, nano, openssh-client, ...) plus
#      the cib control-plane binary and entrypoint.
#
# Services do not auto-start. Set CIB_SERVICES=redis,postgres,nginx (or any
# subset) at run-time and the entrypoint will bring them up before exec'ing
# the control plane. See docker/cib-services.sh.
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

# ---------- stage 1a: web build ----------
FROM --platform=$BUILDPLATFORM node:20-bookworm-slim AS web-build
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund --prefer-offline
COPY web/ ./
RUN npm run build

# ---------- stage 1b: go build ----------
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Drop the freshly built UI into the go:embed target before compiling.
COPY --from=web-build /web/dist/ ./internal/server/webdist/

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
    PATH=/usr/local/go/bin:/home/coder/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Core tools + transparent-SOCKS5 prerequisites + Python + Rust toolchain.
# `passwd` is required for `useradd` (debian-slim strips it from the base).
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git jq sudo gnupg lsb-release \
      passwd adduser \
      build-essential pkg-config make \
      python3 python3-pip python3-venv \
      rustc cargo \
      redsocks nftables iproute2 dnsutils netcat-openbsd \
      tini \
  && rm -rf /var/lib/apt/lists/*

# Common dev tools: search, paging, editors, multiplexers, file utils.
# bat ships as `batcat` on Debian and fd-find ships as `fdfind`; we symlink
# both to their conventional names for parity with other distros.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ripgrep fd-find bat htop tmux \
      vim nano less file tree unzip zip openssh-client \
      ca-certificates curl \
  && ln -sf /usr/bin/batcat   /usr/local/bin/bat \
  && ln -sf /usr/bin/fdfind   /usr/local/bin/fd \
  && rm -rf /var/lib/apt/lists/*

# Bundled services: web (nginx), cache (redis), database (postgres),
# container CLI+daemon (docker.io). Services do not auto-start; the
# entrypoint conditionally brings them up based on CIB_SERVICES.
RUN apt-get update && apt-get install -y --no-install-recommends \
      nginx-light \
      redis-server \
      postgresql postgresql-client \
      docker.io \
  && rm -rf /var/lib/apt/lists/*

# Python stack via Debian packages so we honour PEP 668 cleanly.
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3-fastapi python3-uvicorn python3-pydantic \
      python3-httpx python3-requests python3-pytest \
      python3-rich ipython3 \
  && rm -rf /var/lib/apt/lists/*

# Node.js LTS (for Claude Code itself and for users). 22.x is the
# current Active LTS (started 2024-10); 20.x is in Maintenance through
# 2026-04 only and many tools have started warning about it.
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
  && apt-get update && apt-get install -y --no-install-recommends nodejs \
  && rm -rf /var/lib/apt/lists/*

# Recent Go toolchain (Debian ships an older one). Selected per target arch.
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
RUN useradd -m -s /bin/bash -G sudo,docker coder \
  && printf 'coder ALL=(ALL) NOPASSWD: ALL\n' > /etc/sudoers.d/coder \
  && chmod 0440 /etc/sudoers.d/coder

# Persistent dirs.
RUN install -d -o coder -g coder /workspace \
                                /var/lib/claude-in-box/sessions \
                                /home/coder/.claude

# Control-plane binary + entrypoint and service helpers.
COPY --from=build /out/cib /usr/local/bin/cib
COPY docker/entrypoint.sh    /usr/local/bin/entrypoint
COPY docker/cib-services.sh  /usr/local/bin/cib-services
COPY docker/redsocks.conf.template /etc/claude-in-box/redsocks.conf.template
COPY docker/nginx.default.conf     /etc/claude-in-box/nginx.default.conf
COPY docker/motd                   /etc/motd
COPY docker/bash.bashrc.cib        /etc/profile.d/cib.sh
RUN chmod +x /usr/local/bin/entrypoint /usr/local/bin/cib /usr/local/bin/cib-services \
 && chmod 0644 /etc/profile.d/cib.sh /etc/motd \
 && printf '\n# claude-in-box additions\n[ -r /etc/profile.d/cib.sh ] && . /etc/profile.d/cib.sh\n' >> /etc/bash.bashrc \
 && ln -sf /etc/profile.d/cib.sh /root/.bashrc

# Sensible Claude Code env defaults; user can override.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    CIB_MODE="" \
    CIB_PROXY_URL="" \
    CIB_SERVICES=""

EXPOSE 8080
WORKDIR /workspace

# tini reaps zombies and forwards signals cleanly to the control plane.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint"]
CMD ["cib"]
