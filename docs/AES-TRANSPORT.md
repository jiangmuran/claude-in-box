# AES envelope transport (v2 record stream)

A small HTTP transport that encrypts request and response bodies with AES-256-GCM, designed for embedded devices that cannot afford a TLS stack. v2 replaces the v1 whole-body envelope with a record stream — same crypto, but the body is now a sequence of independently authenticated records terminated by a sentinel. The same wire format covers one-shot RPCs (one record per direction) and long-lived event streams (many records per direction).

If the device can speak TLS, prefer the HTTPS transport. This protocol exists for STM32/ESP32-class hardware and microcontrollers where TLS is too heavy.

> Status: protocol v2. Pinned in `Sec-CIB-Envelope: 2`. v1 is no longer supported by the server or the reference client.

## Why the rewrite

v1 wrapped the entire request/response body in one AES-GCM envelope. That works for tiny payloads but forces the device to buffer the whole body in RAM before verifying the tag and decrypting — fatal for a 30 KB streamed assistant reply on an ESP32-class device with ~100 KB of free heap during the AI session.

v2 fixes this by:

- Splitting the body into independent records (≤ 4 KiB plaintext each, each with its own GCM tag).
- Letting the server stream records one at a time over a chunked HTTP response, with periodic heartbeat records during idle waits.
- Allowing the device to decrypt and render each record as it arrives, never holding more than one record's plaintext in RAM.

For one-shot calls (input, chat, keyinfo) v2 is exactly one record + terminator in each direction — no efficiency loss vs. v1, just a uniform parser on both ends.

## Threat model

What this protocol protects against:

- Passive eavesdroppers on the link between device and box, including a network operator.
- Replay attackers who capture a request and resend it later.
- Tampering with request or response bodies in flight.
- Record reordering by an on-path attacker: each record's AAD binds it to a position in the stream.
- Cross-endpoint replay: each record's AAD binds it to a route, method/direction, and key id.

What this protocol does not protect against:

- An attacker who steals the device's API key. Keep it in secure storage.
- Forward secrecy: a compromised key decrypts all past captured traffic. Use TLS if this matters.
- Denial of service. Use a rate limiter in front of the AES routes.
- A compromised endpoint device. The blast radius is whatever scopes that device's token has.
- Traffic analysis (record sizes, timing). Wrap in a constant-rate scheduler if it matters.

## Wire shape

### Request

```
POST /aes/<route> HTTP/1.1
Host: box.example.com
Sec-CIB-Envelope:  2
Sec-CIB-KeyId:     <device_key_id>
Sec-CIB-Stream:    <32 hex chars>          ; 16 random bytes, unique per request
Sec-CIB-Timestamp: <unix_millis>           ; current device time
Content-Type:      application/cib-stream-1
Content-Length:    ...

<record 0> <record 1> ... <terminator>
```

### Response (server picks a fresh stream id)

```
HTTP/1.1 200 OK
Sec-CIB-Envelope:  2
Sec-CIB-Stream:    <32 hex chars>          ; server-chosen, distinct from the request stream id
Sec-CIB-Timestamp: <unix_millis>
Content-Type:      application/cib-stream-1
Transfer-Encoding: chunked                 ; for streaming responses; one-shot responses set Content-Length

<record 0> <record 1> ... <terminator>
```

The client uses the response's `Sec-CIB-Stream` (not the request's) when verifying response records. The two stream ids never overlap, so request and response can never alias each other's nonces even though both directions use the same AES key.

Failures use HTTP 4xx / 5xx with cleartext JSON bodies and no envelope. See [Errors](#errors).

### Record layout

```
[u16 BE plain_len][ciphertext (plain_len bytes) || 16B GCM tag]
```

`plain_len` is the plaintext length in bytes (0..4096). The ciphertext+tag block is `plain_len + 16` bytes. A length prefix of zero with no following bytes is the **terminator** and ends the stream:

```
[u16 BE 0x0000]
```

A body without a terminator is malformed; the device MUST treat the absence as `BadEnvelope`.

### Inner frame (record plaintext)

Every record's plaintext is:

```
[u8 type][u16 BE payload_len][payload]
```

| type | meaning      | payload                                                  |
|------|--------------|----------------------------------------------------------|
| 0x00 | `heartbeat`  | empty (`payload_len == 0`); keeps the connection warm    |
| 0x01 | `json`       | UTF-8 JSON; used by all one-shot RPC bodies              |
| 0x02 | `frame`      | UTF-8 JSON, one `stream.Frame`; used by events stream    |
| 0x7F | `stream_end` | optional final marker before terminator; carries reason  |

Servers may emit multiple `json` records in sequence (a JSON body that exceeds `MaxRecordPlain` is split at byte boundaries — the client concatenates payloads to recover it). Servers MAY emit heartbeats interleaved with any other records. Servers MUST send the terminator after their last record. Clients tolerate unknown `type` values by skipping them.

## Crypto

- Algorithm: **AES-256-GCM**.
- Key: the device's 32-byte master secret (set when the token is minted via `POST /api/tokens`).
- Nonce per record (12 bytes, derived):

  ```
  nonce[0..8]  = streamID[0..8]      ; from Sec-CIB-Stream header
  nonce[8..12] = counter (u32 BE)    ; 0 for first record, +1 per record
  ```

  Because the stream id is fresh CSPRNG per HTTP call (16 random bytes), the `(key, nonce)` pair is unique even when many devices share a key. The 4-byte counter caps a single stream at 2^32 records — far beyond any reasonable use.
- Tag: 16-byte GCM tag appended to the ciphertext.
- Associated data (AAD), per record:

  ```
  CIB2\n<direction>\n<KeyId>\n<Route>\n<StreamIDHex>\n<Counter>\n
  ```

  - `direction` is `REQUEST` for client→server records and `RESPONSE` for server→client records.
  - `Route` is the path verbatim (no host, no query).
  - `StreamIDHex` is the per-direction 32-char hex from the corresponding `Sec-CIB-Stream` header.
  - `Counter` is the decimal record index in that direction.

  Including direction + route + stream id + counter makes a record unforgeable across endpoints, directions, positions, or HTTP calls.

## Replay protection

The server maintains a sliding window of accepted `(KeyId, RequestStreamID)` pairs for the last `replay_window = 5 minutes` and rejects any pair seen inside the window.

A request is rejected if any of the following hold:

- `|server_now − Timestamp| > 5 minutes` (clock drift outside window),
- the `(KeyId, RequestStreamID)` pair was already used in the window,
- any record's GCM tag does not verify,
- any record's counter is non-monotonic (this surfaces as `BadTag` because the AAD binds counter into the tag).

Devices should:

- generate `Sec-CIB-Stream` from a CSPRNG, **never** reuse one,
- keep a monotonic clock or sync via `/aes/time`,
- on `409 ReplayedNonce`, generate a fresh stream id and retry once.

## Bootstrap

The very first connection from a device cannot rely on prior state. Two cleartext helper endpoints exist for bootstrap:

- `GET /aes/time` → `{ "server_now": <unix_millis>, "tolerance_ms": 150000, "envelope": "2", "max_record_plaintext": 4096 }`. Use to align clocks and confirm v2.
- `GET /aes/keyinfo?id=<KeyId>` → `{ "id": ..., "algorithm": "aes-256-gcm", "envelope": "2", "max_record_plaintext": 4096, "content_type": "application/cib-stream-1" }`. Use to detect rotation.

These do not require auth; they reveal nothing sensitive.

## Errors

When the server rejects an envelope (or an outer transport problem fires before any record can be sealed), the response is cleartext JSON with no envelope:

```
HTTP/1.1 4xx
Content-Type: application/json

{ "error": "<code>", "detail": "<human-readable>" }
```

| code | http | meaning |
|------|------|---------|
| `UnknownKeyId` | 401 | `Sec-CIB-KeyId` does not match any device token. |
| `ClockDrift` | 401 | `Timestamp` is outside the replay window. |
| `ReplayedNonce` | 409 | `(KeyId, Sec-CIB-Stream)` already seen in the window. |
| `BadTag` | 400 | A record's GCM tag did not verify (wrong key, AAD mismatch, reordering, …). |
| `BadEnvelope` | 400 | Missing or malformed envelope headers / records, or terminator missing. |
| `BadEnvelope` | 413 | Payload exceeded record-size or record-count limits. |
| `RouteForbidden` | 403 | Token scope does not allow this route. |

In-stream failures (server starts emitting records and then hits an internal error mid-stream) are signalled by closing the TCP connection without writing the terminator. The device detects this as `BadEnvelope` and may retry with a fresh stream id.

## Endpoint catalogue

The AES routes mirror small portions of the `/api/*` REST surface — only what an embedded device actually needs.

| route | method | body (encrypted) | response |
|-------|--------|------------------|----------|
| `/aes/time` | GET | none (cleartext) | cleartext JSON, see Bootstrap |
| `/aes/keyinfo?id=...` | GET | none (cleartext) | cleartext JSON, see Bootstrap |
| **Sessions management** | | | |
| `/aes/sessions` | GET | (empty) | `{ "sessions": [ ... slim entries ... ], "count": N }` |
| `/aes/sessions` | POST | `{ "workdir":..., "model":..., "title":..., "goal":..., "resume_from":..., "bypass_permissions":bool }` | slim session entry (HTTP 201) |
| `/aes/sessions/<id>` | GET | (empty) | slim session entry |
| `/aes/sessions/<id>` | DELETE | `{ "signal":"term" | "kill" }` (optional) | slim session entry after kill |
| `/aes/sessions/<id>/metadata` | PUT | `{ "title": "...", "goal": "..." }` (both optional, both nullable for clear) | slim session entry |
| `/aes/sessions/<id>/model` | POST | `{ "model": "..." }` | `{ "id": "...", "model": "..." }` |
| `/aes/sessions/<id>/interrupt` | POST | (empty) | `{ "id": "..." }` |
| `/aes/sessions/<id>/usage` | GET | (empty) | `{ "id": "...", "usage": { "input":N, "output":N, "cache_read":N, "cache_write":N } }` |
| **Sessions data plane** | | | |
| `/aes/sessions/<id>/input` | POST | `{ "data": "...", "encoding": "utf8" }` | `{ "bytes": N }` (one-shot) |
| `/aes/sessions/<id>/chat` | POST | `{ "since": <seq> }` (since optional) | `{ "session": "...", "last_seq": N, "messages": [...] }` (one-shot, may span multiple TypeJSON records) |
| `/aes/sessions/<id>/events/stream` | POST | `{ "from": N, "kinds": [...], "max_records": N, "wait_ms": MS, "idle_hb_ms": MS }` | record stream: TypeFrame per `stream.Frame`, TypeHeartbeat per idle tick, terminator on close |

### Slim session entry

```json
{
  "id":         "<uuid>",
  "title":      "Refactor sweep",
  "goal":       "Kill 200ms p99 on /search",
  "model":      "claude-sonnet-4-6",
  "workdir":    "/workspace",
  "state":      "idle" | "working" | "waiting_for_input" | "stopped" | "failed",
  "created_at": "2026-05-23T19:00:00Z",
  "started_at": "2026-05-23T19:00:01Z",
  "last_seq":   1234,
  "usage":      { "input": 4012, "output": 781, "cache_read": 0, "cache_write": 0 }
}
```

`title` and `goal` are user-set via `PUT /aes/sessions/<id>/metadata` (or seeded by the optional `title`/`goal` fields on session create). They persist to the session's on-disk `meta.json` so they survive cib restarts. `usage` is the running total — the cctranscript watcher accumulates from every `usage` frame Claude emits.

### Auth credentials at session create

The AES create endpoint (`POST /aes/sessions`) is the *slim* subset of `POST /api/sessions`: it does NOT accept `api_key` / `oauth_token` / `provider_id` / `auth_mode` in the request body. The box resolves credentials from its own environment (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, or an in-container `claude auth login`). If you need to pass an upstream key from the request body, use the regular bearer-authenticated `POST /api/sessions` instead — embedded clients rarely need this.

### `events/stream` request fields

| field | type | default | notes |
|-------|------|---------|-------|
| `from` | uint64 | 0 | Last seq the device has already rendered. The server replays buffered frames with `seq > from` before subscribing to new ones. |
| `kinds` | string[] | unset = all | Filter frames by `Kind`. Embedded clients should subscribe to a small set (e.g., `["text.delta","status","stop","usage"]`) to reduce bandwidth and decryption load. |
| `max_records` | int | 0 = unlimited | Server emits at most this many TypeFrame records, then closes. |
| `wait_ms` | int | 30000 | Overall deadline; clamped to 0..600_000. |
| `idle_hb_ms` | int | 5000 | Cadence of TypeHeartbeat records during idle waits. Min 1000. |

## Limits (numerical)

- `MaxRecordPlain` = 4096 bytes plaintext per record.
- `MaxAESRequestRecords` = 2048 records per request body (so a request body's total plaintext is bounded at ~8 MiB).
- `replay_window` = 5 minutes.
- `events/stream wait_ms` clamp = 600_000 ms.
- `events/stream idle_hb_ms` floor = 1_000 ms.

A device that wants smaller records may negotiate by setting `max_records` low. The server cannot send a record larger than `MaxRecordPlain` plaintext bytes; clients MAY reject larger records without decrypting.

## Worked example (one-shot input)

Device wants to send input "hello\n" to session `abc123`:

```
device_secret    = <stored 32 bytes>
key_id           = "42"
route            = "/aes/sessions/abc123/input"
stream_id        = csprng(16)
stream_id_hex    = hex(stream_id)
timestamp        = current_millis()

inner_pt         = b"\x01\x00\x24"            ; type=JSON, len=36 BE
                 + b'{"data":"hello\\n","encoding":"utf8"}'

nonce_0          = stream_id[0..8] + u32be(0)
aad_0            = b"CIB2\nREQUEST\n42\n/aes/sessions/abc123/input\n"
                 + stream_id_hex + b"\n0\n"

ct_0, tag_0      = AES_256_GCM_encrypt(device_secret, nonce_0, aad_0, inner_pt)

body             = u16be(len(inner_pt)) + ct_0 + tag_0 + b"\x00\x00"

POST /aes/sessions/abc123/input HTTP/1.1
Host: box.example.com
Sec-CIB-Envelope:  2
Sec-CIB-KeyId:     42
Sec-CIB-Stream:    <stream_id_hex>
Sec-CIB-Timestamp: <timestamp>
Content-Type:      application/cib-stream-1

<body>
```

Server flow:

```
parse headers           → key_id=42, stream_id, timestamp
check timestamp drift   → ok
look up token by key id → device_secret
check replay (42, stream_id) → ok, record it
for each record in body:
    derive nonce, build AAD, gcm.Open → plaintext
    if type == 0x01 (JSON): append payload to request buffer
    if length prefix == 0:  stream ended
parse request buffer as JSON: {"data":"hello\n","encoding":"utf8"}
dispatch to session manager
encrypt response with response stream id + counter 0
write response: [u16 len][ct+tag][u16 0x0000]
```

## Worked example (events stream)

Device wants to subscribe to text + status frames for the next 30 s starting after seq 17:

```
inner_pt        = type=JSON, payload=
                  {"from":17,"kinds":["text.delta","status","stop"],
                   "wait_ms":30000,"idle_hb_ms":5000}
body            = one sealed record + terminator (same shape as above)
```

The server responds with:

```
HTTP/1.1 200 OK
Sec-CIB-Envelope: 2
Sec-CIB-Stream:   <response_stream_id_hex>
Sec-CIB-Timestamp: <server_millis>
Content-Type: application/cib-stream-1
Transfer-Encoding: chunked

[u16 len][ct+tag]   ; record 0, type=frame, payload = JSON of stream.Frame  (text.delta "Hello, ")
[u16 len][ct+tag]   ; record 1, type=frame, payload = ...                   (text.delta "world.")
[u16 len][ct+tag]   ; record 2, type=heartbeat, payload empty               (idle tick after 5 s)
[u16 len][ct+tag]   ; record 3, type=frame, payload = ...                   (status "idle")
[u16 len][ct+tag]   ; record 4, type=frame, payload = ...                   (stop "end_turn")
[u16 0x0000]        ; terminator (wait_ms elapsed, max_records hit, or session closed)
```

The device decrypts each record as it arrives — peak RAM is **one record's plaintext (≤ 4 KiB) plus a 16-byte tag scratch and a 12-byte nonce**.

## Reference implementation

The full reference C client lives in `clients/c/`. Key entry points:

```c
cib_status cib_call_oneshot(cib_client *c,
                            const char *route,
                            const uint8_t *req_json, size_t req_len,
                            uint8_t *resp_buf, size_t resp_cap, size_t *resp_len);

cib_status cib_stream(cib_client *c,
                      const char *route,
                      const uint8_t *req_json, size_t req_len,
                      cib_record_cb cb, void *ud);
```

`cib_call_oneshot` collects every TypeJSON record into a flat buffer (heartbeats and stream-end markers are dropped). `cib_stream` invokes the callback once per record as it arrives — the callback can return non-OK to abort the stream early. See `clients/c/example.c` for a full end-to-end demo.

## Versioning

`Sec-CIB-Envelope: 2` pins the schema and crypto choices. A future revision will bump the integer; the AAD prefix (`CIB2`) bumps in lockstep so a v3 server cannot accidentally decrypt v2 traffic and vice-versa. Devices and the server must agree on the integer before any envelope is decrypted.
