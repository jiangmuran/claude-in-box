"""
Smoke + offline test for cib_aes.py. Exercises:
  - AAD format byte-for-byte against the Go server's format
  - seal/open round-trip
  - Client.post against a stand-in HTTP server that decrypts, builds a
    response envelope, and sends it back

Run with:  python3 -m unittest clients/python/test_cib_aes.py
"""

import http.server
import json
import socket
import threading
import time
import unittest
import urllib.request

from cib_aes import (
    Client, EnvelopeError, _aad, _seal, _open,
    ENVELOPE_VERSION, H_ENVELOPE, H_KEY_ID, H_NONCE, H_TIMESTAMP,
    KEY_LEN, NONCE_LEN,
)


class TestAAD(unittest.TestCase):
    def test_format(self):
        got = _aad("dev-1", 1700000000000, "POST", "/aes/x")
        want = b"CIB1\ndev-1\n1700000000000\nPOST\n/aes/x\n"
        self.assertEqual(got, want)

    def test_response_method_differs(self):
        req_aad = _aad("dev-1", 100, "POST", "/aes/x")
        resp_aad = _aad("dev-1", 100, "RESPONSE", "/aes/x")
        self.assertNotEqual(req_aad, resp_aad)


class TestSealOpenRoundtrip(unittest.TestCase):
    def test_roundtrip(self):
        secret = bytes(range(KEY_LEN))
        nonce = bytes(range(NONCE_LEN))
        aad = _aad("k", 42, "POST", "/r")
        ct = _seal(secret, nonce, aad, b"hello world")
        self.assertEqual(_open(secret, nonce, aad, ct), b"hello world")

    def test_wrong_aad_fails(self):
        secret = bytes(range(KEY_LEN))
        nonce = bytes(range(NONCE_LEN))
        aad1 = _aad("k", 1, "POST", "/r")
        aad2 = _aad("k", 2, "POST", "/r")
        ct = _seal(secret, nonce, aad1, b"x")
        with self.assertRaises(Exception):
            _open(secret, nonce, aad2, ct)


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class TestClientPost(unittest.TestCase):
    """Integration test: stand up a tiny HTTP server that talks the AES
    envelope and round-trip a request through Client.post."""

    def setUp(self):
        self.key_id = "dev-1"
        self.secret = bytes(range(KEY_LEN))
        self.port = _free_port()
        self.server_log: list[dict] = []

        secret = self.secret
        key_id = self.key_id
        log = self.server_log

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, format, *args):  # silence
                pass

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                ct = self.rfile.read(length)
                env = self.headers.get(H_ENVELOPE)
                kid = self.headers.get(H_KEY_ID)
                nonce = bytes.fromhex(self.headers.get(H_NONCE) or "")
                ts = int(self.headers.get(H_TIMESTAMP) or "0")
                aad = _aad(kid, ts, "POST", self.path)
                pt = _open(secret, nonce, aad, ct)
                log.append({"path": self.path, "body": json.loads(pt)})

                # Echo with a small wrapper so the test can assert.
                resp = {"echoed": json.loads(pt), "path": self.path}
                resp_pt = json.dumps(resp).encode()
                # Re-use the same nonce (different one would be more
                # realistic but the spec doesn't require a particular value,
                # only that the AAD method is RESPONSE).
                import os
                resp_nonce = os.urandom(NONCE_LEN)
                resp_ts = int(time.time() * 1000)
                resp_aad = _aad(kid, resp_ts, "RESPONSE", self.path)
                resp_ct = _seal(secret, resp_nonce, resp_aad, resp_pt)

                self.send_response(200)
                self.send_header(H_ENVELOPE, ENVELOPE_VERSION)
                self.send_header(H_KEY_ID, kid)
                self.send_header(H_NONCE, resp_nonce.hex())
                self.send_header(H_TIMESTAMP, str(resp_ts))
                self.send_header("Content-Type", "application/octet-stream")
                self.send_header("Content-Length", str(len(resp_ct)))
                self.end_headers()
                self.wfile.write(resp_ct)

        self.httpd = http.server.HTTPServer(("127.0.0.1", self.port), H)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()

    def tearDown(self):
        self.httpd.shutdown()

    def test_roundtrip(self):
        c = Client(
            base_url=f"http://127.0.0.1:{self.port}",
            key_id=self.key_id,
            secret=self.secret,
        )
        got = c.post("/aes/sessions/x/input", {"data": "hi\r"})
        self.assertEqual(got["echoed"], {"data": "hi\r"})
        self.assertEqual(got["path"], "/aes/sessions/x/input")
        self.assertEqual(len(self.server_log), 1)
        self.assertEqual(self.server_log[0]["body"], {"data": "hi\r"})


if __name__ == "__main__":
    unittest.main()
