<p align="center">
  <img src="assets/banner.png" alt="claude-in-box — portable Claude Code dev environment with sessions, hooks, and a web API" width="800">
</p>

<p align="center">
  <strong>English</strong> &middot; <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <em>Run Claude Code on a real server. Drive it from anywhere — browser, phone, IoT board, even an MCU — through one web port.</em>
</p>

<p align="center">
  <a href="#status"><img src="https://img.shields.io/badge/status-early%20WIP-orange" alt="status: early WIP"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-D97757" alt="MIT licensed"></a>
  <img src="https://img.shields.io/badge/docker-multi--arch-2496ED?logo=docker&logoColor=white" alt="docker multi-arch">
  <img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success" alt="amd64 / arm64">
</p>

---

## What is this

`claude-in-box` packages a full on-demand development environment together with [Claude Code](https://www.anthropic.com/claude-code) into a single Docker container, then exposes it as a web service over **one port**.

You run it on a real server (cloud VM, dedicated host, beefy home machine). Inside the container, Claude Code runs in **full interactive REPL mode** — that is non-negotiable, because that is the only mode in which the Anthropic subscription quota is consumed; `--print` / headless invocations only accept API keys. You then drive that interactive Claude Code from anywhere over the network.

What you get:

- a sandboxed Linux box preloaded with a real, batteries-included dev environment — Node 20, Python 3 (with FastAPI + Uvicorn + Pydantic + httpx + rich + ipython), Go 1.25, Rust, plus `nginx`, `redis-server`, `postgresql`, and the Docker CLI/daemon — and Claude Code itself;
- common tools out of the box: ripgrep, fd, bat, htop, tmux, vim, nano, openssh-client, less, file, tree, jq, curl, wget, build-essential, make;
- bundled services do not auto-start; pass `CIB_SERVICES=redis,postgres,nginx` (any subset of `redis`, `postgres`, `nginx`, `docker`) and the entrypoint brings them up before the control plane;
- one or many virtual-TTY sessions running inside it, each a live Claude Code conversation in bypass-permission mode (the container is the sandbox, so per-tool prompts are unnecessary friction);
- a Web UI that surfaces three concurrent views on the same session — raw virtual terminal, web-native structured Claude driver, and an API inspector for developers;
- structured event streaming: text deltas, tool calls, todo updates, token usage, status changes, stop reasons, model metadata — all available as JSON frames over WebSocket or SSE, never screen-scraped from the TTY;
- session lifecycle controls: create, attach, resume, kill, switch models on the fly;
- two ways to bill Claude per session: an Anthropic subscription (via a long-lived OAuth token you mint on your laptop with `claude setup-token`), or an API key;
- a Web API in multiple wrappings off one port: our native frame schema (REST + WS + SSE + AES envelope), plus planned **Anthropic-compatible** (`/v1/messages`) and **OpenAI-compatible** (`/openai/v1/chat/completions`) adapters so existing SDKs can target the box as a drop-in;
- a transparent SOCKS5 layer so every outbound packet from inside the box can be rerouted through one upstream proxy without per-tool config;
- programmable hooks on every lifecycle event;
- a single multi-arch image (`linux/amd64`, `linux/arm64`) that boots equally cleanly on x86 servers and Ampere-class arm64 hosts.

The point: stop tying Claude Code to one workstation. Put it on a real server you already own, then use it from anywhere with the transport and API shape that fits the client.

## The ideal workflow

```
1.  Pick an environment image: prebuilt :latest or your own custom build on
    top of it.
2.  Forward one port (8080 by default).
3.  docker run — the container boots the control plane on :8080, multiplexing
    Web UI + REST + WS + SSE + AES envelope on the same port.
4.  Open the web panel. Authenticate with the master API key minted at boot.
5.  Choose how to bill Claude this session: an Anthropic subscription (paste
    the long-lived OAuth token you got with `claude setup-token` on your
    laptop), or an Anthropic API key. The choice is per-session.
6.  Dashboard shows: live sessions, token consumption, wall-clock work time,
    current model, hook activity.
7.  Create a new session. The panel gives you three concurrent views on it:
       (a) raw virtual terminal — xterm.js bound to Claude Code's PTY, the
           native CC TUI as you'd see in iTerm;
       (b) web-native Claude driver — chat-style transcript + todo sidebar
           + tool-call timeline + token meter + model picker;
       (c) API inspector — every frame and every API request/response,
           devtools-style.
    You can switch between them or open them side-by-side.
8.  Talk to Claude. Switch models mid-flight. Watch todos, tool calls, and
    status update live in the structured panes — they are rendered from a
    typed event stream, not by screen-scraping the terminal.
9.  From a phone, tablet, embedded MCU, or another agent, hit the same
    sessions over the transport and API shape that suits the client —
    REST/WS for browsers, AES envelope for an ESP32, Anthropic- or
    OpenAI-compatible HTTP for off-the-shelf SDKs (planned).
```

That is the loop the rest of this README is here to explain.

## Capabilities

### Sessions and Claude Code

| Capability | Notes |
|------------|-------|
| Multi-session | PTY-backed; spawn, attach, detach, kill, list. Multiple clients can attach to the same session simultaneously. |
| Interactive REPL only | Claude Code is run in its full interactive mode, never with `--print`. This is mandatory for subscription-quota billing and is what powers the structured event stream via hooks. |
| Bypass-permission mode | Default. Claude Code runs with `--dangerously-skip-permissions` because the container is the security boundary, not the per-tool prompt. Can be turned off per session; hook `PermissionRequest` events still fire and can re-authoritate. |
| Resume | Sessions are CC's own — transcript lives at `~/.claude/projects/<hash>/<session>.jsonl`. `POST /api/sessions { resume: <session_id> }` re-spawns with `--resume`. |
| Model switching | `POST /api/sessions/:id/model { model }` sends `/model <name>` into the PTY mid-session and emits a `meta` frame. |
| Input simulation | `POST /api/sessions/:id/input` writes raw bytes (or text frames) into the session's stdin. Same primitive backs both human typing and automation. |
| Detached / headless | Sessions survive client disconnects. Reconnect with `?from=<seq>` to replay missed frames. |

### Claude authentication (per session)

| Mode | When to use | How |
|------|-------------|-----|
| Anthropic subscription (default for personal use) | You already pay for Claude Pro / Max and want it billed there. | On your laptop, run `claude setup-token` to mint a long-lived OAuth token. Pass it as `CLAUDE_CODE_OAUTH_TOKEN` to the container, or per-session via the API. |
| API key | Programmatic, CI, paying per token, or sharing the box across people who all have their own keys. | Set `ANTHROPIC_API_KEY` on the container, or per-session via the API. |
| In-container interactive `claude /login` | Convenience for users who do not want to fuss with `claude setup-token`. | Roadmap (M3). Web UI will drive an OAuth callback flow against a PTY-backed `claude /login`. |

Subscription billing only works because CC stays in interactive REPL mode inside the container — see the row above.

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

Hooks are first-class. The control plane installs its own `http`-type hooks at session start (HMAC-signed, pointed at an internal route) so it can capture every lifecycle event authoritatively. User-supplied hooks compose on top, merged from image-level (`/etc/claude-in-box/hooks.json`), user-level (`~/.claude/hooks.json`), and per-session declarations. Hooks can rewrite, block, inject context, or annotate; results land on the frame bus as `hook` frames.

### Web API: one port, many wrappings

The container exposes exactly one TCP port. Everything is multiplexed onto it through HTTP routing. Each capability is wrapped in multiple shapes so very different clients can use the same backend with the format they prefer.

| Wrapping | Path prefix | Best for | Crypto | Auth | Status |
|----------|-------------|----------|--------|------|--------|
| Native frame REST + WS | `/api/*`, `/ws/*` | Browser, phone, server, our Web UI | TLS via nginx | Bearer token (master or device-scoped) | M1 |
| Native frame SSE | `/sse/*` | Cheap one-way clients, log tailers | TLS via nginx | Bearer | M1 |
| HTTP + AES envelope | `/aes/*` | Bare-metal MCU (ESP32, STM32), no TLS stack | AES-256-GCM per-device key | API key + per-request nonce | M1 |
| Anthropic-compatible API | `/v1/messages`, `/v1/messages?stream=true` | Existing Claude SDK clients — point base URL at the box, get subscription-backed Claude | TLS via nginx | Bearer / API key | M3 (planned) |
| OpenAI-compatible API | `/openai/v1/chat/completions` | Existing OpenAI SDK clients | TLS via nginx | Bearer / API key | M3 (planned) |
| MQTT bridge | — | IoT bus integrations | TLS or pre-shared | Per topic | Roadmap |
| Raw TCP framed | — | Absolute minimum footprint | AES-GCM | API key | Roadmap |

The Anthropic- and OpenAI-compatible adapters are **format adapters over the same session bus**, not parallel runtimes. They let any tool that already speaks those APIs route through the box and pick up subscription-backed Claude.

For HTTPS deployments we ship an [nginx template](deploy/nginx.conf.template) that terminates TLS, proxies the REST surface, upgrades WebSocket connections, holds SSE open, and forwards client IPs.

For the embedded HTTP transport we ship a small protocol spec, [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md), so device firmware authors can implement it in a few hundred lines with any AES-GCM library.

### Auth on the control plane

- A master API key is minted at container boot via `CIB_AUTH_TOKEN`. The control plane refuses to start without one (override only for local dev).
- Device tokens can be issued via the API. Each has a label, scope set, and optional TTL. Revocable independently.
- WebSocket auth travels in the `Sec-WebSocket-Protocol` subprotocol header to keep tokens out of URL logs.
- OIDC is planned via a fronting reverse proxy (oauth2-proxy / authelia). The control plane honors `X-Forwarded-User`.

### Network: transparent SOCKS5

Set `CIB_PROXY_URL=socks5://user:pass@host:port` once at boot and every outbound TCP (and UDP through `tun2socks` where supported) from inside the box is redirected through that proxy. Claude API calls, `npm install`, `pip install`, `apt`, `git push` — all of it, with no per-app config. Implemented via `redsocks` plus `nftables`.

### Embedded clients (not the server)

The server side is intentionally **not** sized for embedded hosts — running CC in interactive mode against subscription quota wants a real machine. What is embedded-friendly is the **client** side:

- The AES envelope HTTP transport is designed so an ESP32 / STM32 / RP2040 with only an HTTP client and an AES-GCM implementation can be a first-class participant: send input to a session, poll for structured frames, react to todos / stop events.
- A reference C client lives at [`clients/c/`](clients/) (mbedtls-based, ~300 LOC), with a sibling ESP-IDF example.
- Rust and Python reference clients follow in M3.

## Status

Very early. The repository currently holds the project name, logo, architecture sketch, nginx template, and the AES envelope protocol spec. Implementation is in progress under the milestones below. Star or watch to follow along.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the planned shape of the system in more depth.

## Planned architecture (high level)

```
                                                       ┌────────────────────────────────────────────┐
                                                       │            claude-in-box container          │
                                                       │            (real server only)               │
   Browser / phone / iPad   ── /api  /ws  /sse  ───▶   │  ┌────────────┐    ┌──────────────────┐    │
   Server / CI / agent      ── /api  /ws  /sse  ───▶   │  │  control   │◀──▶│ session manager  │──┐ │
   Existing Claude SDK      ── /v1/messages*    ───▶   │  │   plane    │    │  (PTY-backed,    │  │ │
   Existing OpenAI SDK      ── /openai/v1/chat* ───▶   │  │  (single   │    │   interactive,   │  │ │
   ESP32 / STM32 / MCU      ── /aes/...          ───▶  │  │   :8080,   │    │   bypass-perm,   │  │ │
   Watchdog / dashboard     ── /sse              ───▶  │  │   multi-   │    │   resumable)     │  │ │
                                                       │  │   wrapped) │    └──────────────────┘  │ │
                                                       │  │            │            ▲             ▼ │
                                                       │  │  + auth    │            │     ┌──────────┐
                                                       │  └────────────┘            └────▶│  hooks   │
                                                       │        ▲                          │  runtime │
                                                       │        │  structured frames       └──────────┘
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
                                                       │                │  (OAuth long token)        │
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
  --cap-add NET_ADMIN \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CLAUDE_CODE_OAUTH_TOKEN=cclo_...      # from `claude setup-token` on your laptop
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -e CIB_SERVICES=redis,postgres \         # auto-start bundled services
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  -v $(pwd)/claude-home:/home/coder/.claude \
  -v /var/run/docker.sock:/var/run/docker.sock \   # optional: talk to host Docker
  ghcr.io/jiangmuran/claude-in-box:latest

open http://localhost:8080
```

API-only mode (no Web UI served on `/`, only `/api/*` `/ws/*` `/sse/*` `/aes/*`) — same image, just a runtime mode:

```bash
docker run -d --restart unless-stopped \
  -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest
```

Behind HTTPS via nginx: see [`deploy/nginx.conf.template`](deploy/nginx.conf.template).

Implementing the AES envelope on a microcontroller: see [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md).

## Roadmap

**M1 — headless backend, complete:**

- [ ] Base Docker image (Debian-slim + Node + Python + Go + Rust + claude-code), single tag, multi-arch (amd64 + arm64)
- [ ] Session manager: spawn, attach, detach, kill, resume, PTY-backed, bypass-permission default, interactive CC only
- [ ] Mid-session model switching via `/model`
- [ ] Hooks runtime: image / user / session-level merge, control-plane http hooks installed per session
- [ ] Structured event stream (frame schema as tabled above)
- [ ] Web API: bearer token, device tokens, scopes
- [ ] REST + WS + SSE multiplexed on one port, `?from=<seq>` resumption
- [ ] AES envelope HTTP transport for embedded clients (`/aes/*`)
- [ ] Transparent SOCKS5 via redsocks plus nftables
- [ ] `CIB_MODE=headless` runtime switch for API-only deployments
- [ ] Multi-arch CI build to GHCR
- [ ] C reference client and ESP32-IDF demo

**M2 — Web UI:**

- [ ] Three concurrent views per session (raw terminal, web Claude driver, API inspector)
- [ ] Session sidebar, model picker, hook editor, MCP server config CRUD
- [ ] Mobile-responsive layout

**M3 — format adapters and auth stretch:**

- [ ] Anthropic-compatible API (`/v1/messages`, streaming)
- [ ] OpenAI-compatible API (`/openai/v1/chat/completions`)
- [ ] In-container interactive `claude /login` OAuth flow
- [ ] Multi-tenant `~/.claude` isolation per device-token-owner
- [ ] OIDC via reverse-proxy header trust
- [ ] MQTT bridge, raw-TCP framed transport
- [ ] Rust and Python reference clients for AES envelope

## Contributing

Not yet open for contributions; the design is still settling. Open an issue if something resonates or if you have a target client device with constraints we should keep in mind.

## License

[MIT](LICENSE)
