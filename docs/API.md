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

### Contents

- [Auth](#auth) — tokens, scopes
- [Health](#health)
- [Sessions](#sessions) — CRUD, input, streaming, read models, send-and-wait
- [Format adapters](#anthropic-messages-api-compatibility) — Anthropic `/v1/messages`, OpenAI `/openai/v1/chat/completions`
- [Shells](#shells-plain-bash-vttys) — plain-bash vTTYs alongside Claude sessions
- [Files](#files-constrained-file-browser) — workspace + claude-home + box-data
- [Providers](#providers-third-party-endpoints) and [Preferences](#preferences) — third-party Anthropic-compatible endpoints, default auth
- [Claude auth](#in-container-claude-auth) — in-container OAuth login
- [Port mapping](#port-mapping--expose-an-in-container-service-on-a-host-port)
- [AES envelope](#aes-envelope-aes) — embedded transport (bootstrap, management, data plane)
- [Internal hooks receiver](#internal-hooks-receiver) — for the `claude` child only
- [Frame schema](#frame-schema) — what comes out of `/ws`, `/sse`, `/transcript`, `/messages`
- [Errors](#errors)
- [Implementation pointers](#implementation-pointers)

A few endpoint pairs intentionally cover the same data with different
shapes for different clients:

- **Transcript** (`/transcript`) — raw typed frame list with the full
  per-frame schema, for replay or audit.
- **Messages** (`/messages`) — the same data collapsed into chat-style
  bubbles (text deltas merged, tool calls joined to their results).
  What the Web UI's "driver" view renders.
- **Chat** (`/chat`, `/sse/.../chat`, `/aes/.../chat`) — slim shape
  trimmed for MCU-class clients: user / assistant text + one-line tool
  summaries, with cursor-based incremental polling.

Title / goal / running usage totals are first-class fields on `Session`
(set by the AES management surface; see
[AES envelope](#aes-envelope-aes)). The bearer REST surface returns
them on `GET /api/sessions/{id}` but does not yet expose a mutator —
clients that need to write them go through `PUT /aes/sessions/{id}/metadata`
or wait for the bearer equivalent (tracked as a follow-up).

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
  "effort":             "high",
  "auth_mode":          "subscription",
  "oauth_token":        "...",
  "api_key":            "...",
  "provider_id":        "p_abc12345...",
  "resume_from":        "<prior-session-id>",
  "bypass_permissions": true
}
```

`effort` (one of `low | medium | high | xhigh | max`) maps to claude's
`--effort` flag — sets the thinking depth for this session. Omit to let
claude pick its default for the model.

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

## Anthropic Messages API compatibility

cib exposes `POST /v1/messages` accepting the upstream Anthropic
Messages wire shape so any existing SDK (`@anthropic-ai/sdk`,
`anthropic` Python, raw curl) can point its `base_url` at cib and talk
through it to a subscription-backed claude.

```http
POST /v1/messages
Authorization: Bearer <TOKEN>
Content-Type: application/json

{
  "model":      "claude-opus-4-7",
  "max_tokens": 1024,
  "messages":   [{ "role": "user", "content": "say only the word READY" }],
  "system":     "Be terse."
}
```

200:
```json
{
  "id":           "msg_abc123…",
  "type":         "message",
  "role":         "assistant",
  "content":      [{ "type": "text", "text": "READY" }],
  "model":        "claude-opus-4-7",
  "stop_reason":  "end_turn",
  "stop_sequence": null,
  "usage":        { "input_tokens": 7, "output_tokens": 1 }
}
```

`stream: true` switches to SSE with `message_start` → `content_block_start`
→ `content_block_delta` (one per assistant chunk, live as cctranscript
captures it) → `content_block_stop` → `message_delta` → `message_stop`
events. Token-by-token incremental streaming — the SDK's
`messages.stream()` iterator yields each delta as it lands.

Auth picks up cib's active mode (subscription vs api_key — set via the
sign-in modal). The `/v1/messages` caller does not need to know which.

Python example:
```python
import anthropic
client = anthropic.Anthropic(
    api_key="<cib bearer token>",
    base_url="https://your-box.example.com",
)
msg = client.messages.create(
    model="claude-opus-4-7", max_tokens=1024,
    messages=[{"role": "user", "content": "hi"}],
)
print(msg.content[0].text)
```

Limits in this first cut:
- Each request spawns a per-request session (~3-5s warm-up). A warm
  session pool keyed by `bearer token + workdir` is on the roadmap.
- `tools` / `tool_use` / `tool_result` blocks are not yet translated;
  the request currently sees them as opaque text in the prompt.
- Vision input (image content blocks) is dropped.

## OpenAI Chat Completions API compatibility

`POST /openai/v1/chat/completions` accepts the OpenAI Chat Completions
wire shape and projects internally through the same Anthropic pipeline.
Lets an unmodified `openai` Python SDK or `openai-node` client talk to
cib:

```python
from openai import OpenAI
client = OpenAI(api_key="<cib bearer token>",
                base_url="https://your-box.example.com/openai/v1")
r = client.chat.completions.create(
    model="claude-opus-4-7",
    messages=[
      {"role": "system", "content": "Be terse."},
      {"role": "user",   "content": "say only the word READY"},
    ],
)
print(r.choices[0].message.content)
```

Mapping notes:

- `system`-role messages are folded into the Anthropic `system` field
  (multiple system messages concatenated with `\n\n`).
- Response shape is OpenAI's `chat.completion` with one choice carrying
  the full assistant text.
- `stream: true` emits OpenAI `chat.completion.chunk` SSE events
  (role, content, finish) ending in `data: [DONE]`. First cut returns
  the full text in one content chunk; token-by-token incremental
  streaming is on the same roadmap as Anthropic's.
- Tools / function_call / vision input not yet translated.

## Port mapping — expose an in-container service on a host port

cib ships with a `socat`-based forwarder so a service the user started
inside the container (vite dev server, fastapi, jupyter, …) can be
reached from outside without rebuilding the docker run line.

Operator must give cib a pre-allocated host range when starting the
container:

```
docker run -e CIB_PORT_RANGE=9000-9019 -p 9000-9019:9000-9019 ...
```

cib picks an unused host port from that range, runs
`socat TCP-LISTEN:<host>,fork TCP:<internal_host>:<internal_port>`,
and returns the mapping.

### `GET /api/ports`

```json
{
  "range":    [9000, 9019],
  "mappings": [
    { "host_port": 9000, "internal_port": 5173, "internal_host": "127.0.0.1", "created_at": "..." }
  ]
}
```

### `POST /api/ports/expose`

```jsonc
{ "internal_port": 5173, "internal_host": "127.0.0.1" }   // internal_host optional, defaults to 127.0.0.1
```

→ 201 `{ "host_port": 9000, "internal_port": 5173, ... }`.
503 if `CIB_PORT_RANGE` is unset, 400 if the range is full or the
internal port is invalid.

### `DELETE /api/ports/{host_port}`

Tears down the forwarder; idempotent — 404 if no mapping for that host port.

### `POST /api/sessions/{id}/send` — one-shot send-and-wait

Best path for MCU / non-streaming clients: write a prompt, block until
the next stop frame (or timeout), get only the new chat messages back.

```http
POST /api/sessions/<id>/send
Authorization: Bearer <TOKEN>
Content-Type: application/json

{ "prompt": "list the workspace files", "timeout_ms": 60000 }
```

Response (200 OK):

```json
{
  "session":  "uuid",
  "last_seq": 99,
  "messages": [
    { "seq": 86, "role": "user",      "text": "list the workspace files" },
    { "seq": 91, "role": "tool",      "tool": "Bash", "summary": "ok · 17ms" },
    { "seq": 97, "role": "assistant", "text": "no files." }
  ]
}
```

408 Request Timeout when `timeout_ms` elapsed first; body includes the
partial messages produced so far + `"partial": true`. `timeout_ms`
defaults to 60 000, capped at 300 000.

### `GET /api/sessions/{id}/transcript[?from=<seq>]`

Returns the full frame array (or every frame after a given `seq` —
the resume cursor pattern for late-joining subscribers).

### `GET /api/sessions/{id}/messages[?since=<seq>]`

Chat-shaped aggregate of the same data, mirrored to what the Web UI
renders. Each entry has a `type` discriminator:

```jsonc
[
  { "seq": 12, "type": "text", "role": "user",      "text": "list files" },
  { "seq": 18, "type": "tool", "tool": "Bash", "input": {...}, "output": ..., "tool_use_id": "..." },
  { "seq": 19, "type": "todo", "items": [...] },
  { "seq": 20, "type": "askq", "questions": [...] },
  { "seq": 24, "type": "text", "role": "assistant", "text": "no files." },
  { "seq": 28, "type": "usage", "input": 6, "output": 95 },
  { "seq": 29, "type": "stop", "reason": "end_turn", "duration_ms": 1234 }
]
```

Use this when you want the chat without parsing raw frames yourself.

### `GET /api/sessions/{id}/chat[?since=<seq>]` — embedded-slim

Same idea, payload trimmed to the minimum a small MCU (few hundred KB
RAM, HTTP/1.1 only) can fit on the heap. Only carries user/assistant
text and a one-line tool summary; thinking, todos, meta, usage and stop
are dropped:

```jsonc
{
  "session": "uuid",
  "last_seq": 42,
  "messages": [
    { "seq": 12, "role": "user",      "text": "hi" },
    { "seq": 18, "role": "tool",      "tool": "Bash", "summary": "ok · 17ms" },
    { "seq": 24, "role": "assistant", "text": "hello" }
  ]
}
```

Mirrored over AES envelope as `POST /aes/sessions/{id}/chat` (body
`{"since": <seq>}`). Polling pattern for embedded:

```
loop:
  POST /aes/sessions/<id>/input  { "data": "your prompt\r" }
  loop until session is idle:
    POST /aes/sessions/<id>/chat { "since": last_seq }
    render new messages, advance last_seq
    sleep ~500 ms
```

### `GET /sse/sessions/{id}/chat[?since=<seq>]` — true streaming for MCU

Server-Sent Events stream of the slim chat shape. HTTP/1.1 chunked, no
WebSocket upgrade, no JSON aggregation work — any line-by-line reader
can consume it:

```
GET /sse/sessions/<id>/chat?since=12 HTTP/1.1
Authorization: Bearer <TOKEN>
Accept: text/event-stream
```

```
id: 18
event: chat
data: {"seq":18,"role":"tool","tool":"Bash","summary":"running"}

id: 18
event: update
data: {"seq":18,"role":"tool","tool":"Bash","summary":"ok · 17ms"}

id: 24
event: chat
data: {"seq":24,"role":"assistant","text":"hello "}

id: 24
event: update
data: {"seq":24,"role":"assistant","text":"hello world"}

id: 29
event: stop
data: {"seq":29,"reason":"end_turn","duration_ms":1234}

:heartbeat
```

- `event: chat` — a new message appeared (full record)
- `event: update` — the previous message updated (typically assistant
  text growing or tool result landing). Carries the full latest record;
  client can blind-replace by `seq`.
- `event: stop` — current turn ended.
- `:heartbeat` — keepalive every ~25 s.
- `id:` reflects the bus seq; pass back as `Last-Event-ID` (browsers do
  this automatically) or `?since=N` to resume after a reconnect with no
  gap.

MCU receive loop (pseudo-C, fits in ~80 LoC with `mbedtls`):

```c
while (recv_line(line)) {
    if (line.starts_with(":")) continue;             // heartbeat / comment
    if (line.starts_with("event:")) cur_event = line+7;
    if (line.starts_with("data:")) {
        cur_data = line+6;
        if (blank_line_follows()) {
            handle(cur_event, cur_data);
            cur_event = "";
        }
    }
}
```

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

For embedded clients (ESP32, STM32) that cannot afford TLS. AES-256-GCM
record stream — the entire request and response bodies are sequences of
independently authenticated records terminated by a sentinel, so the
device never needs to buffer more than one record's plaintext (≤ 4 KiB).
See [docs/AES-TRANSPORT.md](AES-TRANSPORT.md) for the full wire format.

Cleartext bootstrap:

| | |
|---|---|
| `GET /aes/time` | `{ server_now, tolerance_ms, envelope, max_record_plaintext }` — clock sync + version |
| `GET /aes/keyinfo?id=<KeyId>` | confirm the device's KeyId is recognized |

AES envelope routes:

| | |
|---|---|
| **Management** | |
| `GET /aes/sessions` | list sessions (slim entries with title/goal/usage) |
| `POST /aes/sessions` | create (uses box-env credentials; accepts optional title/goal/model/workdir/resume_from) |
| `GET /aes/sessions/{id}` | one slim entry |
| `DELETE /aes/sessions/{id}` | kill (default SIGTERM; pass `{"signal":"kill"}` for SIGKILL) |
| `PUT /aes/sessions/{id}/metadata` | set `title` / `goal`; persists to `meta.json` |
| `POST /aes/sessions/{id}/model` | switch model (writes `/model <x>` into the PTY) |
| `POST /aes/sessions/{id}/interrupt` | Ctrl-C into the PTY |
| `GET /aes/sessions/{id}/usage` | running token totals (`input`, `output`, `cache_read`, `cache_write`) |
| **Data plane** | |
| `POST /aes/sessions/{id}/input` | encrypted keystroke push (one-shot) |
| `POST /aes/sessions/{id}/chat` | encrypted slim chat list, body `{"since":N}` (one-shot) |
| `POST /aes/sessions/{id}/events/stream` | encrypted record stream of frames + heartbeats |

Each request carries 4 headers:

```
Sec-CIB-Envelope:  2
Sec-CIB-KeyId:     <hex string identifying the device>
Sec-CIB-Stream:    <32-hex-char per-request random stream id>
Sec-CIB-Timestamp: <ms since unix epoch>
Content-Type:      application/cib-stream-1
```

Each record is `[u16 BE plain_len][ciphertext || 16B tag]` followed by a
`[u16 BE 0x0000]` terminator at the end of the body. Inner record
plaintext is `[u8 type][u16 BE payload_len][payload]` where type is
`0x01 (json)` for RPC bodies, `0x02 (frame)` for event-stream frames,
`0x00 (heartbeat)` for idle keep-alives, or `0x7F (stream_end)` for
final markers. AAD per record =
`"CIB2\n<direction>\n<KeyId>\n<Route>\n<StreamIDHex>\n<counter>\n"`.

Server responses carry their own fresh `Sec-CIB-Stream` (server-chosen,
distinct from the request's) plus `Sec-CIB-Timestamp`. Response records
use `direction = RESPONSE` in the AAD. Replay window is 5 minutes;
clock drift past half-window is rejected.

Errors come back as cleartext JSON with stable codes:
`BadEnvelope`, `ClockDrift`, `ReplayedNonce`, `BadTag`,
`UnknownKeyId`, `RouteForbidden`.

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
`/sse/sessions/{id}`, and `/aes/sessions/{id}/events/stream`. Every
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
