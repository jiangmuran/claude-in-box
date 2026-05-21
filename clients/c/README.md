# cib_aes — reference C client

A small, portable C client for the claude-in-box [AES envelope transport](../../docs/AES-TRANSPORT.md). Targets desktop Linux/macOS today; the same `cib_aes.c` ports cleanly to ESP-IDF and other embedded environments that have mbedtls + an HTTP client.

## Files

| File | Purpose |
|---|---|
| `cib_aes.h` | Public API (~80 lines) |
| `cib_aes.c` | Implementation (~280 lines): envelope build, AES-GCM seal/open via mbedtls, HTTP via libcurl |
| `example.c` | CLI demo — fetches `/aes/time`, sends one input line, long-polls for frames |
| `Makefile` | Builds `libcib_aes.a` and `cib-example`; uses pkg-config + brew prefix on macOS |

## Build

Install mbedtls and libcurl, then:

```
make
```

You should see `libcib_aes.a` and `cib-example`.

## Run

Get a device-scoped token from the box (replace `<MASTER>` with your `CIB_AUTH_TOKEN`):

```
curl -s -X POST http://box:8080/api/tokens \
  -H "Authorization: Bearer <MASTER>" \
  -H "Content-Type: application/json" \
  -d '{"label":"my-mcu","scopes":["sessions:input","sessions:read"]}'
```

The response contains `token.id` (use as `--key-id`) and `aes_secret_hex` (use as `--secret`). Start a session via the same Web UI or REST API and grab its `id`. Then:

```
./cib-example \
  --base    http://box:8080 \
  --key-id  $TOKEN_ID \
  --secret  $AES_SECRET_HEX \
  --session $SESSION_ID
```

The example will print the buffered frames as JSON.

## Porting to ESP-IDF

The same `cib_aes.c` works on ESP-IDF with two swaps:

1. Replace `libcurl` with `esp_http_client` (same request shape — set headers, POST a body, read the response).
2. Provide a `clock_gettime` shim. ESP-IDF has `gettimeofday`; the helper at the top of `cib_aes.c` can swap accordingly.

mbedtls is already in ESP-IDF. Memory footprint of the client itself is well under 16 KiB of code on Xtensa.

## Status

This is a reference implementation, not a production client. Things deliberately left out for clarity:

- HTTPS / TLS — by design; this transport exists because TLS is too heavy. If you need TLS, use the regular `/api/*` routes.
- Bootstrap-on-clock-drift logic — `cib_get_time` returns the offset; correcting future timestamps is the caller's job.
- Streaming / SSE — embedded clients use `cib_poll_events` instead.
- JSON parsing — we surface response bodies verbatim. Pick whatever parser fits your firmware.

For language ports (Rust, Python) and an ESP-IDF demo, see the M3 milestone in [README.md](../../README.md).
