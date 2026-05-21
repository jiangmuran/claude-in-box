# Architecture (sketch)

> Status: design draft. Names, file layouts, and APIs will move. The goal here is to capture the shape of the system clearly enough that someone reading the repo for the first time understands what we are building.

## Components

### 1. Base container

A Debian-slim image with:

- Common language runtimes (Node, Python, Go, Rust) — kept minimal, extend per project via overlay images.
- `claude` CLI preinstalled and pinned to a known version.
- A non-root `coder` user with sudo, `/workspace` as its home.
- `redsocks` + `nftables` preinstalled to back the transparent SOCKS5 layer (§7).
- An entrypoint that boots the **control plane** instead of dropping into a shell.

Two image flavors are built:

| Tag | Includes Web UI | Use case | Approx size |
|-----|-----------------|----------|-------------|
| `:latest` | yes | desktop / server | ~280 MB |
| `:latest-headless` | no | embedded, CI, agent-only | ~140 MB |

All flavors are built multi-arch: `linux/amd64`, `linux/arm64`, `linux/arm/v7`.

### 2. Control plane

A single long-running process inside the container exposing:

- **HTTP/1.1 + WebSocket** on `:8080`.
- A REST surface for management actions (create session, send input, list hooks, …).
- A WebSocket / SSE surface for streaming session output to clients.
- A static file server for the Web UI (skipped in headless mode).

The control plane owns the session manager, the hooks runtime, the streaming bridge, and the auth layer.

### 3. Session manager

Each Claude Code session is a child process attached to a pseudo-terminal (PTY).

Responsibilities:

- `spawn(workdir, env, args)` → returns `session_id`.
- `attach(session_id)` → exposes an output stream and a writable stdin handle.
- `write(session_id, bytes)` — used by both human input and the input simulator.
- `kill(session_id, signal)`.
- Lifecycle persistence: each session has a directory under `sessions/<id>/` holding `meta.json`, an append-only `transcript.jsonl`, and the raw `output.log` PTY capture.

PTY backing lets us cleanly support TUI features (cursor, colors, line editing) and gives the input simulator a stable surface.

### 4. Hooks runtime

Hooks are user-supplied executables (or inline scripts) that fire on lifecycle events. Event taxonomy mirrors Claude Code's existing hook events where applicable:

- `session.start`
- `user.prompt.submit`
- `tool.use.pre`
- `tool.use.post`
- `assistant.message`
- `session.stop`
- `session.error`

Each hook receives a structured JSON payload on stdin and can return JSON to mutate the event (block a tool, rewrite a prompt, inject context).

Hook config lives in `/etc/claude-in-box/hooks.json` (image-level) and `~/.claude/hooks.json` (user-level), merged at session start.

### 5. Streaming bridge

A fan-out layer that takes each session's PTY output, normalizes it into structured frames (`stdout`, `stderr`, `hook_event`, `meta`), and broadcasts to:

- subscribed WebSocket clients,
- SSE listeners,
- the on-disk `transcript.jsonl`.

Each frame carries `session_id`, `seq`, `ts`, `kind`, `data` so clients can resume from a sequence number on reconnect.

Frame ordering is global per session; resumption is `?from=<seq>` on connect.

### 6. Attach

Three ways to get into a running box:

1. **`docker attach claude-box`** — attaches stdin/stdout/stderr of the control plane's PID 1. Mostly for ops/debugging.
2. **`claude-in-box attach <session_id>`** — the CLI dials the WebSocket stream API, pipes local stdin/stdout to a remote session's PTY. This is the everyday "drop into a session from anywhere" command.
3. **Web UI terminal pane** — same WS endpoint, rendered with xterm.js.

Attach is non-exclusive: multiple clients can attach to the same session simultaneously and see the same output. Input is serialized through the session manager so two clients typing at once is well-defined (last-write-wins per byte, but no torn writes).

### 7. Transparent SOCKS5 proxy

Setting `CIB_PROXY_URL=socks5://user:pass@host:port` (or `socks5h://…`) at container start enables a transparent redirect layer:

```
Claude Code  ──╮
npm install  ──┤
pip / apt    ──┼──▶ nftables redirect ──▶ redsocks ──▶ upstream SOCKS5
git push     ──┤      (PREROUTING)        :12345
…anything    ──╯
```

Implementation:

- `redsocks` listens on `127.0.0.1:12345` and speaks SOCKS5 upstream.
- An nftables ruleset in the `nat` table catches outbound TCP (and UDP via tun2socks where supported), excluding loopback and the proxy host itself, and DNATs to `127.0.0.1:12345`.
- DNS is handled by the `socks5h://` variant or by a forwarder running locally that proxies DNS over TCP through redsocks.
- The bring-up is idempotent and runs at container start before the control plane comes up; tear-down happens cleanly on SIGTERM.

Effect: every tool inside the box uses the proxy without knowing about it. No per-app `HTTPS_PROXY` env var, no SDK-level config.

For air-gapped/no-proxy mode, leave `CIB_PROXY_URL` unset and the nftables rules are not installed.

### 8. Auth

Lightweight by default, extensible:

- **Bearer token (default).** `CIB_AUTH_TOKEN` env var at boot becomes the master token. All HTTP and WebSocket requests require `Authorization: Bearer <token>`.
- **Device tokens.** Admin can mint scoped tokens via `POST /api/tokens { label, scopes, ttl? }`. Each device (phone, embedded MCU, CI runner) gets its own token; revocable independently.
- **OIDC (planned).** Front the control plane with an OIDC-aware reverse proxy (oauth2-proxy / authelia) for SSO. We expose the right `X-Forwarded-User` semantics so this is plug-and-play.
- **No anonymous mode by default.** If `CIB_AUTH_TOKEN` is unset, the control plane refuses to start. Override with `CIB_AUTH_DISABLED=1` for local dev.

WebSocket auth: the bearer token is sent as a `Sec-WebSocket-Protocol: bearer.<token>` subprotocol header (avoids leaking the token in URL logs) or as `?token=<...>` for restricted clients.

### 9. Web UI

Minimal but real:

- Session switcher in a sidebar.
- xterm.js-style terminal pane bound to the WebSocket stream.
- Slash-command palette for management actions (`/new`, `/kill`, `/rename`).
- Inline hook editor backed by the REST API.
- Mobile-responsive (the whole point is using it from a phone/tablet).

In `headless` mode this layer is absent and `/` returns 404; only `/api/*` and `/ws/*` are served.

### 10. REST API (sketch)

```
POST   /api/auth/login                 { token }        → cookie / 200
POST   /api/tokens                     { label, scopes } → { token, id }
GET    /api/tokens
DELETE /api/tokens/:id

GET    /api/sessions
POST   /api/sessions                   { workdir, env, args } → { id }
GET    /api/sessions/:id
DELETE /api/sessions/:id               { signal? }
POST   /api/sessions/:id/input         { data, encoding? }
GET    /api/sessions/:id/transcript    ?from=<seq>

GET    /api/hooks
PUT    /api/hooks                      { hooks: [...] }

GET    /api/health
GET    /api/proxy/status               → { mode, upstream, dropped }

WS     /api/sessions/:id/stream        ?from=<seq>
SSE    /api/sessions/:id/events        ?from=<seq>
```

All routes (except `/api/health`) require auth (§8).

### 11. Embedded / headless mode

`claude-in-box` is sized to run on small ARM boxes — Raspberry Pi 4/5, N100 mini PCs, Rockchip SBCs.

Tuning for this:

- **Headless image** (`:latest-headless`) drops the Web UI bundle (saves ~140 MB).
- **No bundled language runtimes** in the headless variant by default — only Claude Code + control plane. Languages can be `apt install`-ed on demand inside a session.
- **One PTY per session, no per-session container.** A 4 GB SBC can host ~4 concurrent Claude Code sessions comfortably.
- **CPU/mem accounting** is exposed via `/api/sessions/:id` so the admin can see what's eating the box.
- **Cross-compiled multi-arch images** so the same `docker run` line works on amd64 desktops and arm/v7 SBCs.

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
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

## Open questions

- **Multi-user.** Single-tenant first. Multi-tenant means per-user workspaces, per-user API keys, quotas.
- **Resource isolation between sessions.** Today: all sessions share the container. Future: cgroup-per-session, or one container per session orchestrated by a thin supervisor.
- **Persistence model.** `sessions/` on a host volume is enough for a single box. Across boxes we need an object store or a shared FS.
- **Hook sandboxing.** Hooks run with the same privileges as the session. Locking this down (seccomp? Wasm runtime?) is unsolved.
- **UDP through SOCKS5.** Pure SOCKS5 UDP support is patchy; tun2socks is heavier but more robust. Pick one before promising "all UDP works."

## Non-goals (for now)

- Replacing Claude Code's CLI on the command line. This wraps it, not replaces it.
- A general-purpose remote-IDE. Cursor / VS Code Remote / coder.com cover that space — we focus on the Claude Code session loop.
- A multi-cloud control plane. One box, one service, simple deploys.
