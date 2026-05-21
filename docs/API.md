# claude-in-box · API reference

Everything the box exposes lives behind one TCP port (`:8080` by
default — `:8090` if you run with `-p 8090:8080` like the test deploy,
`:8443` for the nginx-fronted HTTPS path). The Web UI talks to the
same endpoints documented here.

| Surface | Path prefix | Notes |
|---|---|---|
| REST | `/api/*` | JSON in / JSON out, bearer auth |
| WebSocket | `/ws/*` | bearer via `Sec-WebSocket-Protocol: bearer.<token>` |
| SSE | `/sse/*` | `Authorization: Bearer <token>` |
| AES envelope | `/aes/*` | embedded clients, per-device pre-shared key |
| Internal hooks | `/internal/hooks/<id>` | HMAC token, called only by `claude` inside the box |
| Static Web UI | `/`, `/assets/*` | the Svelte SPA, served from the same binary |

All examples below assume `BASE=https://a.hk1.clawf.run:8443`
and the master token in `$T`.

---

## Auth

Two token tiers:

- **Master token** — value of `CIB_AUTH_TOKEN` at boot. Carries every
  scope. Cannot be revoked through the API (you can rotate by
  restarting the container with a different env value).
- **Device tokens** — minted by the master at runtime, with a custom
  label, scope set, and optional TTL. Revocable. Each device token
  also carries an `aes_secret_hex` for AES envelope transport.

Pass the bearer token on every request:

```
curl -H "Authorization: Bearer $T" $BASE/api/sessions
```

For WebSocket, send it as a subprotocol:

```
new WebSocket(url, [`bearer.${token}`, 'json'])
```

### Scopes

| Scope | What it unlocks |
|---|---|
| `sessions:read` | List + get sessions, fetch transcripts, attach to WS / SSE |
| `sessions:write` | Create / kill sessions, model switch, interrupt |
| `sessions:input` | Send keystrokes to a session's PTY |
| `shells:read` | List shells, attach to `/ws/shells/{id}` |
| `shells:write` | Spawn / kill shells |
| `shells:input` | Send keystrokes, resize PTY |
| `fs:read` | List directories, read files (under named roots) |
| `fs:write` | Write / mkdir / delete (under named roots) |
| `providers:read` | List configured third-party endpoints |
| `providers:write` | Add / replace / delete / probe providers |
| `prefs:read` | Read default-auth preferences |
| `prefs:write` | Update default-auth preferences |
| `hooks:read` | (reserved) |
| `hooks:write` | (reserved) |
| `tokens:read` | List device tokens |
| `tokens:write` | Mint / revoke device tokens |
| `proxy:read` | (reserved — health for the redsocks chain) |
| `usage:read` | (reserved) |

### `POST /api/tokens` — mint device token

```http
POST /api/tokens
Authorization: Bearer <MASTER>
Content-Type: application/json

{ "label": "phone", "scopes": ["sessions:read","sessions:input"], "ttl_hours": 168 }
```

Response 201:

```json
{
  "token": { "id": "fc04…", "label": "phone", "scopes": ["sessions:read","sessions:input"], "created_at": "2026-05-21T12:00:00Z", "expires_at": "2026-05-28T12:00:00Z" },
  "plaintext": "ct_…",
  "aes_secret_hex": "ab12…"
}
```

`plaintext` and `aes_secret_hex` are returned ONCE here; the server
stores only their hashes / opaque copies. Keep them.

### `GET /api/tokens` — list device tokens
### `DELETE /api/tokens/{id}` — revoke

---

## Health

`GET /api/health` (public, no auth)

```json
{ "status": "ok", "mode": "default", "version": "main", "commit": "873d273…" }
```

---

## Sessions

A session is one running `claude` REPL in a PTY. Each emits structured
frames (text deltas, tool calls, todos, usage, hook events) that the
control plane fans out.

### `POST /api/sessions` — create

```json
{
  "workdir":            "/workspace",
  "model":              "claude-opus-4-7",
  "auth_mode":          "subscription",
  "oauth_token":        "...",
  "api_key":            "...",
  "provider_id":        "p_abc12345...",
  "resume_from":        "<prior-session-id>",
  "bypass_permissions": true
}
```

Auth resolution:

- `subscription` → uses `oauth_token` body field → falls back to
  `CLAUDE_CODE_OAUTH_TOKEN` env → finally falls back to the in-
  container `claude auth login` state if any.
- `api_key` → uses `api_key` body field → `provider_id` from
  `/api/providers/*` (in which case `ANTHROPIC_BASE_URL` is set to
  the provider's `api_host`) → finally `ANTHROPIC_API_KEY` env.

Response 201:

```json
{ "id": "uuid", "workdir": "...", "model": "...", "state": "starting", "created_at": "...", "last_seq": 0 }
```

### `GET /api/sessions` · `GET /api/sessions/{id}` · `DELETE /api/sessions/{id}`

DELETE accepts `?signal=term` (default) or `?signal=kill`.

### `POST /api/sessions/{id}/input` — keystrokes

```json
{ "data": "your message\n", "encoding": "utf8" }
```

### `POST /api/sessions/{id}/model` — switch model live

```json
{ "model": "claude-sonnet-4-6" }
```

### `POST /api/sessions/{id}/interrupt` — ctrl-c

### `GET /api/sessions/{id}/transcript[?from=<seq>]`

Returns the full frame array (or every frame after a given `seq` —
the resume cursor pattern for late-joining subscribers).

### `GET /ws/sessions/{id}[?from=<seq>]` — live frames

WebSocket. Server pushes JSON text frames matching the `Frame` shape
in `web/src/lib/types.ts`. Pings every 25 s. Server selects subprotocol
`json` from the client offer.

### `GET /sse/sessions/{id}[?from=<seq>]` — same but Server-Sent Events

Each line: `event: <kind>\ndata: <json>\n\n`.

---

## Shells (plain bash vTTYs)

The box runs Claude Code sessions AND lets you open standalone bash
PTYs alongside them. Each shell is its own process; multiple
subscribers per shell see byte-identical output (with a ~64 KiB
scrollback for late joiners).

| | |
|---|---|
| `GET /api/shells` | list — `{ shells: [{ id, cwd, cmd, created_at, running, exit_code? }] }` |
| `POST /api/shells` | spawn — `{ cwd?, cmd?, args?, cols?, rows? }` |
| `GET /api/shells/{id}` | one shell |
| `DELETE /api/shells/{id}[?signal=kill]` | kill + forget |
| `POST /api/shells/{id}/input` | `{ data: "..." }` ← raw keystrokes |
| `POST /api/shells/{id}/resize` | `{ cols, rows }` |
| `GET /ws/shells/{id}` | bidirectional WS, binary frames |

WS protocol:
- server → client: binary frames (raw PTY bytes)
- client → server: binary frames (raw keystrokes), OR a text frame
  `{"resize":{"cols":N,"rows":M}}` to resize without an HTTP round-trip
- subprotocol echoed: `binary`

---

## Files (constrained file browser)

| | |
|---|---|
| `GET /api/fs/roots` | `{ roots: ["workspace","claude","box"] }` |
| `GET /api/fs/list?root=X&path=Y` | one level of entries |
| `GET /api/fs/read?root=X&path=Y` | up to 4 MiB; sets `truncated: true` past the cap |
| `PUT /api/fs/write` | `{ root, path, content }` — atomic tmp+rename |
| `POST /api/fs/mkdir` | `{ root, path }` |
| `DELETE /api/fs/delete?root=X&path=Y` | one file or empty dir |

Roots are hard-coded to small allow-lists in
`internal/fsapi/fsapi.go`:

| Root | Path inside the container |
|---|---|
| `workspace` | `/workspace` |
| `claude` | `/home/coder/.claude` *(legacy mount; the live volume is `/root/.claude`)* |
| `box` | `/var/lib/claude-in-box` |

`..`-style escapes and absolute paths are rejected. Symlinks
pointing outside the root resolve through but get rejected by the
prefix check.

---

## Providers (third-party endpoints)

A *provider* is a stored `{ api_host, api_key, model? }` triple
pointing at an Anthropic-compatible upstream (the real Anthropic API,
a claude-code-router instance, a corporate proxy, etc). When a session
is created with `auth_mode: "api_key"` AND `provider_id: "<id>"`, the
box injects `ANTHROPIC_BASE_URL=<api_host>` and uses the stored key.

| | |
|---|---|
| `GET /api/providers` | list (key field is redacted to `…<last 4>`) |
| `POST /api/providers` | `{ label, api_host, api_key, model? }` |
| `PUT /api/providers/{id}` | replace — overwrites the prior secret atomically |
| `DELETE /api/providers/{id}` | gone |
| `POST /api/providers/probe` | validate `{ api_host, api_key, model? }` without saving |
| `POST /api/providers/{id}/probe` | re-validate an already-saved provider |

Probe runs `GET <api_host>/v1/models` with `x-api-key` and
`anthropic-version: 2023-06-01`. Returns:

```json
{ "ok": true, "http": 200, "endpoint": "...", "latency_ms": 412 }
```

`ok=false` with HTTP 401/403 → bad key; HTTP 404 → wrong endpoint;
network error → bad host.

Each `PUT` is a true delete-and-rewrite: the prior provider record is
removed from in-memory state and the JSON file before the new one is
written. `CreatedAt` is preserved; `UpdatedAt` advances.

---

## Preferences

| | |
|---|---|
| `GET /api/prefs` | `{ default_auth_mode, default_provider_id, default_model, updated_at }` |
| `PATCH /api/prefs` | partial — only keys actually present in the body are applied. Pass `"-"` to clear a key |

The Web UI's NewSession form reads `/api/prefs` on mount and writes
back on successful launch. This is how "once authenticated, default
to that auth" works — first launch picks `api_key + provider_id`,
subsequent launches default to the same choices.

---

## In-container `claude` auth

Drives an in-container `claude auth login --claudeai` flow so the
box can sign in against your Anthropic subscription without exposing
secrets to the host.

| | |
|---|---|
| `GET /api/auth/claude/status` | `{ loggedIn, authMethod, apiProvider, email?, subscriptionType? }` |
| `POST /api/auth/claude/start` | `{ sso?: bool, console?: bool, email? }` → returns a Flow snapshot with an `auth_url` for the user to open in a browser |
| `POST /api/auth/claude/code` | `{ flow_id, code }` — paste the one-time code back. Server validates and either advances the flow (200) or rejects with `{ error, retryable: true, snapshot }` (400). `retryable=true` means the flow stays alive and the user can paste a fresh code on the same `flow_id`. |
| `POST /api/auth/claude/cancel` | `{ flow_id }` |
| `POST /api/auth/claude/logout` | wipes the on-disk credentials |

States: `starting → awaiting_code → verifying → done` (happy path);
`failed`, `cancelled`, or `timed_out` are terminal.

---

## AES envelope (`/aes/*`)

For embedded clients (ESP32, STM32) that cannot afford TLS. Symmetric
AES-256-GCM with AAD-bound per-(method,route,timestamp) authentication.
See [docs/AES-TRANSPORT.md](AES-TRANSPORT.md) for the wire format.

Cleartext bootstrap:

| | |
|---|---|
| `GET /aes/time` | `{ server_now, tolerance_ms }` — for device clock sync |
| `GET /aes/keyinfo?id=<KeyId>` | confirm the device's KeyId is recognized |

AES envelope routes:

| | |
|---|---|
| `POST /aes/sessions/{id}/input` | encrypted keystroke push |
| `POST /aes/sessions/{id}/events/poll` | encrypted long-poll for frames |

Each request carries 4 headers:

```
Sec-CIB-Envelope: 1
Sec-CIB-KeyId:    <hex string identifying the device>
Sec-CIB-Nonce:    <24-hex-char random per-request>
Sec-CIB-Timestamp:<ms since unix epoch>
```

The body is `ciphertext || GCM tag`. AAD =
`"CIB1\n<KeyId>\n<Timestamp>\n<Method>\n<Route>\n"`.

Server responses use the same 4 headers (with a fresh nonce + the
server's clock) and the AAD `Method` is `"RESPONSE"`. Replay window
is 5 minutes; clock drift past half-window is rejected.

Errors come back as cleartext JSON with stable codes:
`BadEnvelope`, `ClockDrift`, `ReplayedNonce`, `BadTag`,
`UnknownKeyId`, `RouteForbidden`, `TooLarge`.

---

## Internal hooks receiver

`POST /internal/hooks/{session_id}?event=<event_name>`

Called by the per-session `claude` install, NOT by external clients.
Authenticated by a per-session HMAC token written into the session's
own settings.json. The body is whatever Claude Code's hook system
passes; the receiver parses it, emits a `hook` frame onto the session
bus, and may return a JSON body to mutate Claude's behavior (block a
tool call, inject context, …).

You normally do not call this yourself — but the contract is
documented at `internal/hooks/receiver.go`.

---

## Frame schema

The structured event stream emitted on `/ws/sessions/{id}`,
`/sse/sessions/{id}`, and `/aes/sessions/{id}/events/poll`. Every
frame is:

```json
{ "session": "uuid", "seq": 12, "ts": "2026-05-21T12:01:02Z", "kind": "...", "data": { ... } }
```

| `kind` | `data` shape | When |
|---|---|---|
| `meta` | `{ key, value }` | metadata changes (e.g., model switch) |
| `status` | `{ state, elapsed_ms? }` | lifecycle transitions |
| `text.delta` | `{ text }` | claude's streaming reply |
| `thinking` | `{ text }` | internal reasoning blocks (when claude emits them) |
| `tool.use.start` | `{ id, name, input }` | claude begins a tool call |
| `tool.use.result` | `{ id, name, output, error?, duration_ms? }` | tool returned |
| `todo.update` | `{ items: [...] }` | claude's TodoWrite |
| `usage` | `{ input, output, cache_read? }` | per-turn token counts |
| `ask.question` | `{ question, options? }` | claude asked the user something |
| `hook` | `{ event, name, payload }` | a hook fired (from internal receiver) |
| `pty.raw` | `{ bytes }` | (optional) raw PTY bytes — only for the terminal view |
| `cc.raw` | `{ line }` | raw JSONL line from Claude's stream-json output |
| `stop` | `{ reason }` | session ended |

`seq` is monotonic per session. Pass `?from=<last-seq>` on any
streaming endpoint to resume without gaps.

---

## Errors

Every error response is JSON:

```json
{ "error": "human-readable message" }
```

Conventional codes:

| HTTP | Meaning |
|---|---|
| 400 | bad request shape, invalid arguments, validation failed |
| 401 | missing or invalid bearer / AES envelope |
| 403 | scope check failed (token exists but lacks the right scope) |
| 404 | session / shell / file / provider not found |
| 409 | (AES) replay window violation |
| 413 | (FS) payload above the 4 MiB cap |
| 500 | unhandled server error — please report |

---

## Implementation pointers

- Source of truth for endpoint shapes:
  - REST handlers: `internal/server/*.go`
  - Frame schema: `internal/stream/frame.go`
  - AES envelope: `internal/aes/envelope.go` + `docs/AES-TRANSPORT.md`
  - Auth: `internal/auth/`
- Web UI's typed client: `web/src/lib/api.ts`, `web/src/lib/types.ts`
- C reference client for AES envelope: `clients/c/`
