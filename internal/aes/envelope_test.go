package aes

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeyLen)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestSealOpen_Roundtrip(t *testing.T) {
	key := randomKey(t)
	h, err := NewHeaders("device-42")
	if err != nil {
		t.Fatalf("NewHeaders: %v", err)
	}
	plaintext := []byte(`{"data":"hello world","encoding":"utf8"}`)

	ct, err := Seal(key, h, "POST", "/aes/sessions/abc123/input", plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(key, h, "POST", "/aes/sessions/abc123/input", ct)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(plaintext, got) {
		t.Fatalf("roundtrip mismatch: %q vs %q", plaintext, got)
	}
}

func TestSealOpen_WrongKeyFails(t *testing.T) {
	k1, k2 := randomKey(t), randomKey(t)
	h, _ := NewHeaders("d")
	ct, _ := Seal(k1, h, "POST", "/aes/x", []byte("hi"))
	if _, err := Open(k2, h, "POST", "/aes/x", ct); err == nil {
		t.Fatal("expected Open to fail with wrong key")
	}
}

func TestSealOpen_WrongRouteFails(t *testing.T) {
	key := randomKey(t)
	h, _ := NewHeaders("d")
	ct, _ := Seal(key, h, "POST", "/aes/a", []byte("hi"))
	if _, err := Open(key, h, "POST", "/aes/b", ct); err == nil {
		t.Fatal("expected Open to fail when route differs (AAD binding)")
	}
}

func TestSealOpen_WrongMethodFails(t *testing.T) {
	key := randomKey(t)
	h, _ := NewHeaders("d")
	ct, _ := Seal(key, h, "POST", "/aes/x", []byte("hi"))
	if _, err := Open(key, h, "GET", "/aes/x", ct); err == nil {
		t.Fatal("expected Open to fail when method differs (AAD binding)")
	}
}

func TestSealOpen_TamperedTagFails(t *testing.T) {
	key := randomKey(t)
	h, _ := NewHeaders("d")
	ct, _ := Seal(key, h, "POST", "/aes/x", []byte("hi"))
	ct[len(ct)-1] ^= 0x01
	if _, err := Open(key, h, "POST", "/aes/x", ct); err == nil {
		t.Fatal("expected Open to fail when tag is flipped")
	}
}

func TestParseHeaders_HappyPath(t *testing.T) {
	src, _ := NewHeaders("dev42")
	req := httptest.NewRequest("POST", "/aes/x", nil)
	src.Apply(req)

	got, err := ParseHeaders(req)
	if err != nil {
		t.Fatalf("ParseHeaders: %v", err)
	}
	if got.KeyID != "dev42" || got.NonceHex != src.NonceHex || got.TimestampMillis != src.TimestampMillis {
		t.Fatalf("parsed = %+v want %+v", got, src)
	}
}

func TestParseHeaders_RejectsBadVersion(t *testing.T) {
	src, _ := NewHeaders("d")
	req := httptest.NewRequest("POST", "/x", nil)
	src.Apply(req)
	req.Header.Set(HeaderEnvelope, "99")

	if _, err := ParseHeaders(req); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseHeaders_RejectsBadNonceLen(t *testing.T) {
	src, _ := NewHeaders("d")
	req := httptest.NewRequest("POST", "/x", nil)
	src.Apply(req)
	req.Header.Set(HeaderNonce, "deadbeef")

	if _, err := ParseHeaders(req); err == nil {
		t.Fatal("expected error for short nonce")
	}
}

func TestParseHeaders_RejectsNonNumericTimestamp(t *testing.T) {
	src, _ := NewHeaders("d")
	req := httptest.NewRequest("POST", "/x", nil)
	src.Apply(req)
	req.Header.Set(HeaderTimestamp, "later")

	if _, err := ParseHeaders(req); err == nil {
		t.Fatal("expected error for non-numeric timestamp")
	}
}

func TestAAD_Stable(t *testing.T) {
	h := Headers{Envelope: "1", KeyID: "k", TimestampMillis: 12345}
	got := string(AAD(h, "POST", "/aes/x"))
	want := "CIB1\nk\n12345\nPOST\n/aes/x\n"
	if got != want {
		t.Fatalf("AAD = %q want %q", got, want)
	}
}

// helper for replay tests below.
func newReq(t *testing.T, key, nonce string, tsMillis int64) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", "/aes/x", nil)
	req.Header.Set(HeaderEnvelope, "1")
	req.Header.Set(HeaderKeyID, key)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(tsMillis, 10))
	return req
}

func TestReplayCache_FirstAcceptsSecondRejects(t *testing.T) {
	cache := NewReplayCache()
	if err := cache.CheckAndRecord("dev", "deadbeef"+strings.Repeat("00", 8)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := cache.CheckAndRecord("dev", "deadbeef"+strings.Repeat("00", 8)); err != ErrReplay {
		t.Fatalf("second: err = %v, want ErrReplay", err)
	}
}

func TestReplayCache_DifferentKeysIndependent(t *testing.T) {
	cache := NewReplayCache()
	n := "deadbeef" + strings.Repeat("00", 8)
	if err := cache.CheckAndRecord("a", n); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := cache.CheckAndRecord("b", n); err != nil {
		t.Fatalf("b: %v (same nonce, different key should be ok)", err)
	}
}

func TestReplayCache_TimestampDrift(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cache := NewReplayCache().WithWindow(60 * time.Second)
	cache.now = func() time.Time { return now }

	// Within half-window (=30s) — ok.
	if err := cache.CheckTimestamp(now.UnixMilli() - 20_000); err != nil {
		t.Fatalf("close-enough timestamp rejected: %v", err)
	}
	// Beyond half-window — rejected.
	if err := cache.CheckTimestamp(now.UnixMilli() - 45_000); err != ErrClockDrift {
		t.Fatalf("far timestamp accepted: %v", err)
	}
}

func TestReplayCache_EvictsAfterWindow(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cur := base
	cache := NewReplayCache().WithWindow(100 * time.Millisecond)
	cache.now = func() time.Time { return cur }

	n := "deadbeef" + strings.Repeat("00", 8)
	if err := cache.CheckAndRecord("dev", n); err != nil {
		t.Fatalf("first: %v", err)
	}
	cur = base.Add(50 * time.Millisecond)
	if err := cache.CheckAndRecord("dev", n); err != ErrReplay {
		t.Fatalf("inside window should still replay; err = %v", err)
	}
	cur = base.Add(200 * time.Millisecond)
	if err := cache.CheckAndRecord("dev", n); err != nil {
		t.Fatalf("after window, same nonce should be ok again; err = %v", err)
	}
	if got := cache.Size(); got != 1 {
		t.Fatalf("size after sweep = %d want 1", got)
	}
}
