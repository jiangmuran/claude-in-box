# Architecture (sketch)

> Status: design draft. Names, file layouts, and APIs will move. The goal here is to capture the shape of the system clearly enough that someone reading the repo for the first time understands what we are building.

## Components

### 1. Base container

A Debian-slim image with:

- Common language runtimes (Node, Python, Go, Rust) — kept minimal, extend per project via overlay images.
- `claude` CLI preinstalled and pinned to a known version.
- A non-root `coder` user with sudo, `/workspace` as its home.
- `redsocks` plus `nftables` preinstalled to back the transparent SOCKS5 layer (§8).
- An entrypoint that boots the control plane instead of dropping into a shell.

Two image flavors:

| Tag | Includes Web UI | Use case | Approx size |
|-----|-----------------|----------|-------------|
| `:latest` | yes | desktop, server | ~280 MB |
| `:latest-headless` | no | embedded, CI, agent-only | ~140 MB |

All flavors built multi-arch: `linux/amd64`, `linux/arm64`, `linux/arm/v7`.

### 2. Control plane

A single long-running process inside the container exposing:

- HTTP/1.1 + WebSocket on `:8080`.
- A REST surface for management actions (create session, send input, list hooks, mint tokens, …).
- A WebSocket / SSE surface for streaming structured frames to clients.
- An optional static file server for the Web UI (skipped in headless mode).
- An AES-envelope HTTP transport (§7) sharing the same port and routing prefix.

The control plane owns the session manager, the hooks runtime, the streaming bridge, the transport layer, and the auth layer.

### 3. Session manager

Each Claude Code session is a child process attached to a pseudo-terminal (PTY).

Responsibilities:

- `spawn(workdir, env, args, options)` returns `session_id`.
  - Default `options.bypass_permissions = true` because the container is the sandbox.
  - `options.resume_from = <session_id>` brings a previous session back with its full transcript.
  - `options.auth = { mode: "subscription" | "api_key", … }` declares how Claude is billed for this session (§4).
  - `options.model = "claude-opus-4-7" | ...` sets the initial model.
- `attach(session_id)` exposes the structured frame stream and a writable stdin handle.
- `write(session_id, bytes | text_frame)` — used by both human input and the input simulator.
- `set_model(session_id, model)` — issues the `/model` slash command inside the PTY and emits a `meta` frame.
- `interrupt(session_id)` — sends the equivalent of `Ctrl+C`/Esc to the session.
- `kill(session_id, signal)`.
- Lifecycle persistence: each session has a directory under `sessions/<id>/` containing:
  - `meta.json` — workdir, env, model, auth mode, created_at, stopped_at, ...
  - `transcript.jsonl` — append-only structured frames (§6) for resume and audit
  - `output.log` — raw PTY capture for terminal replay
  - `hooks/` — per-session hook overrides

PTY backing supports TUI features (cursor, colors, line editing) and gives the input simulator a stable surface. Multiple clients can attach to one session simultaneously; input from each is serialized through the manager so writes do not tear.

### 4. Claude authentication (per session)

Two modes coexist:

**Subscription** — uses an Anthropic claude.ai account.

- A small `claude-in-box login` flow inside the container drives the standard `claude login` device-code path.
- Credentials persist in `/home/coder/.claude/` on a mounted volume so they survive container restarts.
- One subscription account can power multiple parallel sessions (subject to the account's own concurrency rules).

**API key** — uses `ANTHROPIC_API_KEY=sk-ant-...`.

- Container-level default via env var; per-session override via the create API.
- Each session's billing isolation is the API key it was started with.

A session's auth mode is fixed at create time but a new session can be spawned with a different mode at any time. The dashboard rolls up usage per session and per auth identity.

### 5. Hooks runtime

Hooks are user-supplied executables (or inline scripts) that fire on lifecycle events listed in §6. Each receives a structured JSON payload on stdin and can return JSON to mutate the event (block a tool, rewrite a prompt, inject context, redact output before it streams to clients).

Hook config is merged at session start in this order, last write wins:

1. `/etc/claude-in-box/hooks.json` — image / operator level
2. `~/.claude/hooks.json` — user level (mounted volume)
3. session-level overrides declared in the `POST /api/sessions` body

Hooks themselves also emit a `hook` frame onto the stream when they fire, so observers can watch them work.

### 6. Structured event stream

The streaming bridge does not just relay terminal bytes. It parses Claude Code's lifecycle into typed frames so any client — phone, terminal, MCU — can act on them without screen-scraping.

Every frame carries `session`, `seq`, `ts`, `kind`, `data`. Frame ordering is global per session; resumption is `?from=<seq>` on (re)connect.

| Frame `kind` | Emitted when | `data` fields |
|--------------|--------------|---------------|
| `text.delta` | Assistant streams text | `text` |
| `thinking` | Extended-thinking block | `text` (optional, config-gated) |
| `tool.use.start` | Tool invocation begins | `tool`, `input` |
| `tool.use.result` | Tool returns | `tool`, `output`, `error?`, `duration_ms` |
| `todo.update` | TodoWrite / TodoUpdate fires | `items: [{ id, subject, status, activeForm? }]` |
| `ask.question` | Model asks user to pick | `prompt`, `options[]`, `multiSelect` |
| `usage` | End of turn | `input`, `output`, `cache_read`, `cache_write` |
| `status` | Session state changes | `state` in `idle / working / waiting_for_input / stopped`, `elapsed_ms` |
| `stop` | Turn or session ends | `reason` |
| `meta` | Model or config changes | `model`, `workdir`, `auth_mode`, … |
| `hook` | A hook fired | `name`, `event`, `payload`, `result?` |
| `pty.raw` | Optional opaque PTY bytes | `data` (off by default; on for terminal-style clients) |

Parsing strategy: we run Claude Code with structured output where it supports it (and capture hook events on the side) so we never need to grep the TTY for tool calls. The `pty.raw` channel exists only for clients that want to render the original terminal UI verbatim.

### 7. Transports

Each capability is exposed across multiple transports so very different devices can use the same backend.

#### 7.1 HTTPS + WSS (primary)

Standard REST + WebSocket, fronted by [nginx](../deploy/nginx.conf.template) (or any reverse proxy) for TLS termination. WebSocket auth travels in the `Sec-WebSocket-Protocol` subprotocol header.

#### 7.2 HTTP + AES envelope (embedded)

For devices with no TLS stack (ESP32, STM32, RP2040, …).

Routes mirror `/api/*` under `/aes/*`. Request and response bodies are AES-256-GCM encrypted with a per-device key. Replay protection via nonce plus timestamp. Full wire format in [`AES-TRANSPORT.md`](AES-TRANSPORT.md). The device only needs an AES-GCM implementation and an HTTP client.

#### 7.3 SSE

Read-only one-way stream of frames. Easier to implement than WebSocket on constrained clients and friendlier to proxies. Same auth as REST; same frame format as WebSocket.

#### 7.4 WebSocket over LAN (no TLS)

For trusted LANs where TLS is unnecessary overhead. The AES envelope can still be layered on top if the LAN is untrusted but the device has no TLS.

#### 7.5 MQTT bridge (roadmap)

For shops already on an MQTT bus. Each session's structured frames are republished to `claude-in-box/<session>/frames`. Input goes to `claude-in-box/<session>/input`. Auth via topic ACL.

#### 7.6 Raw TCP framed (roadmap)

Length-prefixed framing over a raw TCP socket with AES-GCM payloads, for the absolute-minimum-footprint case.

### 8. Attach

Three ways to dial in:

1. `docker attach claude-box` — attaches stdin/stdout/stderr of the control plane's PID 1. Mostly for ops.
2. `claude-in-box attach <session_id>` — CLI dials the WebSocket stream and pipes local stdin/stdout to a remote session's PTY. Everyday "drop into a session from anywhere" command.
3. Web UI terminal pane — same WS endpoint, rendered with xterm.js.

Attach is non-exclusive: many clients on one session see the same output; input is serialized.

### 9. Transparent SOCKS5

Setting `CIB_PROXY_URL=socks5://user:pass@host:port` (or `socks5h://…`) at container start enables a transparent redirect:

```
Claude Code   ─╮
npm install   ─┤
pip / apt     ─┼──▶ nftables redirect ──▶ redsocks ──▶ upstream SOCKS5
git push      ─┤      (PREROUTING)        :12345
anything else ─╯
```

- `redsocks` listens on `127.0.0.1:12345` and speaks SOCKS5 upstream.
- nftables `nat` rules catch outbound TCP, excluding loopback and the proxy host itself, DNAT to `127.0.0.1:12345`.
- DNS handled via `socks5h://` or a local TCP-DNS forwarder routed through redsocks.
- Bring-up is idempotent at container start, before the control plane. Tear-down is clean on SIGTERM.
- If `CIB_PROXY_URL` is unset, no rules are installed; everything goes direct.

### 10. Control-plane auth

- A master API key is minted at container boot via `CIB_AUTH_TOKEN`. The control plane refuses to start without one (`CIB_AUTH_DISABLED=1` overrides for local dev).
- Device tokens are minted by the admin: `POST /api/tokens { label, scopes, ttl? }`. Each device (phone, MCU, CI runner) gets its own token, revocable independently.
- Scopes are coarse (`sessions:read`, `sessions:write`, `hooks:write`, `tokens:admin`, …) and enforced at the route layer.
- WebSocket auth in `Sec-WebSocket-Protocol: bearer.<token>` to keep tokens out of URL logs; `?token=...` accepted for clients that cannot set subprotocols.
- OIDC is planned via a fronting reverse proxy (oauth2-proxy / authelia); the control plane honors `X-Forwarded-User` and `X-Forwarded-Email`.

### 11. Web UI

Minimal but real:

- Sidebar: session list with state badges and token-usage sparklines.
- Main pane: xterm.js terminal bound to the WebSocket stream.
- Side panels rendered from the structured frame stream:
  - Live todo list (`todo.update`).
  - Tool-call log (`tool.use.*`).
  - Token / time meter (`usage`, `status`).
  - AskUserQuestion modal (`ask.question`) with the multi-select / single-select form.
- Top bar: model picker, auth-mode picker, hook editor, settings.
- Mobile-responsive (the whole point is using it from a phone or tablet).

In `headless` mode this layer is absent; `/` returns 404, only `/api/*`, `/ws/*`, `/sse/*`, `/aes/*` are served.

### 12. REST API (sketch)

```
POST   /api/auth/login                 { token }           → cookie / 200
POST   /api/tokens                     { label, scopes, ttl? } → { token, id }
GET    /api/tokens
DELETE /api/tokens/:id

GET    /api/sessions
POST   /api/sessions                   { workdir, env, args, auth, model, resume_from?, bypass_permissions? } → { id }
GET    /api/sessions/:id
DELETE /api/sessions/:id               { signal? }
POST   /api/sessions/:id/input         { data, encoding? }
POST   /api/sessions/:id/model         { model }
POST   /api/sessions/:id/interrupt
GET    /api/sessions/:id/transcript    ?from=<seq>
GET    /api/sessions/:id/usage

GET    /api/hooks
PUT    /api/hooks                      { hooks: [...] }

GET    /api/health
GET    /api/proxy/status               → { mode, upstream, dropped }
GET    /api/usage                      ?since&until&group_by=session|model|auth

WS     /api/sessions/:id/stream        ?from=<seq>
SSE    /api/sessions/:id/events        ?from=<seq>

POST   /aes/sessions/:id/input         AES envelope, see AES-TRANSPORT.md
POST   /aes/sessions/:id/events/poll   AES envelope, long-poll for frames
... (mirror of /api/* under /aes/*)
```

All routes except `/api/health` require auth (§10).

### 13. Embedded / headless mode

`claude-in-box` is sized to run on small ARM boxes — Raspberry Pi 4/5, N100 mini PCs, Rockchip SBCs.

Tuning for this:

- Headless image drops the UI bundle (saves ~140 MB).
- No bundled language runtimes in the headless variant; install on demand inside a session.
- One PTY per session, no per-session container. A 4 GB SBC can host roughly four concurrent Claude Code sessions.
- CPU and memory accounting per session exposed via `/api/sessions/:id` and `/api/usage`.
- Cross-compiled multi-arch images, same `docker run` line everywhere.

Recommended embedded deployment:

```bash
docker run -d --restart unless-stopped \
  --memory=2g --cpus=2 \
  -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  -e CIB_PROXY_URL=socks5://... \
  -v /opt/claude-box/sessions:/var/lib/claude-in-box/sessions \
  -v /opt/claude-box/workspace:/workspace \
  -v /opt/claude-box/claude-home:/home/coder/.claude \
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

## Open questions

- **Multi-user.** Single-tenant first. Multi-tenant means per-user workspaces, per-user API keys, quotas, isolated `~/.claude`.
- **Resource isolation between sessions.** Today: all sessions share the container. Future: cgroup-per-session, or one container per session orchestrated by a thin supervisor.
- **Persistence model.** `sessions/` on a host volume is enough for a single box. Across boxes we need an object store or a shared FS.
- **Hook sandboxing.** Hooks run with the same privileges as the session. Locking this down (seccomp? Wasm runtime?) is unsolved.
- **UDP through SOCKS5.** Pure SOCKS5 UDP support is patchy; tun2socks is heavier but more robust. Pick one before promising "all UDP works."
- **Subscription concurrency limits.** Claude.ai accounts have rate limits; the box should surface these clearly when they hit.
- **AES envelope key rotation.** First cut: rotate by minting a new device token and retiring the old. A formal key-derivation rotation may follow.

## Non-goals (for now)

- Replacing Claude Code's CLI on the command line. This wraps it, not replaces it.
- A general-purpose remote IDE. Cursor / VS Code Remote / coder.com cover that space — we focus on the Claude Code session loop.
- A multi-cloud control plane. One box, one service, simple deploys.
