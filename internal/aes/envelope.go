// Package aes implements the v2 record-stream envelope transport — the
// no-TLS path for embedded clients (ESP32 / STM32 class) that cannot
// afford a TLS stack but can run AES-GCM alongside an HTTP client.
//
// See docs/AES-TRANSPORT.md for the canonical wire-format spec. The
// short version:
//
//   - Both request and response bodies are a sequence of independently
//     authenticated AES-GCM records, terminated by a zero-length
//     sentinel. There is NO whole-body envelope.
//   - One protocol covers two shapes:
//   - One-shot calls (small JSON in, small JSON out) — exactly one
//     record + terminator in each direction.
//   - Long-lived streams (events) — many records, periodic heartbeats,
//     terminator on stream end.
//   - A device only ever needs one record-sized buffer (≤ 4096 bytes
//     plaintext) in RAM, even for a 100 KB streamed response.
//
// Wire shape:
//
//	POST /aes/<route> HTTP/1.1
//	Sec-CIB-Envelope:  2
//	Sec-CIB-KeyId:     <device key id>
//	Sec-CIB-Stream:    <32 hex chars>      ; 16 random bytes, unique per request
//	Sec-CIB-Timestamp: <unix millis>
//	Content-Type:      application/cib-stream-1
//
//	[u16 BE plain_len][ciphertext_and_tag]   ; record 0
//	[u16 BE plain_len][ciphertext_and_tag]   ; record 1
//	...
//	[u16 BE 0x0000]                          ; terminator
//
// `plain_len` is the plaintext length (0..MaxRecordPlain). Each record's
// on-the-wire length is `plain_len + TagLen` bytes following the 2-byte
// length prefix. A length of 0 with no following bytes is the
// terminator.
//
// Each record's plaintext is an inner frame:
//
//	[u8 type][u16 BE payload_len][payload]
//
// Types:
//
//	0x00  heartbeat   payload_len == 0; keeps the connection warm
//	0x01  json        payload is UTF-8 JSON — used by all RPC calls
//	0x02  frame       payload is a serialized stream.Frame — used by
//	                  /aes/sessions/:id/events/stream
//	0x7F  stream_end  payload optional, signals graceful close before
//	                  the terminator (carries e.g. final usage info)
//
// Nonce derivation (12 bytes):
//
//	nonce[0..8]  = streamID[0..8]
//	nonce[8..12] = counter (u32 BE, starts at 0, +1 per record)
//
// Because streamID is fresh CSPRNG per request, (key, nonce) is never
// reused even if two devices share a key (they will not collide with
// 2^64 probability).
//
// AAD for every record:
//
//	CIB2\n<direction>\n<keyId>\n<route>\n<streamIDHex>\n<counter>\n
//
// `direction` is "REQUEST" for client-to-server records and "RESPONSE"
// for the other way. Binding the route + direction + counter into the
// AAD makes records unforgeable across endpoints, directions, or
// positions.
package aes

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvelopeVersion is the wire version that goes in Sec-CIB-Envelope.
	// Bump together with the AAD prefix ("CIB<n>") when the format breaks.
	EnvelopeVersion = "2"

	// AADPrefix is the leading magic in every AAD. Together with the
	// envelope version header it lets a server refuse to even attempt
	// decryption when a v1 client tries to talk to a v2 server.
	AADPrefix = "CIB2"

	// NonceLen is the AES-GCM nonce width.
	NonceLen = 12
	// TagLen is the AES-GCM authentication tag width.
	TagLen = 16
	// KeyLen is the device's master secret width (AES-256).
	KeyLen = 32
	// StreamIDLen is the per-request random stream identifier (raw bytes).
	StreamIDLen = 16

	// MaxRecordPlain caps the plaintext size of a single record. Pinned so
	// embedded clients can pre-allocate exactly one fixed-size buffer at
	// boot. Servers MUST NOT emit a record with plaintext larger than this
	// and clients MAY reject larger records without decrypting.
	MaxRecordPlain = 4096

	// HeaderEnvelope etc. are the wire header names.
	HeaderEnvelope  = "Sec-CIB-Envelope"
	HeaderKeyID     = "Sec-CIB-KeyId"
	HeaderStreamID  = "Sec-CIB-Stream"
	HeaderTimestamp = "Sec-CIB-Timestamp"

	// ContentType is the MIME type for the record-stream body.
	ContentType = "application/cib-stream-1"

	// DirectionRequest / DirectionResponse appear in the AAD to keep
	// request and response records from being substituted for each
	// other. Both directions use the same key.
	DirectionRequest  = "REQUEST"
	DirectionResponse = "RESPONSE"
)

// Inner-record plaintext types.
const (
	TypeHeartbeat byte = 0x00
	TypeJSON      byte = 0x01
	TypeFrame     byte = 0x02
	TypeStreamEnd byte = 0x7F
)

// InnerHeaderLen is the size of the per-record plaintext header
// ([u8 type][u16 BE payload_len]).
const InnerHeaderLen = 3

// Headers is the per-request envelope metadata, decoded once from the
// `Sec-CIB-*` headers and reused to build every record's AAD + nonce.
type Headers struct {
	Envelope        string
	KeyID           string
	StreamID        [StreamIDLen]byte
	StreamIDHex     string
	TimestampMillis int64
}

// ParseHeaders extracts the envelope metadata from an *http.Request and
// validates the shape. It does NOT validate timestamp drift or replay —
// those checks live in ReplayCache.
func ParseHeaders(r *http.Request) (Headers, error) {
	h := Headers{
		Envelope:    r.Header.Get(HeaderEnvelope),
		KeyID:       r.Header.Get(HeaderKeyID),
		StreamIDHex: r.Header.Get(HeaderStreamID),
	}
	if h.Envelope != EnvelopeVersion {
		return h, fmt.Errorf("aes: unsupported %s=%q (want %q)", HeaderEnvelope, h.Envelope, EnvelopeVersion)
	}
	if h.KeyID == "" {
		return h, fmt.Errorf("aes: missing %s", HeaderKeyID)
	}
	if h.StreamIDHex == "" {
		return h, fmt.Errorf("aes: missing %s", HeaderStreamID)
	}
	sb, err := hex.DecodeString(h.StreamIDHex)
	if err != nil || len(sb) != StreamIDLen {
		return h, fmt.Errorf("aes: %s must be %d hex chars (%d raw bytes)", HeaderStreamID, StreamIDLen*2, StreamIDLen)
	}
	copy(h.StreamID[:], sb)

	tsStr := r.Header.Get(HeaderTimestamp)
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return h, fmt.Errorf("aes: %s must be unix millis: %v", HeaderTimestamp, err)
	}
	h.TimestampMillis = ts
	return h, nil
}

// NewHeaders generates a fresh Headers with a random streamID and the
// current wall time. Used by the test client and the reference Go
// client.
func NewHeaders(keyID string, randSource func([]byte) error) (Headers, error) {
	h := Headers{
		Envelope:        EnvelopeVersion,
		KeyID:           keyID,
		TimestampMillis: time.Now().UTC().UnixMilli(),
	}
	if randSource == nil {
		return h, errors.New("aes: NewHeaders requires a CSPRNG source")
	}
	if err := randSource(h.StreamID[:]); err != nil {
		return h, err
	}
	h.StreamIDHex = hex.EncodeToString(h.StreamID[:])
	return h, nil
}

// Apply writes the envelope headers onto r.Header. Used by the
// reference Go client and tests.
func (h Headers) Apply(r *http.Request) {
	r.Header.Set(HeaderEnvelope, h.Envelope)
	r.Header.Set(HeaderKeyID, h.KeyID)
	r.Header.Set(HeaderStreamID, h.StreamIDHex)
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(h.TimestampMillis, 10))
	r.Header.Set("Content-Type", ContentType)
}

// DeriveNonce builds the 12-byte GCM nonce for record `counter` in a
// stream. Both sides compute this independently from the streamID and
// the running counter; nonce uniqueness for the lifetime of `key`
// reduces to the uniqueness of streamID (CSPRNG per request).
func DeriveNonce(streamID [StreamIDLen]byte, counter uint32) [NonceLen]byte {
	var n [NonceLen]byte
	copy(n[:8], streamID[:8])
	binary.BigEndian.PutUint32(n[8:12], counter)
	return n
}

// AAD builds the associated-data string that binds a record to a
// single (route, direction, stream, counter) tuple. Format:
//
//	CIB2\n<direction>\n<keyId>\n<route>\n<streamIDHex>\n<counter>\n
//
// Routes are the URL path verbatim, no host or query.
func AAD(h Headers, direction, route string, counter uint32) []byte {
	var b strings.Builder
	b.Grow(len(AADPrefix) + len(direction) + len(h.KeyID) + len(route) + len(h.StreamIDHex) + 24)
	b.WriteString(AADPrefix)
	b.WriteByte('\n')
	b.WriteString(direction)
	b.WriteByte('\n')
	b.WriteString(h.KeyID)
	b.WriteByte('\n')
	b.WriteString(route)
	b.WriteByte('\n')
	b.WriteString(h.StreamIDHex)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatUint(uint64(counter), 10))
	b.WriteByte('\n')
	return []byte(b.String())
}

// EncodeInner builds the inner plaintext frame for a record:
//
//	[u8 type][u16 BE payload_len][payload]
//
// Returns ErrTooLarge if payload is larger than MaxRecordPlain - InnerHeaderLen.
func EncodeInner(t byte, payload []byte) ([]byte, error) {
	if len(payload) > MaxRecordPlain-InnerHeaderLen {
		return nil, ErrTooLarge
	}
	out := make([]byte, InnerHeaderLen+len(payload))
	out[0] = t
	binary.BigEndian.PutUint16(out[1:3], uint16(len(payload)))
	copy(out[InnerHeaderLen:], payload)
	return out, nil
}

// DecodeInner is the inverse of EncodeInner. Returns the inner type,
// payload (sub-slice into `plaintext`), and any framing error.
func DecodeInner(plaintext []byte) (byte, []byte, error) {
	if len(plaintext) < InnerHeaderLen {
		return 0, nil, ErrInnerShort
	}
	t := plaintext[0]
	n := int(binary.BigEndian.Uint16(plaintext[1:3]))
	if InnerHeaderLen+n != len(plaintext) {
		return 0, nil, ErrInnerLength
	}
	return t, plaintext[InnerHeaderLen : InnerHeaderLen+n], nil
}

// Sentinel errors for the inner framing layer. Bad outer framing is
// reported by the stream Reader as ErrBadFrame; bad crypto is reported
// as ErrBadTag (matched to the protocol error code).
var (
	ErrTooLarge    = errors.New("aes: payload exceeds MaxRecordPlain")
	ErrInnerShort  = errors.New("aes: inner frame shorter than header")
	ErrInnerLength = errors.New("aes: inner frame length mismatch")
	ErrBadFrame    = errors.New("aes: malformed outer record")
	ErrBadTag      = errors.New("aes: GCM tag verification failed")
)
