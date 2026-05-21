package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
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

// aesClient is a tiny client that round-trips AES envelopes against the
// httptest harness — same primitives an embedded device would implement.
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

func (c *aesClient) do(t *testing.T, method, route string, plaintext []byte) (int, []byte) {
	t.Helper()
	hdrs, err := aespkg.NewHeaders(c.keyID)
	if err != nil {
		t.Fatalf("NewHeaders: %v", err)
	}
	ct, err := aespkg.Seal(c.key, hdrs, method, route, plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	req, _ := http.NewRequest(method, c.baseURL+route, bytes.NewReader(ct))
	hdrs.Apply(req)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, route, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != 200 {
		// Server returned a cleartext error envelope per spec.
		return res.StatusCode, body
	}
	// Parse the response envelope. Server sends back its own timestamp +
	// nonce so we can rebuild the AAD identically; method is the literal
	// "RESPONSE" pseudo-method by spec.
	respH := aespkg.Headers{
		Envelope: res.Header.Get(aespkg.HeaderEnvelope),
		KeyID:    hdrs.KeyID,
		NonceHex: res.Header.Get(aespkg.HeaderNonce),
	}
	if tsStr := res.Header.Get(aespkg.HeaderTimestamp); tsStr != "" {
		if ts, perr := strconv.ParseInt(tsStr, 10, 64); perr == nil {
			respH.TimestampMillis = ts
		}
	}
	nb, err := hex.DecodeString(respH.NonceHex)
	if err != nil || len(nb) != aespkg.NonceLen {
		t.Fatalf("bad response nonce: hex=%q err=%v", respH.NonceHex, err)
	}
	copy(respH.Nonce[:], nb)
	plain, err := aespkg.Open(c.key, respH, "RESPONSE", route, body)
	if err != nil {
		t.Fatalf("Open response: %v (raw=%x)", err, body)
	}
	return res.StatusCode, plain
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
		ServerNow   int64  `json:"server_now"`
		ToleranceMs int64  `json:"tolerance_ms"`
		Envelope    string `json:"envelope"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.ServerNow < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("server_now suspicious: %d", body.ServerNow)
	}
	if body.Envelope != "1" {
		t.Fatalf("envelope = %q", body.Envelope)
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
	// Build a valid envelope, then flip a bit in the last byte.
	hdrs, _ := aespkg.NewHeaders(c.keyID)
	ct, _ := aespkg.Seal(c.key, hdrs, "POST", "/aes/sessions/x/input", []byte("{}"))
	ct[len(ct)-1] ^= 0x01
	req, _ := http.NewRequest("POST", c.baseURL+"/aes/sessions/x/input", bytes.NewReader(ct))
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

func TestAES_RejectsReplayedNonce(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	c := newAESClient(t, h)
	hdrs, _ := aespkg.NewHeaders(c.keyID)
	plain, _ := json.Marshal(inputRequest{Data: "a\n"})
	route := "/aes/sessions/" + sess.ID + "/input"
	ct, _ := aespkg.Seal(c.key, hdrs, "POST", route, plain)

	// First request with this nonce: OK.
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("POST", c.baseURL+route, bytes.NewReader(ct))
		hdrs.Apply(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		switch i {
		case 0:
			if res.StatusCode != 200 {
				t.Fatalf("first req status = %d body=%s", res.StatusCode, body)
			}
		case 1:
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("replayed req status = %d want 409 body=%s", res.StatusCode, body)
			}
		}
	}
}

func TestAES_RejectsBadKeyId(t *testing.T) {
	h := newHarness(t)
	hdrs, _ := aespkg.NewHeaders("definitely-not-a-real-key-id")
	// Even with bad key id, the envelope must parse — server validates KeyId
	// before attempting to decrypt.
	key := make([]byte, aespkg.KeyLen)
	ct, _ := aespkg.Seal(key, hdrs, "POST", "/aes/sessions/x/input", []byte("{}"))
	req, _ := http.NewRequest("POST", h.srv.URL+"/aes/sessions/x/input", bytes.NewReader(ct))
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

func TestAES_EventsPollReturnsBufferedFrames(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	<-sess.Done()
	c := newAESClient(t, h)

	plain, _ := json.Marshal(pollRequest{From: 0, Max: 32, WaitMs: 0})
	route := "/aes/sessions/" + sess.ID + "/events/poll"
	status, body := c.do(t, "POST", route, plain)
	if status != 200 {
		t.Fatalf("status = %d body=%s", status, body)
	}
	var resp struct {
		Frames  []map[string]any `json:"frames"`
		LastSeq uint64           `json:"last_seq"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if len(resp.Frames) < 3 || resp.LastSeq < 3 {
		t.Fatalf("thin response: frames=%d last_seq=%d", len(resp.Frames), resp.LastSeq)
	}
}

func TestAES_EventsPollLongPollWaits(t *testing.T) {
	h := newHarness(t)
	// Use a fresh long-lived session that has not produced any frames so
	// the long-poll has nothing to drain. A bash sleep keeps the PTY open
	// and the bus alive (CloseAll is only called when the child exits).
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
	plain, _ := json.Marshal(pollRequest{From: 999999, Max: 8, WaitMs: 250})
	route := "/aes/sessions/" + sess.ID + "/events/poll"
	start := time.Now()
	status, body := c.do(t, "POST", route, plain)
	elapsed := time.Since(start)
	if status != 200 {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("long-poll returned too fast (%v); expected >= ~250ms wait", elapsed)
	}
	var resp struct {
		Frames []map[string]any `json:"frames"`
	}
	_ = json.Unmarshal(body, &resp)
	if len(resp.Frames) != 0 {
		t.Fatalf("expected zero frames after the high `from`, got %d", len(resp.Frames))
	}
}
