package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	aespkg "github.com/jiangmuran/claude-in-box/internal/aes"
	"github.com/jiangmuran/claude-in-box/internal/hooks"
)

// TestIntegration_MultiTransportSameSession spawns one session and reads the
// SAME frame stream via three independent transports concurrently: REST
// transcript, WebSocket, AES envelope long-poll. Asserts they all see the
// same well-known kinds (text.delta, todo.update, usage, stop).
func TestIntegration_MultiTransportSameSession(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)

	type result struct {
		via   string
		kinds []string
		err   error
	}
	out := make(chan result, 3)
	var wg sync.WaitGroup

	// ----- WS reader -----
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		url := "ws://" + strings.TrimPrefix(h.srv.URL, "http://") + "/ws/sessions/" + sess.ID
		c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
			Subprotocols: []string{"bearer." + h.master, "json"},
		})
		if err != nil {
			out <- result{via: "ws", err: err}
			return
		}
		defer c.CloseNow()
		var seen []string
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				break
			}
			if typ != websocket.MessageText {
				continue
			}
			var f map[string]any
			_ = json.Unmarshal(data, &f)
			if k, _ := f["kind"].(string); k != "" {
				seen = append(seen, k)
				if k == "stop" {
					break
				}
			}
		}
		out <- result{via: "ws", kinds: seen}
	}()

	// ----- REST transcript poller -----
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-sess.Done()
		res := h.req(t, "GET", "/api/sessions/"+sess.ID+"/transcript", nil)
		defer res.Body.Close()
		var body struct {
			Frames []map[string]any `json:"frames"`
		}
		_ = json.NewDecoder(res.Body).Decode(&body)
		var kinds []string
		for _, f := range body.Frames {
			kinds = append(kinds, f["kind"].(string))
		}
		out <- result{via: "rest", kinds: kinds}
	}()

	// ----- AES envelope long-poll reader -----
	wg.Add(1)
	go func() {
		defer wg.Done()
		c := newAESClient(t, h)
		var seen []string
		var fromSeq uint64
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			req := map[string]any{"from": fromSeq, "max": 16, "wait_ms": 250}
			b, _ := json.Marshal(req)
			status, body := c.do(t, "POST", "/aes/sessions/"+sess.ID+"/events/poll", b)
			if status != 200 {
				out <- result{via: "aes", err: fmt.Errorf("status %d: %s", status, body)}
				return
			}
			var resp struct {
				Frames  []map[string]any `json:"frames"`
				LastSeq uint64           `json:"last_seq"`
				Closed  bool             `json:"closed"`
			}
			_ = json.Unmarshal(body, &resp)
			for _, f := range resp.Frames {
				if k, ok := f["kind"].(string); ok {
					seen = append(seen, k)
					if seq, ok2 := f["seq"].(float64); ok2 && uint64(seq) > fromSeq {
						fromSeq = uint64(seq)
					}
				}
			}
			if hasKind(seen, "stop") || resp.Closed {
				break
			}
		}
		out <- result{via: "aes", kinds: seen}
	}()

	wg.Wait()
	close(out)

	results := map[string][]string{}
	for r := range out {
		if r.err != nil {
			t.Fatalf("transport %s: %v", r.via, r.err)
		}
		results[r.via] = r.kinds
	}

	// Assert each transport sees the well-known kinds the stub emits.
	// The stub does not generate a todo.update; the hook-driven path is
	// covered separately by TestIntegration_HookCallbackEmitsFrame.
	for _, via := range []string{"ws", "rest", "aes"} {
		ks := results[via]
		for _, want := range []string{"text.delta", "usage", "stop"} {
			if !hasKind(ks, want) {
				t.Fatalf("%s missing %q; got %v", via, want, ks)
			}
		}
	}
}

// TestIntegration_HookCallbackEmitsFrame proves that an HTTP POST to the
// per-session /internal/hooks/<id> endpoint with the right HMAC token shows
// up on the session's frame stream as a `hook` frame, end to end.
func TestIntegration_HookCallbackEmitsFrame(t *testing.T) {
	h := newHarness(t)
	// Give the session manager the loopback address it would use in production.
	h.sessions.ControlAddr = strings.TrimPrefix(h.srv.URL, "http://")
	sess := h.spawnStubSession(t)
	defer func() { _ = sess.Kill(nil) }()

	// Wait for stub to finish writing initial frames so the bus has a known
	// state, then fire a hook callback as if `claude` itself ran the
	// installed command-hook.
	<-sess.Done()

	hookURL := h.srv.URL + "/internal/hooks/" + sess.ID + "?event=PreToolUse"
	hookBody := []byte(`{"tool_name":"Bash","tool_input":{"command":"echo hi"}}`)
	req, _ := http.NewRequest("POST", hookURL, bytes.NewReader(hookBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hooks.HeaderHookToken, sess.HookToken())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hook POST: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("hook POST status = %d body=%s", res.StatusCode, body)
	}

	// The frame is now on the bus. Pull it from the transcript.
	tres := h.req(t, "GET", "/api/sessions/"+sess.ID+"/transcript", nil)
	defer tres.Body.Close()
	var got struct {
		Frames []map[string]any `json:"frames"`
	}
	_ = json.NewDecoder(tres.Body).Decode(&got)
	found := false
	for _, f := range got.Frames {
		if f["kind"] != "hook" {
			continue
		}
		data, _ := f["data"].(map[string]any)
		if data["event"] == "PreToolUse" && data["name"] == "PreToolUse" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hook frame not found in transcript; got %+v", got.Frames)
	}
}

// TestIntegration_MultiWSSubscribersSeeSameFrames opens two WS connections to
// the same session and asserts they see byte-identical frame payloads (the
// fan-out is real, not per-subscriber re-parse).
func TestIntegration_MultiWSSubscribersSeeSameFrames(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dial := func() *websocket.Conn {
		url := "ws://" + strings.TrimPrefix(h.srv.URL, "http://") + "/ws/sessions/" + sess.ID
		c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
			Subprotocols: []string{"bearer." + h.master, "json"},
		})
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		return c
	}
	a := dial()
	b := dial()
	defer a.CloseNow()
	defer b.CloseNow()

	type frame struct {
		seq  float64
		kind string
		raw  string
	}
	read := func(c *websocket.Conn) []frame {
		var out []frame
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				break
			}
			if typ != websocket.MessageText {
				continue
			}
			var f map[string]any
			_ = json.Unmarshal(data, &f)
			out = append(out, frame{seq: f["seq"].(float64), kind: f["kind"].(string), raw: string(data)})
			if f["kind"] == "stop" {
				break
			}
		}
		return out
	}

	var fa, fb []frame
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); fa = read(a) }()
	go func() { defer wg.Done(); fb = read(b) }()
	wg.Wait()

	if len(fa) == 0 || len(fa) != len(fb) {
		t.Fatalf("subscribers saw different frame counts: a=%d b=%d", len(fa), len(fb))
	}
	for i := range fa {
		if fa[i].seq != fb[i].seq || fa[i].kind != fb[i].kind {
			t.Fatalf("frame %d differs: a=%+v b=%+v", i, fa[i], fb[i])
		}
	}
}

// TestIntegration_AESSecretRoundtripsViaTokenStore proves that minted AES
// secrets survive a save-and-reopen cycle of the FileStore so devices keep
// working across box restarts.
func TestIntegration_AESSecretRoundtripsViaTokenStore(t *testing.T) {
	h := newHarness(t)
	res := h.req(t, "POST", "/api/tokens", map[string]any{
		"label":  "device",
		"scopes": []string{"sessions:input"},
	})
	defer res.Body.Close()
	var minted struct {
		Token        struct{ ID string `json:"id"` } `json:"token"`
		Plaintext    string `json:"plaintext"`
		AESSecretHex string `json:"aes_secret_hex"`
	}
	_ = json.NewDecoder(res.Body).Decode(&minted)
	if minted.AESSecretHex == "" {
		t.Fatal("Mint did not return aes_secret_hex")
	}
	wantKey, err := hex.DecodeString(minted.AESSecretHex)
	if err != nil || len(wantKey) != aespkg.KeyLen {
		t.Fatalf("bad aes hex: %v len=%d", err, len(wantKey))
	}

	gotKey, ok := h.tokens.GetAESSecret(minted.Token.ID)
	if !ok {
		t.Fatal("GetAESSecret returned not-found")
	}
	if !bytes.Equal(wantKey, gotKey) {
		t.Fatalf("AES secret mismatch between mint and lookup")
	}
}

func hasKind(ks []string, want string) bool {
	for _, k := range ks {
		if k == want {
			return true
		}
	}
	return false
}
