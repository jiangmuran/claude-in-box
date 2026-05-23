package aes

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeyLen)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func newH(t *testing.T, keyID string) Headers {
	t.Helper()
	h, err := NewHeaders(keyID, func(b []byte) error {
		_, err := rand.Read(b)
		return err
	})
	if err != nil {
		t.Fatalf("NewHeaders: %v", err)
	}
	return h
}

// ---- envelope shape / parsing --------------------------------------------

func TestParseHeaders_HappyPath(t *testing.T) {
	src := newH(t, "dev42")
	req := httptest.NewRequest("POST", "/aes/x", nil)
	src.Apply(req)

	got, err := ParseHeaders(req)
	if err != nil {
		t.Fatalf("ParseHeaders: %v", err)
	}
	if got.KeyID != "dev42" || got.StreamIDHex != src.StreamIDHex || got.TimestampMillis != src.TimestampMillis {
		t.Fatalf("parsed = %+v want %+v", got, src)
	}
	if got.StreamID != src.StreamID {
		t.Fatalf("streamID raw bytes mismatch")
	}
}

func TestParseHeaders_RejectsOldVersion(t *testing.T) {
	src := newH(t, "d")
	req := httptest.NewRequest("POST", "/x", nil)
	src.Apply(req)
	req.Header.Set(HeaderEnvelope, "1")

	if _, err := ParseHeaders(req); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported-version err, got %v", err)
	}
}

func TestParseHeaders_RejectsShortStreamID(t *testing.T) {
	src := newH(t, "d")
	req := httptest.NewRequest("POST", "/x", nil)
	src.Apply(req)
	req.Header.Set(HeaderStreamID, "deadbeef")

	if _, err := ParseHeaders(req); err == nil {
		t.Fatal("expected error for short streamID")
	}
}

func TestParseHeaders_RejectsMissingHeaders(t *testing.T) {
	cases := []string{HeaderEnvelope, HeaderKeyID, HeaderStreamID, HeaderTimestamp}
	for _, drop := range cases {
		t.Run(drop, func(t *testing.T) {
			src := newH(t, "d")
			req := httptest.NewRequest("POST", "/x", nil)
			src.Apply(req)
			req.Header.Del(drop)
			if _, err := ParseHeaders(req); err == nil {
				t.Fatalf("expected error when missing %s", drop)
			}
		})
	}
}

// ---- AAD + nonce ----------------------------------------------------------

func TestAAD_Stable(t *testing.T) {
	h := Headers{KeyID: "k", StreamIDHex: "ff00", TimestampMillis: 12345}
	got := string(AAD(h, "REQUEST", "/aes/x", 7))
	want := "CIB2\nREQUEST\nk\n/aes/x\nff00\n7\n"
	if got != want {
		t.Fatalf("AAD = %q want %q", got, want)
	}
}

func TestDeriveNonce_DistinctPerCounter(t *testing.T) {
	var sid [StreamIDLen]byte
	if _, err := rand.Read(sid[:]); err != nil {
		t.Fatal(err)
	}
	n0 := DeriveNonce(sid, 0)
	n1 := DeriveNonce(sid, 1)
	if n0 == n1 {
		t.Fatal("nonce collision across counters")
	}
	// counter is the trailing four bytes
	if binary.BigEndian.Uint32(n0[8:]) != 0 || binary.BigEndian.Uint32(n1[8:]) != 1 {
		t.Fatalf("counter encoding wrong: %x %x", n0, n1)
	}
}

// ---- inner framing --------------------------------------------------------

func TestEncodeDecodeInner_Roundtrip(t *testing.T) {
	payload := []byte("hello inner frame")
	inner, err := EncodeInner(TypeJSON, payload)
	if err != nil {
		t.Fatal(err)
	}
	tp, got, err := DecodeInner(inner)
	if err != nil {
		t.Fatal(err)
	}
	if tp != TypeJSON || !bytes.Equal(got, payload) {
		t.Fatalf("decoded type=%x payload=%q", tp, got)
	}
}

func TestEncodeInner_RejectsOversized(t *testing.T) {
	big := make([]byte, MaxRecordPlain)
	if _, err := EncodeInner(TypeJSON, big); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestDecodeInner_RejectsBadLength(t *testing.T) {
	if _, _, err := DecodeInner([]byte{0x01, 0x00, 0x05, 'a', 'b'}); err != ErrInnerLength {
		t.Fatalf("err = %v, want ErrInnerLength", err)
	}
}

// ---- record-level Seal/Open ----------------------------------------------

func TestSealOpenRecord_Roundtrip(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	inner, _ := EncodeInner(TypeJSON, []byte(`{"hi":1}`))

	var buf bytes.Buffer
	if err := SealRecord(&buf, key, h, DirectionRequest, "/aes/x", 0, inner); err != nil {
		t.Fatal(err)
	}
	plain, err := OpenRecord(&buf, key, h, DirectionRequest, "/aes/x", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	tp, payload, err := DecodeInner(plain)
	if err != nil {
		t.Fatal(err)
	}
	if tp != TypeJSON || string(payload) != `{"hi":1}` {
		t.Fatalf("payload = %q", payload)
	}
}

func TestSealOpenRecord_WrongCounterFails(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	inner, _ := EncodeInner(TypeJSON, []byte("hi"))

	var buf bytes.Buffer
	_ = SealRecord(&buf, key, h, DirectionRequest, "/aes/x", 3, inner)
	if _, err := OpenRecord(&buf, key, h, DirectionRequest, "/aes/x", 4, nil); err != ErrBadTag {
		t.Fatalf("err = %v, want ErrBadTag", err)
	}
}

func TestSealOpenRecord_WrongDirectionFails(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	inner, _ := EncodeInner(TypeJSON, []byte("hi"))

	var buf bytes.Buffer
	_ = SealRecord(&buf, key, h, DirectionRequest, "/aes/x", 0, inner)
	if _, err := OpenRecord(&buf, key, h, DirectionResponse, "/aes/x", 0, nil); err != ErrBadTag {
		t.Fatalf("err = %v, want ErrBadTag", err)
	}
}

func TestSealOpenRecord_WrongRouteFails(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	inner, _ := EncodeInner(TypeJSON, []byte("hi"))

	var buf bytes.Buffer
	_ = SealRecord(&buf, key, h, DirectionRequest, "/aes/a", 0, inner)
	if _, err := OpenRecord(&buf, key, h, DirectionRequest, "/aes/b", 0, nil); err != ErrBadTag {
		t.Fatalf("err = %v, want ErrBadTag", err)
	}
}

func TestSealOpenRecord_WrongStreamIDFails(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	inner, _ := EncodeInner(TypeJSON, []byte("hi"))

	var buf bytes.Buffer
	_ = SealRecord(&buf, key, h, DirectionRequest, "/aes/x", 0, inner)

	// Different streamID → different nonce AND different AAD → fail.
	h2 := h
	if _, err := rand.Read(h2.StreamID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRecord(&buf, key, h2, DirectionRequest, "/aes/x", 0, nil); err != ErrBadTag {
		t.Fatalf("err = %v, want ErrBadTag", err)
	}
}

func TestOpenRecord_RejectsOversizedLengthPrefix(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer
	// Lie about plain length to exceed MaxRecordPlain.
	binary.Write(&buf, binary.BigEndian, uint16(MaxRecordPlain+1))
	if _, err := OpenRecord(&buf, key, h, DirectionRequest, "/x", 0, nil); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestOpenRecord_TerminatorReturnsEOF(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer
	WriteTerminator(&buf)
	if _, err := OpenRecord(&buf, key, h, DirectionRequest, "/x", 0, nil); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestOpenRecord_TruncatedBodyIsBadFrame(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint16(16))
	buf.Write([]byte{0x01, 0x02, 0x03}) // only 3 bytes of the promised 16+16
	if _, err := OpenRecord(&buf, key, h, DirectionRequest, "/x", 0, nil); err != ErrBadFrame {
		t.Fatalf("err = %v, want ErrBadFrame", err)
	}
}

// ---- one-shot helpers -----------------------------------------------------

func TestSealOpenOneShot_Roundtrip(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	body := []byte(`{"data":"hi"}`)

	var buf bytes.Buffer
	if err := SealOneShot(&buf, key, h, DirectionRequest, "/aes/x/input", TypeJSON, body); err != nil {
		t.Fatal(err)
	}
	tp, got, err := OpenOneShot(&buf, key, h, DirectionRequest, "/aes/x/input")
	if err != nil {
		t.Fatal(err)
	}
	if tp != TypeJSON || !bytes.Equal(got, body) {
		t.Fatalf("decoded type=%x payload=%q", tp, got)
	}
}

func TestOpenOneShot_RejectsTrailingData(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer
	inner, _ := EncodeInner(TypeJSON, []byte("hi"))
	SealRecord(&buf, key, h, DirectionRequest, "/x", 0, inner)
	SealRecord(&buf, key, h, DirectionRequest, "/x", 1, inner) // extra record!
	WriteTerminator(&buf)

	if _, _, err := OpenOneShot(&buf, key, h, DirectionRequest, "/x"); err != ErrBadFrame {
		t.Fatalf("err = %v, want ErrBadFrame", err)
	}
}

// ---- Sink / Source streaming ----------------------------------------------

func TestSinkSource_StreamingRoundtrip(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer

	sink := NewSink(&buf, key, h, DirectionResponse, "/aes/x/events/stream")
	want := []string{"frame-0", "frame-1", "frame-2"}
	for _, w := range want {
		if err := sink.Write(TypeFrame, []byte(w)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	src := NewSource(&buf, key, h, DirectionResponse, "/aes/x/events/stream")
	var got []string
	var hb int
	for {
		tp, payload, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch tp {
		case TypeFrame:
			got = append(got, string(payload))
		case TypeHeartbeat:
			hb++
			if len(payload) != 0 {
				t.Fatalf("heartbeat carried payload %q", payload)
			}
		default:
			t.Fatalf("unexpected type %x", tp)
		}
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
	if hb != 1 {
		t.Fatalf("heartbeats = %d want 1", hb)
	}
}

func TestSink_RejectsAfterClose(t *testing.T) {
	key := randomKey(t)
	h := newH(t, "dev")
	var buf bytes.Buffer
	sink := NewSink(&buf, key, h, DirectionResponse, "/x")
	sink.Close()
	if err := sink.Write(TypeJSON, []byte("x")); err == nil {
		t.Fatal("expected error writing to closed sink")
	}
}

func TestSource_ReorderingFails(t *testing.T) {
	// If a relay shuffles records the AAD counter mismatch yields ErrBadTag
	// rather than silently delivering out-of-order frames.
	key := randomKey(t)
	h := newH(t, "dev")
	var direct, shuffled bytes.Buffer

	sink := NewSink(&direct, key, h, DirectionResponse, "/x")
	for i := 0; i < 3; i++ {
		_ = sink.Write(TypeJSON, []byte{byte('a' + i)})
	}
	sink.Close()

	// Re-emit records in order [1, 0, 2] without recomputing crypto.
	raw := direct.Bytes()
	// Parse: each record is [2B len][len + TagLen bytes].
	recs := make([][]byte, 0, 3)
	for i := 0; i < len(raw); {
		n := int(binary.BigEndian.Uint16(raw[i : i+2]))
		if n == 0 {
			break
		}
		total := 2 + n + TagLen
		recs = append(recs, raw[i:i+total])
		i += total
	}
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
	shuffled.Write(recs[1])
	shuffled.Write(recs[0])
	shuffled.Write(recs[2])
	WriteTerminator(&shuffled)

	src := NewSource(&shuffled, key, h, DirectionResponse, "/x")
	if _, _, err := src.Next(); err != ErrBadTag {
		t.Fatalf("err = %v, want ErrBadTag (reordering must be detected)", err)
	}
}

// ---- replay cache (preserved from v1) ------------------------------------

func TestReplayCache_StreamIDDuplicateRejected(t *testing.T) {
	cache := NewReplayCache()
	if err := cache.CheckAndRecord("dev", "abcdef0123456789abcdef0123456789"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := cache.CheckAndRecord("dev", "abcdef0123456789abcdef0123456789"); err != ErrReplay {
		t.Fatalf("second: err = %v, want ErrReplay", err)
	}
}
