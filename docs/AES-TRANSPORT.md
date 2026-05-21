# AES envelope transport

A small HTTP transport that encrypts request and response bodies with AES-256-GCM, designed for embedded devices that cannot afford a TLS stack.

If the device can speak TLS, prefer the HTTPS transport. This protocol exists for STM32-class hardware and microcontrollers where TLS is too heavy.

> Status: protocol draft. Wire format may shift before v1. Pinned in `Sec-CIB-Envelope: 1`.

## Threat model

What this protocol protects against:

- Passive eavesdroppers on the link between device and box, including a network operator.
- Replay attackers who capture a request and resend it later.
- Tampering with request or response bodies in flight.

What this protocol does not protect against:

- An attacker who steals the device's API key. Keep it in secure storage.
- Denial of service. Use a rate limiter in front of the AES route.
- A compromised endpoint device. The blast radius is whatever scopes that device's token has.
- Traffic analysis (request size, timing). Wrap in a constant-rate scheduler if that matters.

If you need an authenticated link with forward secrecy, use TLS. This protocol is a deliberately small drop-in for cases where you cannot.

## Wire shape

A request:

```
POST /aes/<route> HTTP/1.1
Host: box.example.com
Sec-CIB-Envelope: 1
Sec-CIB-KeyId: <device_key_id>
Sec-CIB-Nonce: <24 hex chars>          ; 12 bytes
Sec-CIB-Timestamp: <unix_millis>       ; current device time
Content-Type: application/octet-stream
Content-Length: ...

<ciphertext || tag>
```

A response:

```
HTTP/1.1 200 OK
Sec-CIB-Envelope: 1
Sec-CIB-Nonce: <24 hex chars>          ; server-chosen nonce
Content-Type: application/octet-stream
Content-Length: ...

<ciphertext || tag>
```

Failures use HTTP 4xx / 5xx with cleartext JSON bodies and no envelope. See "Errors" below.

## Crypto

- Algorithm: **AES-256-GCM**.
- IV: the 12-byte value in `Sec-CIB-Nonce`. Decoded from hex.
- Tag: 16-byte GCM tag, appended to the ciphertext.
- Associated data (AAD): the ASCII string

  ```
  CIB1\n<KeyId>\n<Timestamp>\n<Method>\n<Route>\n
  ```

  where the components are exactly the header values and the path (no host, no query). Including these in AAD binds the envelope to a request: the same ciphertext cannot be replayed against a different route or as a different method.

  For responses, the AAD is the same string with `<Method>` replaced by `RESPONSE` and `<Route>` unchanged.

- Plaintext: a JSON object whose schema matches the equivalent `/api/*` route. Empty body = `{}`.

## Key derivation

The control plane mints a 32-byte master device secret when the operator creates a device token via `POST /api/tokens`. The secret is returned **once** in the response and never again; the device stores it in secure storage. The token's `id` becomes the `KeyId`.

If you need per-route or per-direction keys, derive at the device:

```
device_secret  = <returned master secret>      ; 32 bytes
request_key    = HKDF-SHA256(device_secret, salt="CIB1/req", L=32)
response_key   = HKDF-SHA256(device_secret, salt="CIB1/res", L=32)
```

The default implementation uses the same key for both directions to keep firmware small. Operators who want a cleaner split can opt in via `derive_subkeys=true` when minting the token.

## Replay protection

The server maintains a sliding window of accepted `(KeyId, Nonce)` tuples for the last `replay_window = 5 minutes`.

A request is rejected if any of the following hold:

- `|server_now − Timestamp| > 5 minutes` (clock drift outside window),
- the `(KeyId, Nonce)` pair was already used in the window,
- the GCM tag does not verify.

Devices should:

- generate `Nonce` from a CSPRNG, **never** reuse one,
- keep a monotonic clock or sync via the control plane's `/aes/time` endpoint (returns server time, plaintext, see "Bootstrap"),
- on `409 ReplayedNonce`, regenerate and retry once.

## Bootstrap

The very first connection from a device cannot rely on prior state. Two cleartext helper endpoints exist for bootstrap:

- `GET /aes/time` → `{ "server_now": <unix_millis>, "tolerance_ms": 300000 }`. Use to align clocks.
- `GET /aes/keyinfo?id=<KeyId>` → `{ "id": ..., "algorithm": "aes-256-gcm", "derive_subkeys": false, "envelope": 1 }`. Use to detect rotation.

These do not require auth; they reveal nothing sensitive.

## Errors

When the server rejects an envelope, the response is cleartext JSON with no envelope:

```
HTTP/1.1 4xx
Content-Type: application/json

{ "error": "<code>", "detail": "<human-readable>" }
```

Defined codes:

| code | http | meaning |
|------|------|---------|
| `UnknownKeyId` | 401 | `Sec-CIB-KeyId` does not match any device token. |
| `ClockDrift` | 401 | `Timestamp` is outside the replay window. |
| `ReplayedNonce` | 409 | `Nonce` already seen for this `KeyId` in the window. |
| `BadTag` | 400 | GCM tag did not verify. |
| `BadEnvelope` | 400 | Missing or malformed envelope headers. |
| `RouteForbidden` | 403 | Token scope does not allow this route. |

## Worked example

Device wants to send input "hello\n" to session `abc123`:

```
device_secret = <stored 32 bytes>
plaintext     = b'{"data":"hello\\n","encoding":"utf8"}'
nonce         = csprng(12)
timestamp     = current_millis()
aad           = b"CIB1\n42\n" + str(timestamp) + b"\nPOST\n/aes/sessions/abc123/input\n"
ct, tag       = AES_256_GCM_encrypt(device_secret, nonce, aad, plaintext)
body          = ct + tag

POST /aes/sessions/abc123/input HTTP/1.1
Host: box.example.com
Sec-CIB-Envelope: 1
Sec-CIB-KeyId: 42
Sec-CIB-Nonce: <hex(nonce)>
Sec-CIB-Timestamp: <timestamp>
Content-Type: application/octet-stream

<body>
```

Server flow:

```
look up token by KeyId=42                                → device_secret
verify |server_now - timestamp| <= 5 min                 → ok
verify (42, nonce) not in replay_window                  → ok, record it
AAD = "CIB1\n42\n" + timestamp + "\nPOST\n/aes/sessions/abc123/input\n"
plaintext = AES_256_GCM_decrypt(device_secret, nonce, aad, ct, tag)
parse plaintext as the /api/sessions/:id/input body
dispatch to session manager
encrypt response with response key + server nonce
return 200
```

## Polling for stream frames

Embedded devices that can only make request/response calls poll for frames instead of holding a stream:

```
POST /aes/sessions/:id/events/poll
  plaintext: { "from": <seq>, "max": <n>, "wait_ms": <0..30000> }
```

The server returns up to `n` frames newer than `from`. If none are available, it long-polls up to `wait_ms` before returning an empty array. The device updates `from = max(seq returned)` and polls again.

For a typical "watchdog" device this is ~one request per few seconds. Battery cost is bounded; no socket-keepalive complexity.

## Reference implementation (device-side pseudocode)

```c
// Pseudocode: ~150 lines on top of mbedtls or wolfSSL's AES-GCM module.
// 1. cib_init(key_id, secret_32);
// 2. cib_set_endpoint("https?://box.example.com");
// 3. cib_call(method, route, plaintext_buf, plaintext_len,
//             out_buf, out_buf_cap, &out_len);
//      - builds AAD,
//      - encrypts plaintext with AES-GCM,
//      - sets headers,
//      - performs HTTP request (no TLS required),
//      - decrypts response,
//      - returns plaintext in out_buf.
```

A full C reference, plus MicroPython and Rust variants, will land under `clients/` once the protocol is finalized.

## Versioning

`Sec-CIB-Envelope: 1` pins the schema and crypto choices. A future revision will bump the integer and may negotiate via `Sec-CIB-Envelope-Supports`. Devices and the server must agree on the integer before any envelope is decrypted.
