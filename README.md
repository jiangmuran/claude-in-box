<p align="center">
  <img src="assets/banner.png" alt="claude-in-box — portable Claude Code dev environment with sessions, hooks, and a web API" width="800">
</p>

<p align="center">
  <strong>English</strong> &middot; <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <em>Run Claude Code anywhere. One container. Web-managed. Reachable from any device, down to a microcontroller.</em>
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

What you get:

- a sandboxed Linux box preloaded with the languages, tools, and Claude Code itself;
- one or many virtual-TTY sessions running inside it, each one a live Claude Code conversation in bypass-permission mode (the container is the sandbox, so per-tool prompts are unnecessary friction);
- structured event streaming: text deltas, tool calls, todo updates, token usage, status changes, stop reasons, model metadata, all available as JSON frames over WebSocket or SSE;
- session lifecycle controls from the web UI: create, attach, resume, kill, switch models on the fly;
- two ways to bill Claude: log in with your Anthropic subscription, or paste an API key;
- a Web UI plus a REST/WebSocket API, both gated by API-key auth;
- a rich set of transports designed for very different clients, from a phone browser down to an STM32: HTTPS, WebSocket over TLS, HTTP with AES-GCM envelope encryption, SSE, MQTT (planned), each with its own auth and crypto shape;
- a transparent SOCKS5 layer so every outbound packet from inside the box can be rerouted through one upstream proxy without per-tool config;
- programmable hooks on every lifecycle event;
- multi-arch images, a headless flavor that drops the UI, and tuning small enough for a Raspberry Pi or N100 mini PC.

The point: stop tying Claude Code to one workstation. Put it on a beefy home server, a cheap VPS, or a single-board computer, then use it from anywhere with the transport that fits the device.

## The ideal workflow

```
1.  Pick an environment image: prebuilt or your own custom one.
2.  Forward the port (8080 by default).
3.  docker run — the container boots the control plane and waits for auth.
4.  Open the web panel. Authenticate with the master API key minted at boot.
5.  Choose how to talk to Claude: log in with your claude.ai subscription,
    or paste an Anthropic API key. The choice is per-session.
6.  Dashboard shows: live sessions, token consumption, wall-clock work time,
    current model, hook activity.
7.  Create a new session. You drop into a virtual-TTY pane running Claude Code
    in bypass-permission mode. Or resume an earlier session from its
    transcript and pick up exactly where it stopped.
8.  Talk to Claude. Switch models mid-flight. Watch todos, tool calls, and
    status update live in side panels rendered from the structured event
    stream, not just the raw terminal.
9.  From a phone, tablet, embedded MCU, or another agent, hit the same
    sessions over the transport that suits the device.
```

That is the loop the rest of this README is here to explain.

## Capabilities

### Sessions and Claude Code

| Capability | Notes |
|------------|-------|
| Multi-session | PTY-backed; spawn, attach, detach, kill, list. Multiple clients can attach to the same session simultaneously. |
| Bypass-permission mode | Default. Claude Code runs with `--dangerously-skip-permissions` because the container is the security boundary, not the per-tool prompt. Can be turned off per session. |
| Resume | Every session keeps an append-only `transcript.jsonl`. `POST /api/sessions { resume: <session_id> }` brings it back with full history. |
| Model switching | `POST /api/sessions/:id/model { model }` switches mid-flight, e.g. `claude-opus-4-7`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001`. |
| Input simulation | `POST /api/sessions/:id/input` writes raw bytes (or text frames) into the session's stdin. The same primitive backs both human typing and automation. |
| Detached / headless | Sessions survive client disconnects. Reconnect with `?from=<seq>` to replay missed frames. |

### Claude authentication (per session)

| Mode | When to use | How |
|------|-------------|-----|
| Anthropic subscription | You already pay for Claude.ai Pro/Max and want it billed there. | Web UI guides you through the standard `claude login` flow inside the container. Credentials live in `~/.claude/` on a mounted volume. |
| API key | Programmatic, CI, multi-user, or paying per token. | Paste `sk-ant-...` in the UI or set `ANTHROPIC_API_KEY` on the container. |
| Mixed | Two sessions, two billing modes. | Each session declares its own auth mode at create time. |

### Structured event stream

The streaming bridge does not just relay terminal bytes. It parses Claude Code's lifecycle into typed frames that any client can render without screen-scraping. Every frame carries `session`, `seq`, `ts`.

| Frame type | Emitted when | Payload fields |
|------------|--------------|----------------|
| `text.delta` | Assistant text streams | `text` |
| `thinking` | Extended-thinking block | `text` (optional, gated by config) |
| `tool.use.start` | Tool invocation begins | `tool`, `input` |
| `tool.use.result` | Tool returns | `tool`, `output`, `error?`, `duration_ms` |
| `todo.update` | TodoWrite / TodoUpdate fires | `items: [{ id, subject, status, activeForm? }]` |
| `ask.question` | Model asks the user to pick | `prompt`, `options[]`, `multiSelect` |
| `usage` | End of turn | `input`, `output`, `cache_read`, `cache_write` |
| `status` | Session state changes | `state` in `idle / working / waiting_for_input / stopped`, `elapsed_ms` |
| `stop` | Turn or session ends | `reason` |
| `meta` | Model or config changes | `model`, `workdir`, … |
| `hook` | A user hook fired | `name`, `event`, `payload`, `result?` |
| `pty.raw` | Optional opaque PTY bytes | `data` (off by default, on for terminal-style clients) |

Clients pick which frames they care about: a phone dashboard probably wants `todo.update`, `usage`, `status`, `stop`; a terminal emulator wants `pty.raw`; a watchdog wants only `status` and `stop`.

### Hooks

Hooks are first-class. User scripts fire on every lifecycle event listed in the table above and can rewrite, block, or annotate. Hook config is merged from image-level (`/etc/claude-in-box/hooks.json`), user-level (`~/.claude/hooks.json`), and per-session declarations.

### Web API and transports

Each capability ships across multiple transports so that very different devices can use the same backend. Pick the one that fits the device.

| Transport | Best for | Crypto | Auth | Streaming | Status |
|-----------|----------|--------|------|-----------|--------|
| HTTPS / WSS | Browser, phone, server | TLS | Bearer token | yes (WSS) | planned, first target |
| HTTP + AES envelope | Bare-metal MCU (ESP32, STM32), no TLS stack | AES-256-GCM per-device key | API key + per-request nonce | request/response only | planned |
| SSE | Cheap one-way clients, log tailers | TLS or AES envelope | Bearer / API key | yes (one-way) | planned |
| WebSocket (no TLS) over private LAN | Trusted LAN clients, dev | optional AES envelope | Bearer | yes | planned |
| MQTT bridge | IoT bus integrations | TLS or pre-shared | per topic | yes | roadmap |
| Raw TCP framed | Absolute minimum footprint | AES-GCM | API key | yes | roadmap |

For HTTPS deployments we ship an [nginx template](deploy/nginx.conf.template) that terminates TLS, proxies the REST surface, upgrades WebSocket connections, and forwards client IPs.

For the embedded HTTP transport we ship a small protocol spec, [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md), so device firmware authors can implement it in a few hundred lines with any AES-GCM library.

### Auth on the control plane

- A master API key is minted at container boot via `CIB_AUTH_TOKEN`. The control plane refuses to start without one (override only for local dev).
- Device tokens can be issued via the API. Each has a label, scope set, and optional TTL. Revocable independently.
- WebSocket auth travels in the `Sec-WebSocket-Protocol` subprotocol header to keep tokens out of URL logs.
- OIDC is planned via a fronting reverse proxy (oauth2-proxy / authelia). The control plane honors `X-Forwarded-User`.

### Network: transparent SOCKS5

Set `CIB_PROXY_URL=socks5://user:pass@host:port` once at boot and every outbound TCP (and UDP through `tun2socks` where supported) from inside the box is redirected through that proxy. Claude API calls, `npm install`, `pip install`, `apt`, `git push` — all of it, with no per-app config. Implemented via `redsocks` plus `nftables`.

### Embedded support

- Multi-arch images: `linux/amd64`, `linux/arm64`, `linux/arm/v7`.
- Headless flavor (`:latest-headless`) drops the Web UI bundle, smaller by roughly 140 MB. Only the REST and WebSocket API are exposed.
- Default base is Debian-slim; the headless variant skips bundled language runtimes so the image stays compact.
- Memory and CPU accounting per session, exposed via the API, so an admin running on a 4 GB SBC can see what is eating the box.

## Status

Very early. The repository currently holds the project name, logo, architecture sketch, nginx template, and the AES envelope protocol spec. Implementation is in progress. Star or watch if you want to follow along.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the planned shape of the system in more depth.

## Planned architecture (high level)

```
                                                       ┌────────────────────────────────────────────┐
                                                       │            claude-in-box container          │
                                                       │                                            │
   Browser / phone / iPad   ── HTTPS / WSS ─────────▶  │  ┌────────────┐    ┌──────────────────┐    │
   Server / CI / agent      ── HTTPS ────────────────▶ │  │  control   │◀──▶│ session manager  │──┐ │
   ESP32 / STM32 / MCU      ── HTTP + AES envelope ──▶ │  │   plane    │    │  (pty-backed,    │  │ │
   Watchdog / dashboard     ── SSE ──────────────────▶ │  │  (rest +   │    │   bypass-perm,   │  │ │
   IoT bus                  ── MQTT (planned) ──────▶  │  │   ws +     │    │   resumable)     │  │ │
                                                       │  │   sse +    │    └──────────────────┘  │ │
                                                       │  │   aes ep)  │            ▲             ▼ │
                                                       │  │            │            │     ┌──────────┐
                                                       │  │  + auth    │            └────▶│  hooks   │
                                                       │  │  layer     │  ws stream        │  runtime │
                                                       │  └────────────┘                   └──────────┘
                                                       │        ▲                                │   │
                                                       │        │  structured frames             │   │
                                                       │        │  text.delta / tool.use         │   │
                                                       │        │  todo.update / usage           │   │
                                                       │        │  status / stop / meta          │   │
                                                       │        │                                │   │
                                                       │        │              ┌─────────────────┴──┐│
                                                       │        └──────────────┤ session files +    ││
                                                       │                       │ transcript.jsonl   ││
                                                       │                       └────────────────────┘│
                                                       │                                            │
                                                       │   Claude Code  ◀── pty ──  session N       │
                                                       │   Claude Code  ◀── pty ──  session 2       │
                                                       │   Claude Code  ◀── pty ──  session 1       │
                                                       │                ▲                            │
                                                       │                │  Anthropic subscription    │
                                                       │                │       or API key           │
                                                       │     ┌──────────┴────────┐                  │
                                                       │     │ transparent socks5│  ◀── optional    │
                                                       │     │ (redsocks + nft)  │     PROXY_URL    │
                                                       │     └───────────────────┘                  │
                                                       └────────────────────────────────────────────┘
```

## Quick start

```bash
# placeholder command shape; container does not exist yet.
docker run -d --name claude-box \
  -p 8080:8080 \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  -v $(pwd)/claude-home:/home/coder/.claude \
  ghcr.io/jiangmuran/claude-in-box:latest

open http://localhost:8080
```

Headless flavor for embedded hosts:

```bash
docker run -d --restart unless-stopped \
  --memory=2g --cpus=2 \
  -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

Behind HTTPS via nginx: see [`deploy/nginx.conf.template`](deploy/nginx.conf.template).

Implementing the AES envelope on a microcontroller: see [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md).

## Roadmap

- [ ] Base Docker image (Debian-slim + Node + Python + Go + Claude Code), multi-arch
- [ ] Session manager: spawn, attach, detach, kill, resume, PTY-backed, bypass-permission default
- [ ] Mid-session model switching
- [ ] Hooks runtime with image, user, and session-level merge
- [ ] Structured event stream (frames as tabled above)
- [ ] Web API: bearer token, device tokens, scopes, OIDC (later)
- [ ] WebSocket and SSE streaming with `?from=<seq>` resumption
- [ ] AES envelope HTTP transport for embedded clients
- [ ] MQTT and raw-TCP framed transports
- [ ] Web UI: terminal pane, side panels for todos / tool calls / usage / status, model picker, hook editor, mobile-responsive
- [ ] Subscription login flow inside the container
- [ ] Transparent SOCKS5 via redsocks plus nftables
- [ ] Headless flavor, multi-arch CI build
- [ ] Persistent sessions across container restarts
- [ ] Per-session resource accounting
- [ ] Multi-user / multi-tenant mode

## Contributing

Not yet open for contributions; the design is still settling. Open an issue if something resonates or if you have a target device with constraints we should keep in mind.

## License

[MIT](LICENSE)
