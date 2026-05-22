"""
cib_aes — reference Python client for the claude-in-box AES envelope.

Mirror of clients/c/cib_aes.c so the protocol round-trips can be tested
in both implementations. Designed to run on CPython 3.10+ and on
MicroPython 1.22+ with `cryptography` (CPython) or `cryptolib` (MicroPython).

Wire format reference: docs/AES-TRANSPORT.md.

Usage:

    from cib_aes import Client

    c = Client(
        base_url="https://box.example.com",
        key_id="dev-1",
        secret=bytes.fromhex("<64 hex chars>"),
    )
    body = c.post("/aes/sessions/<sid>/input", {"data": "hi\\r"})
    print(body)
"""

from __future__ import annotations

import json
import os
import time
import typing as t
import urllib.parse
import urllib.request
import urllib.error

try:
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM  # CPython
    _HAVE_CPYTHON_AES = True
except ImportError:  # pragma: no cover — MicroPython fallback
    _HAVE_CPYTHON_AES = False
    try:
        from cryptolib import aes  # type: ignore
    except ImportError as e:  # pragma: no cover
        raise ImportError(
            "cib_aes requires either `cryptography` (CPython) or `cryptolib` "
            "(MicroPython 1.22+)"
        ) from e


ENVELOPE_VERSION = "1"
NONCE_LEN = 12
TAG_LEN = 16
KEY_LEN = 32

H_ENVELOPE = "Sec-CIB-Envelope"
H_KEY_ID = "Sec-CIB-KeyId"
H_NONCE = "Sec-CIB-Nonce"
H_TIMESTAMP = "Sec-CIB-Timestamp"


class EnvelopeError(Exception):
    """Raised when the server's envelope cannot be verified."""


def _aad(key_id: str, ts_ms: int, method: str, route: str) -> bytes:
    """
    Reproduces internal/aes/envelope.go's AAD format:

        CIB1\\n<KeyId>\\n<Timestamp>\\n<Method>\\n<Route>\\n
    """
    return (
        f"CIB{ENVELOPE_VERSION}\n{key_id}\n{ts_ms}\n{method}\n{route}\n"
    ).encode()


def _seal(secret: bytes, nonce: bytes, aad: bytes, plaintext: bytes) -> bytes:
    """AES-256-GCM seal. Returns ciphertext || tag (server contract)."""
    if len(secret) != KEY_LEN:
        raise ValueError(f"secret must be {KEY_LEN} bytes")
    if len(nonce) != NONCE_LEN:
        raise ValueError(f"nonce must be {NONCE_LEN} bytes")
    if _HAVE_CPYTHON_AES:
        return AESGCM(secret).encrypt(nonce, plaintext, aad)
    # MicroPython AES-GCM is not in the stdlib; the project ships an
    # ESP-IDF/mbedtls binding via the cryptolib shim. This path is not
    # exercised on CPython; tests run under cryptography.
    raise NotImplementedError(
        "MicroPython AES-GCM glue lives in cib_aes_mp.py; import that "
        "module on MicroPython targets instead."
    )


def _open(secret: bytes, nonce: bytes, aad: bytes, ciphertext: bytes) -> bytes:
    if _HAVE_CPYTHON_AES:
        return AESGCM(secret).decrypt(nonce, ciphertext, aad)
    raise NotImplementedError("see _seal note")


class Client:
    def __init__(
        self,
        base_url: str,
        key_id: str,
        secret: bytes,
        *,
        timeout: float = 30.0,
        random_source: t.Callable[[int], bytes] = os.urandom,
    ):
        if len(secret) != KEY_LEN:
            raise ValueError(f"secret must be {KEY_LEN} bytes")
        self.base_url = base_url.rstrip("/")
        self.key_id = key_id
        self.secret = secret
        self.timeout = timeout
        self.random_source = random_source

    # -------- bootstrap (cleartext) --------

    def time(self) -> dict:
        """GET /aes/time — returns server_now + tolerance_ms (cleartext)."""
        with urllib.request.urlopen(
            self.base_url + "/aes/time", timeout=self.timeout
        ) as resp:
            return json.loads(resp.read())

    def keyinfo(self) -> dict:
        """GET /aes/keyinfo (cleartext) — returns envelope version etc."""
        with urllib.request.urlopen(
            self.base_url + "/aes/keyinfo", timeout=self.timeout
        ) as resp:
            return json.loads(resp.read())

    # -------- enveloped POSTs --------

    def post(self, route: str, body: t.Any) -> t.Any:
        """
        POST `route` (path component, e.g. "/aes/sessions/<sid>/input")
        with `body` as JSON. Body is sealed inside an AES-GCM envelope;
        the server's reply is unsealed and JSON-decoded before return.
        """
        plaintext = json.dumps(body, separators=(",", ":")).encode()
        nonce = self.random_source(NONCE_LEN)
        ts_ms = int(time.time() * 1000)
        aad = _aad(self.key_id, ts_ms, "POST", route)
        sealed = _seal(self.secret, nonce, aad, plaintext)

        req = urllib.request.Request(
            self.base_url + route,
            data=sealed,
            method="POST",
            headers={
                "Content-Type": "application/octet-stream",
                H_ENVELOPE: ENVELOPE_VERSION,
                H_KEY_ID: self.key_id,
                H_NONCE: nonce.hex(),
                H_TIMESTAMP: str(ts_ms),
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                resp_headers = resp.headers
                resp_body = resp.read()
        except urllib.error.HTTPError as e:
            # Bad envelope errors are cleartext JSON; bubble up directly.
            body_bytes = e.read()
            raise EnvelopeError(
                f"HTTP {e.code}: {body_bytes.decode(errors='replace')}"
            ) from None

        resp_env = resp_headers.get(H_ENVELOPE)
        if resp_env != ENVELOPE_VERSION:
            raise EnvelopeError(
                f"server returned envelope={resp_env!r} (want {ENVELOPE_VERSION!r})"
            )
        resp_nonce_hex = resp_headers.get(H_NONCE)
        resp_ts_str = resp_headers.get(H_TIMESTAMP)
        if not resp_nonce_hex or not resp_ts_str:
            raise EnvelopeError("server response missing nonce/timestamp headers")

        resp_nonce = bytes.fromhex(resp_nonce_hex)
        resp_ts = int(resp_ts_str)
        resp_aad = _aad(self.key_id, resp_ts, "RESPONSE", route)
        plaintext_resp = _open(self.secret, resp_nonce, resp_aad, resp_body)
        if not plaintext_resp:
            return None
        return json.loads(plaintext_resp)

    # -------- convenience wrappers --------

    def session_chat(self, sid: str, since: int = 0) -> dict:
        """POST /aes/sessions/<sid>/chat — slim chat list, supports since cursor."""
        return self.post(f"/aes/sessions/{sid}/chat", {"since": since})

    def session_input(self, sid: str, data: str) -> dict:
        """POST /aes/sessions/<sid>/input — write a string into the PTY."""
        return self.post(f"/aes/sessions/{sid}/input", {"data": data})

    def events_poll(
        self,
        sid: str,
        from_seq: int = 0,
        wait_ms: int = 10_000,
        max_frames: int = 64,
    ) -> dict:
        """POST /aes/sessions/<sid>/events/poll — long-poll raw frames."""
        return self.post(
            f"/aes/sessions/{sid}/events/poll",
            {"from": from_seq, "wait_ms": wait_ms, "max": max_frames},
        )


__all__ = ["Client", "EnvelopeError"]
