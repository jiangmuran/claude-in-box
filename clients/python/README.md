# `cib_aes` — Python client for the claude-in-box AES envelope

Mirror of [`clients/c/cib_aes.c`](../c/cib_aes.c). One file
(`cib_aes.py`), no setup tools, talks to the box via its
[AES envelope transport](../../docs/AES-TRANSPORT.md).

## Why

The cleartext REST / WS / SSE surfaces are great when you have TLS at
the door. When you don't — a Pi behind NAT, an ESP32 at a customer
site, a script running on a coworker's laptop — the AES envelope lets
you ship a long-lived shared secret instead and skip cert wrangling.

This module is the reference for non-MCU clients. The C client
([`../c/`](../c/)) is the canonical low-spec implementation; the two
have to stay byte-compatible.

## Install

CPython 3.10+:

```
pip install cryptography
```

MicroPython 1.22+ on ESP32/ESP-IDF: import `cryptolib` instead and use
the shim (TODO).

## Usage

```python
from cib_aes import Client

c = Client(
    base_url="https://box.example.com",
    key_id="dev-1",
    secret=bytes.fromhex("…64 hex chars…"),   # the 32-byte AES-256 key
)

# 1. Send a prompt and wait for the assistant reply (slim chat shape)
sid = "<your cib session id>"
c.session_input(sid, "summarise /workspace\r")
state = c.session_chat(sid)
for m in state["messages"]:
    print(f"{m['role']:>9} · {m.get('text') or m.get('tool')}")
```

See [`test_cib_aes.py`](./test_cib_aes.py) for the integration test —
it stands up a tiny HTTP server speaking the envelope protocol and
round-trips a request, which is also the cheapest way to verify your
server's envelope matches the spec.

## Run the tests

```
cd clients/python && python3 -m unittest test_cib_aes -v
```

5 test cases:

- AAD format (byte-for-byte against `internal/aes/envelope.go`'s
  `AAD()`)
- seal / open round-trip
- AAD-mismatch failure
- end-to-end POST through a stub server (decrypt request, build
  response envelope with `method="RESPONSE"`, send back)
- response-method binding (request and response envelopes are not
  interchangeable)

No network required.
