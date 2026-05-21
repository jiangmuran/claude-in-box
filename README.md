<p align="center">
  <img src="assets/banner.png" alt="claude-in-box — portable Claude Code dev environment with sessions, hooks, and a web API" width="800">
</p>

<p align="center">
  <strong>English</strong> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <em>Run Claude Code anywhere. Sandboxed. Multi-session. Programmable. Reachable from any device — even a Raspberry Pi.</em>
</p>

<p align="center">
  <a href="#status"><img src="https://img.shields.io/badge/status-early%20WIP-orange" alt="status: early WIP"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-D97757" alt="MIT licensed"></a>
  <img src="https://img.shields.io/badge/docker-multi--arch-2496ED?logo=docker&logoColor=white" alt="docker multi-arch">
  <img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64%20%7C%20armv7-success" alt="amd64 / arm64 / armv7">
</p>

---

## What is this

`claude-in-box` packages a full on-demand development environment together with [Claude Code](https://www.anthropic.com/claude-code) into a single Docker container, then exposes it as a web service.

You get:

- 📦 a **sandboxed Linux box** preloaded with the languages, tools, and Claude Code itself,
- 🖥️ one or many **virtual-TTY sessions** running inside it, each one a live Claude Code conversation,
- 🪝 **custom hooks** that fire on every lifecycle event (prompt submitted, tool call, stop, …) and can intercept, log, or transform I/O,
- 🌐 **transparent global SOCKS5** support — one env var and every outbound TCP/UDP packet (Claude API, `npm`, `pip`, `apt`, `git`, …) is rerouted, no per-app config,
- 📡 **WebSocket streaming** of every frame of session output, with sequence numbers so clients can resume on reconnect,
- 🔌 **attach from anywhere** — `docker attach` for the whole control plane, or `claude-in-box attach <session>` to dial into a single live session,
- 🔐 **authenticated Web API** — bearer tokens out of the box, per-device tokens, OIDC pluggable,
- 🪶 **lightweight enough for embedded devices** — multi-arch images (amd64 / arm64 / armv7), an API-only "headless" mode that drops the UI, and a tuned base image that boots on a Raspberry Pi.

The point: stop tying Claude Code to one workstation. Put it on a beefy home server, a cheap VPS, or a single-board computer, then use it from anywhere.

## Why

- **Portable.** One container, one image, one command. Spin up identical environments per project, per branch, per experiment.
- **Multi-session.** Drive several Claude Code conversations in parallel without juggling terminal tabs.
- **Programmable.** Hooks are first-class. Plug in your own logging, auth gates, model routing, output transforms.
- **Remote-first.** Designed to be controlled over the network, not assumed to be local.
- **Network-aware.** Transparent SOCKS5 means it works in restricted networks without modifying upstream tools.
- **Auditable.** Every session keeps a structured `transcript.jsonl` of inputs, outputs, and hook events on disk.
- **Embedded-friendly.** Designed to fit on tiny ARM boxes you can hide in a closet.

## Status

Very early. The repo currently holds the project name, logo, and architecture sketch. Code is being written. Star/watch if you want to follow along.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the planned shape of the system.

## Planned architecture (high level)

```
                       ┌──────────────────────────────────────────┐
                       │           claude-in-box container         │
                       │                                          │
  Browser / curl ──▶  ┌┴──────────┐    ┌──────────────────┐      │
  iPad / phone  ──▶  │  web api  │◀──▶│ session manager  │──┐   │
  embedded MCU  ──▶  │ + web ui  │    │  (pty-backed)    │  │   │
  another agent      │  (auth)   │    └──────────────────┘  │   │
                       └┬──────────┘            ▲           ▼   │
                       │       ▲                │     ┌──────────┐│
                       │       │                └────▶│  hooks   ││
                       │       │ ws stream            │ runtime  ││
                       │       │                      └──────────┘│
                       │       │                            │     │
                       │       │            ┌───────────────┴──┐  │
                       │       └────────────┤  session files   │  │
                       │                    │  + structured log│  │
                       │                    └──────────────────┘  │
                       │                                          │
                       │  Claude Code  ◀── pty ──  session N      │
                       │  Claude Code  ◀── pty ──  session 2      │
                       │  Claude Code  ◀── pty ──  session 1      │
                       │              ▲                            │
                       │              │ all outbound traffic       │
                       │     ┌────────┴──────────┐                │
                       │     │ transparent socks5 │   ◀── optional│
                       │     │ (redsocks + nft)   │   PROXY_URL   │
                       │     └────────────────────┘                │
                       └──────────────────────────────────────────┘
```

## Quick start

```bash
# coming soon — placeholder command shape:
docker run -d --name claude-box \
  -p 8080:8080 \
  -e ANTHROPIC_API_KEY=sk-... \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  ghcr.io/jiangmuran/claude-in-box:latest

# open the web UI
open http://localhost:8080

# or attach a live session from a CLI
claude-in-box attach session-abc123

# or attach the whole container (debugging)
docker attach claude-box
```

Headless / embedded mode (no Web UI, smaller image, API-only):

```bash
docker run -d -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

## Roadmap

- [ ] Base Docker image (Debian-slim + Node + Python + Go + Claude Code)
- [ ] Multi-arch build (`linux/amd64`, `linux/arm64`, `linux/arm/v7`)
- [ ] Session manager: spawn / attach / detach / kill PTY-backed Claude Code sessions
- [ ] Input simulator: write into a session's stdin programmatically
- [ ] Hooks runtime: load `~/.claude/hooks.json` + project-level hooks, run them on lifecycle events
- [ ] Transparent SOCKS5 proxy via `redsocks` + nftables (one env var, all traffic redirected)
- [ ] Streaming bridge: WebSocket + SSE with resumable sequence numbers
- [ ] Web API: bearer token auth, per-device tokens, optional OIDC
- [ ] `claude-in-box` CLI: `attach`, `ls`, `new`, `kill`, `logs`, `exec`
- [ ] Web UI: terminal pane, session switcher, hook editor (mobile-responsive)
- [ ] Headless / embedded mode (API-only, no UI assets)
- [ ] Persistent session storage across container restarts
- [ ] Multi-user / multi-tenant mode

## Contributing

Not yet open for contributions — design is still settling. Open an issue if something resonates.

## License

[MIT](LICENSE)
