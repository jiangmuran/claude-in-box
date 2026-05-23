package aes

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// SealRecord encrypts one record's plaintext and writes the length
// prefix + ciphertext+tag onto w. `counter` is the per-stream record
// index used in both the nonce and AAD; callers MUST increment it by
// exactly one per record and never reuse a value within a stream.
//
// The wire layout written:
//
//	[u16 BE len(plaintext)][ciphertext (len bytes) || 16B tag]
func SealRecord(
	w io.Writer,
	key []byte,
	h Headers,
	direction, route string,
	counter uint32,
	plaintext []byte,
) error {
	if len(key) != KeyLen {
		return fmt.Errorf("aes: key length must be %d", KeyLen)
	}
	if len(plaintext) > MaxRecordPlain {
		return ErrTooLarge
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := DeriveNonce(h.StreamID, counter)
	aad := AAD(h, direction, route, counter)
	ct := gcm.Seal(nil, nonce[:], plaintext, aad)

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(plaintext)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := w.Write(ct); err != nil {
		return err
	}
	return nil
}

// WriteTerminator writes the zero-length sentinel that closes a record
// stream. Calling SealRecord after WriteTerminator on the same writer
// produces a malformed body — the writer should be closed instead.
func WriteTerminator(w io.Writer) error {
	_, err := w.Write([]byte{0x00, 0x00})
	return err
}

// OpenRecord pulls one record from r, decrypts it, and returns the
// plaintext. Returns (nil, io.EOF) when r yields the terminator. Any
// other error (short read, decrypt failure, oversized record) is
// returned verbatim and the stream is considered dead.
//
// `counter` must match SealRecord's counter on the producer side. The
// caller is responsible for advancing it monotonically; OpenRecord
// only enforces decryption-time binding via the AAD.
func OpenRecord(
	r io.Reader,
	key []byte,
	h Headers,
	direction, route string,
	counter uint32,
	scratch []byte,
) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("aes: key length must be %d", KeyLen)
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrBadFrame
		}
		return nil, err
	}
	plainLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if plainLen == 0 {
		return nil, io.EOF
	}
	if plainLen > MaxRecordPlain {
		return nil, ErrTooLarge
	}
	wireLen := plainLen + TagLen
	if cap(scratch) < wireLen {
		scratch = make([]byte, wireLen)
	} else {
		scratch = scratch[:wireLen]
	}
	if _, err := io.ReadFull(r, scratch); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, ErrBadFrame
		}
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := DeriveNonce(h.StreamID, counter)
	aad := AAD(h, direction, route, counter)
	plain, err := gcm.Open(nil, nonce[:], scratch, aad)
	if err != nil {
		return nil, ErrBadTag
	}
	return plain, nil
}

// ----- one-shot helpers ----------------------------------------------------

// SealOneShot is the convenience for "wrap one JSON request body": it
// writes exactly one TypeJSON record holding `body` followed by the
// terminator. Use this on both client request bodies and small server
// response bodies. For long streams use Sink directly.
func SealOneShot(
	w io.Writer,
	key []byte,
	h Headers,
	direction, route string,
	innerType byte,
	body []byte,
) error {
	inner, err := EncodeInner(innerType, body)
	if err != nil {
		return err
	}
	if err := SealRecord(w, key, h, direction, route, 0, inner); err != nil {
		return err
	}
	return WriteTerminator(w)
}

// OpenOneShot is the inverse of SealOneShot. Returns (innerType,
// payload). It requires that the body contains exactly one record
// followed by the terminator; extra records yield ErrBadFrame so
// callers do not accidentally drop unread data.
func OpenOneShot(
	r io.Reader,
	key []byte,
	h Headers,
	direction, route string,
) (byte, []byte, error) {
	plain, err := OpenRecord(r, key, h, direction, route, 0, nil)
	if err != nil {
		return 0, nil, err
	}
	t, payload, err := DecodeInner(plain)
	if err != nil {
		return 0, nil, err
	}
	// Drain one more record-len; must be the terminator.
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	if binary.BigEndian.Uint16(lenBuf[:]) != 0 {
		return 0, nil, ErrBadFrame
	}
	return t, payload, nil
}

// ----- streaming Sink ------------------------------------------------------

// Sink is a stateful, monotonic record writer. It is safe to use from
// the server side of a streaming endpoint where many records flow
// before the terminator. Sink batches counter management and flushes
// each record so a chunked HTTP response delivers records to a
// long-poll client in real time.
//
// Sinks are NOT safe for concurrent use; serialize calls externally.
type Sink struct {
	w         io.Writer
	flusher   interface{ Flush() }
	key       []byte
	headers   Headers
	direction string
	route     string

	mu      sync.Mutex
	counter uint32
	closed  bool
}

// NewSink builds a Sink that emits records to w. If w is an
// http.Flusher (or wraps one), the Sink will flush after each record
// so an embedded device sees records as soon as they are produced.
func NewSink(w io.Writer, key []byte, h Headers, direction, route string) *Sink {
	s := &Sink{w: w, key: key, headers: h, direction: direction, route: route}
	if f, ok := w.(interface{ Flush() }); ok {
		s.flusher = f
	}
	return s
}

// Write seals one record carrying the inner frame (type, payload) and
// advances the counter. Returns the error from the underlying writer
// or the crypto layer; on error the Sink is left closed.
func (s *Sink) Write(innerType byte, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("aes: Sink closed")
	}
	inner, err := EncodeInner(innerType, payload)
	if err != nil {
		s.closed = true
		return err
	}
	if err := SealRecord(s.w, s.key, s.headers, s.direction, s.route, s.counter, inner); err != nil {
		s.closed = true
		return err
	}
	s.counter++
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// Heartbeat writes an empty TypeHeartbeat record. Servers should emit
// one every few seconds during idle periods so the device can
// distinguish "still alive, just waiting" from "connection dead".
func (s *Sink) Heartbeat() error { return s.Write(TypeHeartbeat, nil) }

// Close writes the terminator and marks the sink unusable. Subsequent
// Write calls error. Safe to call twice (second is a no-op).
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := WriteTerminator(s.w); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// Count returns the number of records written so far (excluding the
// terminator). Useful in tests and metrics.
func (s *Sink) Count() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counter
}

// ----- streaming Source ----------------------------------------------------

// Source is the receive-side counterpart to Sink. It reads records
// from r in monotonic counter order, decrypts each, and yields the
// inner (type, payload) tuple. Returns io.EOF when the terminator is
// reached.
//
// Sources reuse one scratch buffer across records so steady-state heap
// is one MaxRecordPlain + TagLen allocation per Source.
type Source struct {
	r         io.Reader
	key       []byte
	headers   Headers
	direction string
	route     string

	counter uint32
	scratch []byte
}

// NewSource wraps r for record-stream reading.
func NewSource(r io.Reader, key []byte, h Headers, direction, route string) *Source {
	return &Source{
		r: r, key: key, headers: h, direction: direction, route: route,
		scratch: make([]byte, MaxRecordPlain+TagLen),
	}
}

// Next returns the next (type, payload) tuple, advancing the counter.
// Returns (0, nil, io.EOF) at the terminator; on any framing/decrypt
// error the Source is dead and the caller should hang up.
func (s *Source) Next() (byte, []byte, error) {
	plain, err := OpenRecord(s.r, s.key, s.headers, s.direction, s.route, s.counter, s.scratch)
	if err != nil {
		return 0, nil, err
	}
	t, payload, err := DecodeInner(plain)
	if err != nil {
		return 0, nil, err
	}
	s.counter++
	// Defensive copy: DecodeInner returned a sub-slice into scratch,
	// which Next will overwrite on the following call.
	out := make([]byte, len(payload))
	copy(out, payload)
	return t, out, nil
}

// Count returns the number of records read so far (excluding the terminator).
func (s *Source) Count() uint32 { return s.counter }
