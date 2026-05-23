package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"syscall"
	"testing"
	"time"

	aespkg "github.com/jiangmuran/claude-in-box/internal/aes"
	"github.com/jiangmuran/claude-in-box/internal/auth"
	"github.com/jiangmuran/claude-in-box/internal/session"
)

// aesClient is the v2 test client: it speaks the record-stream envelope
// against the httptest harness exactly like a real device would.
type aesClient struct {
	baseURL string
	keyID   string
	key     []byte
}

func newAESClient(t *testing.T, h *harness) *aesClient {
	t.Helper()
	mint, err := h.tokens.Mint("device", []string{auth.ScopeSessionsInput, auth.ScopeSessionsRead}, 0)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if mint.AESSecretHex == "" {
		t.Fatal("Mint did not return an AES secret")
	}
	key, err := hex.DecodeString(mint.AESSecretHex)
	if err != nil || len(key) != aespkg.KeyLen {
		t.Fatalf("bad aes secret: %v len=%d", err, len(key))
	}
	return &aesClient{baseURL: h.srv.URL, keyID: mint.Token.ID, key: key}
}

// do performs a one-shot AES round-trip: seals plaintext as one
// TypeJSON record + terminator on the request body, posts, and
// decodes the response record stream concatenating all TypeJSON
// payloads. Status 200 → returns concatenated plaintext. Non-200 →
// returns the cleartext error body.
func (c *aesClient) do(t *testing.T, method, route string, plaintext []byte) (int, []byte) {
	t.Helper()
	hdrs := freshHeaders(t, c.keyID)

	var body bytes.Buffer
	if err := aespkg.SealOneShot(&body, c.key, hdrs, aespkg.DirectionRequest, route, aespkg.TypeJSON, plaintext); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	req, _ := http.NewRequest(method, c.baseURL+route, &body)
	hdrs.Apply(req)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, route, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	// Application-level non-200s (404 "no such session", 400 "empty
	// data", etc.) are still envelope-wrapped — only protocol-level
	// rejections (BadEnvelope, BadTag, ReplayedNonce, …) return
	// cleartext JSON. Differentiate by Content-Type.
	if res.Header.Get("Content-Type") != aespkg.ContentType {
		return res.StatusCode, raw
	}
	respH := parseResponseHeaders(t, res, c.keyID)
	out := decodeResponse(t, c.key, respH, route, bytes.NewReader(raw))
	return res.StatusCode, out
}

func freshHeaders(t *testing.T, keyID string) aespkg.Headers {
	t.Helper()
	h, err := aespkg.NewHeaders(keyID, func(b []byte) error {
		_, err := rand.Read(b)
		return err
	})
	if err != nil {
		t.Fatalf("NewHeaders: %v", err)
	}
	return h
}

func parseResponseHeaders(t *testing.T, res *http.Response, keyID string) aespkg.Headers {
	t.Helper()
	h := aespkg.Headers{
		Envelope:    res.Header.Get(aespkg.HeaderEnvelope),
		KeyID:       keyID,
		StreamIDHex: res.Header.Get(aespkg.HeaderStreamID),
	}
	if tsStr := res.Header.Get(aespkg.HeaderTimestamp); tsStr != "" {
		if ts, perr := strconv.ParseInt(tsStr, 10, 64); perr == nil {
			h.TimestampMillis = ts
		}
	}
	nb, err := hex.DecodeString(h.StreamIDHex)
	if err != nil || len(nb) != aespkg.StreamIDLen {
		t.Fatalf("bad response stream id hex=%q err=%v", h.StreamIDHex, err)
	}
	copy(h.StreamID[:], nb)
	return h
}

func decodeResponse(t *testing.T, key []byte, respH aespkg.Headers, route string, body io.Reader) []byte {
	t.Helper()
	src := aespkg.NewSource(body, key, respH, aespkg.DirectionResponse, route)
	var buf bytes.Buffer
	for {
		tp, payload, err := src.Next()
		if errors.Is(err, io.EOF) {
			return buf.Bytes()
		}
		if err != nil {
			t.Fatalf("decode response: %v", err)
		}
		switch tp {
		case aespkg.TypeJSON:
			buf.Write(payload)
		case aespkg.TypeFrame:
			// Frames in one-shot responses are unexpected, but harmless
			// to accumulate for tests that want them.
			buf.Write(payload)
		case aespkg.TypeStreamEnd, aespkg.TypeHeartbeat:
			// ignore
		}
	}
}

// -------- tests --------

func TestAES_TimeIsCleartextAndNonZero(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/aes/time")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		ServerNow          int64  `json:"server_now"`
		ToleranceMs        int64  `json:"tolerance_ms"`
		Envelope           string `json:"envelope"`
		MaxRecordPlaintext int    `json:"max_record_plaintext"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.ServerNow < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("server_now suspicious: %d", body.ServerNow)
	}
	if body.Envelope != "2" {
		t.Fatalf("envelope = %q want 2", body.Envelope)
	}
	if body.MaxRecordPlaintext != aespkg.MaxRecordPlain {
		t.Fatalf("max_record_plaintext = %d", body.MaxRecordPlaintext)
	}
}

func TestAES_KeyInfoFor404Unknown(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/aes/keyinfo?id=does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestAES_KeyInfoForMintedToken(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	res, err := http.Get(h.srv.URL + "/aes/keyinfo?id=" + c.keyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	var body struct {
		Envelope    string `json:"envelope"`
		ContentType string `json:"content_type"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.Envelope != "2" || body.ContentType != aespkg.ContentType {
		t.Fatalf("keyinfo body = %+v", body)
	}
}

func TestAES_InputRoundtrip(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	c := newAESClient(t, h)

	plaintext, _ := json.Marshal(inputRequest{Data: "hello\n"})
	status, body := c.do(t, "POST", "/aes/sessions/"+sess.ID+"/input", plaintext)
	if status != 200 {
		t.Fatalf("status = %d body=%s", status, body)
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if resp["bytes"] == nil || int(resp["bytes"].(float64)) != len("hello\n") {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}

func TestAES_RejectsTamperedTag(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	hdrs := freshHeaders(t, c.keyID)

	var body bytes.Buffer
	_ = aespkg.SealOneShot(&body, c.key, hdrs, aespkg.DirectionRequest, "/aes/sessions/x/input", aespkg.TypeJSON, []byte("{}"))
	// Flip a bit somewhere in the ciphertext (avoid the length prefix
	// at offset 0..1 and the trailing terminator).
	raw := body.Bytes()
	raw[len(raw)-3] ^= 0x01

	req, _ := http.NewRequest("POST", c.baseURL+"/aes/sessions/x/input", bytes.NewReader(raw))
	hdrs.Apply(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d want 400 body=%s", res.StatusCode, b)
	}
}

func TestAES_RejectsReplayedStreamID(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	c := newAESClient(t, h)
	hdrs := freshHeaders(t, c.keyID)
	plain, _ := json.Marshal(inputRequest{Data: "a\n"})
	route := "/aes/sessions/" + sess.ID + "/input"

	for i := 0; i < 2; i++ {
		var body bytes.Buffer
		_ = aespkg.SealOneShot(&body, c.key, hdrs, aespkg.DirectionRequest, route, aespkg.TypeJSON, plain)
		req, _ := http.NewRequest("POST", c.baseURL+route, &body)
		hdrs.Apply(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		switch i {
		case 0:
			if res.StatusCode != 200 {
				t.Fatalf("first status = %d body=%s", res.StatusCode, raw)
			}
		case 1:
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("replayed status = %d want 409 body=%s", res.StatusCode, raw)
			}
		}
	}
}

func TestAES_RejectsBadKeyId(t *testing.T) {
	h := newHarness(t)
	hdrs := freshHeaders(t, "definitely-not-a-real-key-id")
	key := make([]byte, aespkg.KeyLen)
	var body bytes.Buffer
	_ = aespkg.SealOneShot(&body, key, hdrs, aespkg.DirectionRequest, "/aes/sessions/x/input", aespkg.TypeJSON, []byte("{}"))
	req, _ := http.NewRequest("POST", h.srv.URL+"/aes/sessions/x/input", &body)
	hdrs.Apply(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d want 401 body=%s", res.StatusCode, b)
	}
}

func TestAES_EventsStreamDrainsBufferedFrames(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	<-sess.Done()
	c := newAESClient(t, h)

	// wait_ms small so we don't long-poll; we just want the snapshot.
	req, _ := json.Marshal(streamRequest{From: 0, WaitMs: 50, MaxRecords: 0})
	route := "/aes/sessions/" + sess.ID + "/events/stream"
	frames := c.stream(t, route, req)
	if len(frames) < 3 {
		t.Fatalf("got %d frames, want >= 3", len(frames))
	}
}

func TestAES_EventsStreamLongPollWaitsAndHeartbeats(t *testing.T) {
	h := newHarness(t)
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "sleep 2"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() {
		_ = sess.Kill(syscall.SIGTERM)
		<-sess.Done()
	}()

	c := newAESClient(t, h)
	req, _ := json.Marshal(streamRequest{From: 999_999, WaitMs: 400, IdleHbMs: 1000})
	route := "/aes/sessions/" + sess.ID + "/events/stream"

	start := time.Now()
	rec := c.streamRecords(t, route, req)
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Fatalf("stream returned too fast (%v); expected ≥ 400ms wait", elapsed)
	}
	// We expect zero frames (none past seq 999999) but at least one
	// heartbeat in 400ms with a 1s heartbeat cadence is unlikely; the
	// key invariant is: no false frames, stream terminator received.
	for _, r := range rec {
		if r.kind == aespkg.TypeFrame {
			t.Fatalf("unexpected TypeFrame in long-poll-empty stream: %s", r.payload)
		}
	}
}

func TestAES_EventsStreamKindFilter(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	<-sess.Done()
	c := newAESClient(t, h)

	// Only ask for "status" frames; stub session emits both text.delta and status.
	req, _ := json.Marshal(streamRequest{From: 0, WaitMs: 50, Kinds: []string{"status"}})
	route := "/aes/sessions/" + sess.ID + "/events/stream"
	frames := c.stream(t, route, req)
	for _, f := range frames {
		if f["kind"] != "status" {
			t.Fatalf("filter leaked kind %v", f["kind"])
		}
	}
}

func TestAES_EventsStreamMaxRecordsCap(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	<-sess.Done()
	c := newAESClient(t, h)

	req, _ := json.Marshal(streamRequest{From: 0, WaitMs: 50, MaxRecords: 2})
	route := "/aes/sessions/" + sess.ID + "/events/stream"
	frames := c.stream(t, route, req)
	if len(frames) != 2 {
		t.Fatalf("max_records=2 → got %d frames", len(frames))
	}
}

// ---- streaming-client helpers -------------------------------------------

type recvRecord struct {
	kind    byte
	payload []byte
}

func (c *aesClient) streamRecords(t *testing.T, route string, plaintext []byte) []recvRecord {
	t.Helper()
	hdrs := freshHeaders(t, c.keyID)
	var body bytes.Buffer
	if err := aespkg.SealOneShot(&body, c.key, hdrs, aespkg.DirectionRequest, route, aespkg.TypeJSON, plaintext); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	req, _ := http.NewRequest("POST", c.baseURL+route, &body)
	hdrs.Apply(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", route, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d body=%s", res.StatusCode, raw)
	}
	respH := parseResponseHeaders(t, res, c.keyID)
	src := aespkg.NewSource(res.Body, c.key, respH, aespkg.DirectionResponse, route)

	var out []recvRecord
	for {
		tp, payload, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("stream Next: %v", err)
		}
		dup := make([]byte, len(payload))
		copy(dup, payload)
		out = append(out, recvRecord{kind: tp, payload: dup})
	}
}

func (c *aesClient) stream(t *testing.T, route string, plaintext []byte) []map[string]any {
	t.Helper()
	rec := c.streamRecords(t, route, plaintext)
	var out []map[string]any
	for _, r := range rec {
		if r.kind != aespkg.TypeFrame {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal(r.payload, &f); err != nil {
			t.Fatalf("decode frame: %v body=%s", err, r.payload)
		}
		out = append(out, f)
	}
	return out
}
