# Architecture

Companion to the README. Read the README first for the what and why; this doc covers the how. Names and layouts here match what the binary actually does.

## Components

### 1. Base container

A Debian-slim image with:

- Language runtimes: Node 22 LTS, Python 3 (plus FastAPI / Uvicorn / Pydantic / httpx / requests / pytest / rich / ipython3 from Debian packages), Go (current stable from go.dev), Rust (`rustc` / `cargo`).
- Bundled services: `nginx-light`, `redis-server`, `postgresql` (+ `postgresql-client`), `docker.io` (CLI + daemon). None auto-start; the entrypoint reads `CIB_SERVICES` (comma-separated list of any of `redis`, `postgres`, `nginx`, `docker`) and brings up only what is requested. `cib-services {start,stop,status} <svc[,svc...]>` is also available from inside a session.
- Common dev tools: `ripgrep`, `fd-find`, `bat`, `htop`, `tmux`, `vim`, `nano`, `openssh-client`, `less`, `file`, `tree`, `jq`, `curl`, `wget`, `make`, `build-essential`.
- `claude` CLI preinstalled and pinned to a known version.
- A non-root `coder` user in groups `sudo` and `docker`, `/workspace` as its home.
- `redsocks` plus `nftables` preinstalled to back the transparent SOCKS5 layer (§9).
- An entrypoint that runs the SOCKS5 setup (when configured), starts the requested services, then boots the control plane.
- The Web UI bundle baked in; whether it is served on `/` is controlled at runtime by `CIB_MODE` (default = serve; `headless` = API-only, `/` returns 404).

There is **one image, one tag**. No separate headless image. The same `:latest` runs both interactive desktops and API-only deployments — they differ only in env vars.

The image is built multi-arch for `linux/amd64` and `linux/arm64`. **`armv7` is deliberately not built**: the project targets real servers because Claude Code must stay in interactive REPL mode to consume the Anthropic subscription quota (see §3 and §4), and a real server is not an armv7 SBC. The "embedded" side of the project is the *client* (§14), not the server.

### 2. Control plane

A single long-running process inside the container exposing:

- HTTP/1.1 + WebSocket on `:8080`.
- A REST surface for management actions (create session, send input, list hooks, mint tokens, …).
- A WebSocket / SSE surface for streaming structured frames to clients.
- An optional static file server for the Web UI (skipped in headless mode).
- An AES-envelope HTTP transport (§7) sharing the same port and routing prefix.

The control plane owns the session manager, the hooks runtime, the streaming bridge, the transport layer, and the auth layer.

### 3. Session manager

Each Claude Code session is a child process attached to a pseudo-terminal (PTY). **Claude Code is always launched in interactive REPL mode** — never with `--print` — because subscription-quota billing only applies to interactive runs. Hook-driven structured event capture (§5) is what makes interactive mode equivalent to "structured output" for our purposes.

Spawn command shape:

```
claude
  --output-format stream-json
  --include-hook-events
  --dangerously-skip-permissions          # off if requested
  --model <model>
  [--resume <session_id>]
  --settings <per-session merged settings.json>
```

`--output-format stream-json --include-hook-events` is layered on top **if** Claude Code supports it in interactive mode; if it does not, hooks alone carry the structured channel and the raw PTY carries the visible output.

Responsibilities:

- `spawn(workdir, env, args, options)` returns `session_id`.
  - Default `options.bypass_permissions = true` because the container is the sandbox.
  - `options.resume_from = <session_id>` brings a previous session back with its full transcript via `--resume`.
  - `options.auth = { mode: "subscription" | "api_key", … }` declares how Claude is billed for this session (§4).
  - `options.model = "claude-opus-4-x" | "claude-sonnet-4-x" | ...` sets the initial model.
- `attach(session_id)` exposes the structured frame stream and a writable stdin handle.
- `write(session_id, bytes | text_frame)` — used by both human input and the input simulator.
- `set_model(session_id, model)` — issues the `/model` slash command inside the PTY and emits a `meta` frame.
- `interrupt(session_id)` — sends the equivalent of `Ctrl+C`/Esc to the session.
- `kill(session_id, signal)`.
- Lifecycle persistence: each session has a directory under `sessions/<id>/` containing:
  - `meta.json` — workdir, env, model, auth mode, created_at, stopped_at, ...
  - `transcript.jsonl` — append-only structured frames (§6) for resume and audit; CC's own `~/.claude/projects/<hash>/<id>.jsonl` is the upstream source of truth.
  - `output.log` — raw PTY capture for terminal replay
  - `hooks/` — per-session hook overrides

PTY backing supports TUI features (cursor, colors, line editing) and gives the input simulator a stable surface. Multiple clients can attach to one session simultaneously; input from each is serialized through the manager so writes do not tear.

### 4. Claude authentication (per session)

Two modes coexist; subscription is the default for personal use because it is what most users already pay for.

**Subscription via in-container interactive `claude /login` (primary path).**

- The Web UI's unified auth modal drives a PTY-backed `claude /login` inside the container end-to-end (start → paste code → finish).
- Credentials land in the mounted `~/.claude/.credentials.json` and persist across restarts; transcripts and MCP config persist alongside in the same volume.
- This is the recommended path for new deployments.

**Subscription via long-lived OAuth token (legacy).**

- Pre-dates the in-container interactive flow. Useful for fully headless deployments where the operator cannot open the Web UI to log in.
- The user mints the token with `claude setup-token` on a workstation and passes it as `CLAUDE_CODE_OAUTH_TOKEN`.
- **Note:** tokens issued by `claude setup-token` move to a separate Agent SDK billing quota after 2026-06-15 and stop drawing against the interactive subscription. The interactive flow above is preferred for new deployments; this path is kept for compatibility.

**API key.**

- Container-level default via `ANTHROPIC_API_KEY=sk-ant-...`; per-session override via the create API.
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

#### 7.7 Anthropic-compatible API

`POST /v1/messages` (with optional `stream=true` SSE) mimics `api.anthropic.com`. Incoming Messages-API-shaped requests spawn a per-request session under the hood; outgoing events come back as Anthropic-shaped `message_start` / `content_block_delta` / `message_delta` / `message_stop` SSE events, emitted incrementally as the assistant writes them.

This is a **format adapter over the same session bus**, not a parallel runtime. Any existing Claude SDK can point `base_url` at the box and transparently route through subscription-backed Claude.

#### 7.8 OpenAI-compatible API

`POST /openai/v1/chat/completions` accepts the OpenAI Chat Completions request shape and returns `chat.completion` / `chat.completion.chunk` responses. Same underlying session, system messages folded into Anthropic's `system` field. Lets `openai`, `openai-node`, langchain, and friends target the box with no SDK changes.

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

The Web UI surfaces **three concurrent views** on the same underlying session. A user can switch between them, or open them side-by-side, depending on whether they want CC's native TUI, a no-terminal experience, or a developer-facing wire view.

**View A — Raw virtual terminal.**

- xterm.js bound to the PTY's raw byte stream (`pty.raw` frames).
- Renders CC's native TUI verbatim: resume picker, slash-command menu, ANSI colors, etc.
- The right surface when the CC TUI is the most natural UX.

**View B — Web-native Claude driver.**

- Chat-style transcript rendered from `text.delta`, `tool.use.*`, `todo.update`, `ask.question` frames.
- Side rails: live todo list (`todo.update`), tool-call timeline (`tool.use.*`), token / time meter (`usage`, `status`).
- Top bar: session sidebar, model picker, auth-mode picker, hook editor.
- Mobile-responsive — this is the view that makes a phone a viable client.

**View C — API inspector.**

- Devtools-style stream of every frame on the bus and every HTTP request/response on the wire.
- For developers building against the box.

All three views read from the same frame bus; switching between them does not interrupt the session.

In `headless` mode this layer is absent; `/` returns 404, and only `/api/*`, `/ws/*`, `/sse/*`, `/aes/*`, `/v1/messages*`, and `/openai/v1/chat/completions` are served.

### 12. REST API (summary)

For the full reference with bodies, status codes, and examples see [`docs/API.md`](API.md). Highlights:

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

POST   /aes/sessions/:id/input            AES v2 record envelope (see AES-TRANSPORT.md)
POST   /aes/sessions/:id/chat             AES v2 — slim chat list, supports `since` cursor
POST   /aes/sessions/:id/events/stream    AES v2 — chunked record stream of frames

# Format adapters
POST   /v1/messages                       Anthropic Messages API (non-stream)
POST   /v1/messages?stream=true           Anthropic Messages API (SSE, incremental)
POST   /openai/v1/chat/completions        OpenAI Chat Completions (with stream flag)
```

All routes except `/api/health`, `/aes/time`, and `/aes/keyinfo` require auth (§10). The Anthropic- and OpenAI-compatible adapters accept either a bearer token or the API key in the original SDK's expected header.

### 13. API-only ("headless") runtime mode

`CIB_MODE=headless` flips a single switch in the control plane: `/` returns 404, the Web UI bundle is not served, and only the API surfaces are exposed (`/api/*`, `/ws/*`, `/sse/*`, `/aes/*`, plus the Anthropic / OpenAI format adapters). The image is the same `:latest` — there is no second tag.

Recommended for: CI runners, agent-only deployments, machines that should never expose a human-facing UI.

```bash
docker run -d --restart unless-stopped \
  -p 8080:8080 \
  --cap-add NET_ADMIN \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  -e CLAUDE_CODE_OAUTH_TOKEN=cclo_... \
  -e CIB_PROXY_URL=socks5://... \
  -v /opt/claude-box/sessions:/var/lib/claude-in-box/sessions \
  -v /opt/claude-box/workspace:/workspace \
  -v /opt/claude-box/claude-home:/home/coder/.claude \
  ghcr.io/jiangmuran/claude-in-box:latest
```

### 14. Embedded *clients* (not the server)

The server is intentionally **not** sized for embedded hosts — running CC in interactive mode against subscription quota wants a real machine. What is embedded-friendly is the *client*.

- **AES envelope** (§7.2) is designed so an ESP32 / STM32 / RP2040 with only an HTTP client and an AES-GCM implementation can be a first-class participant.
- **Record-stream events endpoint** (`POST /aes/sessions/:id/events/stream`) lets the device decrypt and render frames as they arrive, while keeping peak RAM at one record (≤ 4 KiB plaintext) regardless of total response size. Heartbeats land every few seconds during idle waits.
- A reference C client lives at `clients/c/` (mbedtls + libcurl, ~300 LOC); ESP-IDF example beside it.
- A reference Python client lives at `clients/python/` (stdlib + `cryptography`, ~250 LOC, with tests). A Rust client is the next on the list.
- Devices identify themselves with a device-scoped token minted by the admin (§10) and each gets its own scope set, revocable independently.

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
