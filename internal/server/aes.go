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
	"time"

	aespkg "github.com/jiangmuran/claude-in-box/internal/aes"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// MaxAESRequestRecords caps how many records a single request body
// may contain before the server gives up reading. With MaxRecordPlain
// = 4096, this allows ~8 MiB of plaintext per request, matching the
// previous v1 MaxAESBody.
const MaxAESRequestRecords = 2048

// -------- cleartext bootstrap endpoints -------------------------------------

func (s *Server) aesTime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"server_now":           time.Now().UTC().UnixMilli(),
		"tolerance_ms":         int64(aespkg.DefaultReplayWindow / time.Millisecond / 2),
		"envelope":             aespkg.EnvelopeVersion,
		"max_record_plaintext": aespkg.MaxRecordPlain,
	})
}

func (s *Server) aesKeyInfo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeAESError(w, http.StatusBadRequest, "BadEnvelope", "missing id")
		return
	}
	if _, ok := s.cfg.Tokens.GetAESSecret(id); !ok {
		writeAESError(w, http.StatusNotFound, "UnknownKeyId", "no AES secret for that key id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                   id,
		"algorithm":            "aes-256-gcm",
		"envelope":             aespkg.EnvelopeVersion,
		"max_record_plaintext": aespkg.MaxRecordPlain,
		"content_type":         aespkg.ContentType,
	})
}

// -------- envelope plumbing -------------------------------------------------

// envelopeRoute is the canonical AAD route used by both sides. The
// actual HTTP path includes the dynamic session id; we keep the path
// verbatim so devices can build AAD without a route table.
func envelopeRoute(r *http.Request) string { return r.URL.Path }

// envelopeCtx bundles per-request decrypt state — the parsed headers
// and the device key. Returned by readEnvelope1 / readEnvelopeStream
// so handlers can later seal the response with the same key.
type envelopeCtx struct {
	reqHdrs aespkg.Headers
	key     []byte
}

// readEnvelope1 reads exactly one inner JSON record from the request
// body plus the terminator and returns the decoded plaintext. Used by
// every endpoint whose request body is one small JSON object.
//
// Inner records of TypeStreamEnd or TypeHeartbeat between the JSON
// record and the terminator are tolerated for forward compatibility
// (a client that wants to send keep-alives is welcome to). Multiple
// TypeJSON records are concatenated so a large input that exceeds
// MaxRecordPlain still survives. Anything else (TypeFrame in a
// request) is rejected.
func (s *Server) readEnvelope1(r *http.Request) ([]byte, *envelopeCtx, error) {
	h, err := aespkg.ParseHeaders(r)
	if err != nil {
		return nil, nil, &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: err.Error()}
	}
	if err := s.cfg.AESReplay.CheckTimestamp(h.TimestampMillis); err != nil {
		return nil, nil, &aesError{Code: "ClockDrift", Status: http.StatusUnauthorized, Detail: err.Error()}
	}
	key, ok := s.cfg.Tokens.GetAESSecret(h.KeyID)
	if !ok {
		return nil, nil, &aesError{Code: "UnknownKeyId", Status: http.StatusUnauthorized, Detail: "no key for KeyId"}
	}
	if err := s.cfg.AESReplay.CheckAndRecord(h.KeyID, h.StreamIDHex); err != nil {
		return nil, nil, &aesError{Code: "ReplayedNonce", Status: http.StatusConflict, Detail: err.Error()}
	}

	src := aespkg.NewSource(r.Body, key, h, aespkg.DirectionRequest, envelopeRoute(r))
	var buf bytes.Buffer
	for i := 0; ; i++ {
		if i >= MaxAESRequestRecords {
			return nil, nil, &aesError{Code: "BadEnvelope", Status: http.StatusRequestEntityTooLarge, Detail: "too many records"}
		}
		t, payload, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, classifyOpenErr(err)
		}
		switch t {
		case aespkg.TypeJSON:
			buf.Write(payload)
		case aespkg.TypeHeartbeat, aespkg.TypeStreamEnd:
			// ignore
		default:
			return nil, nil, &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: "unexpected inner type in request"}
		}
	}
	return buf.Bytes(), &envelopeCtx{reqHdrs: h, key: key}, nil
}

// classifyOpenErr maps aes-package errors to wire-protocol error codes.
func classifyOpenErr(err error) *aesError {
	switch {
	case errors.Is(err, aespkg.ErrBadTag):
		return &aesError{Code: "BadTag", Status: http.StatusBadRequest, Detail: "decryption failed"}
	case errors.Is(err, aespkg.ErrBadFrame), errors.Is(err, aespkg.ErrInnerLength), errors.Is(err, aespkg.ErrInnerShort):
		return &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: err.Error()}
	case errors.Is(err, aespkg.ErrTooLarge):
		return &aesError{Code: "BadEnvelope", Status: http.StatusRequestEntityTooLarge, Detail: err.Error()}
	default:
		return &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: err.Error()}
	}
}

// writeEnvelopeJSON encrypts v as JSON and writes a one-record
// response body. status applies to the outer HTTP status; the inner
// payload is the JSON bytes.
func (s *Server) writeEnvelopeJSON(w http.ResponseWriter, r *http.Request, ec *envelopeCtx, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	respH, err := newServerHeaders(ec.reqHdrs.KeyID)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	if len(body) > aespkg.MaxRecordPlain-aespkg.InnerHeaderLen {
		// Split across multiple TypeJSON records so the client side
		// concatenates them. Most responses fit in one record.
		s.writeEnvelopeChunkedJSON(w, r, ec, respH, status, body)
		return
	}
	applyResponseHeaders(w, respH, status)
	if err := aespkg.SealOneShot(w, ec.key, respH, aespkg.DirectionResponse, envelopeRoute(r), aespkg.TypeJSON, body); err != nil {
		// Headers already flushed; can't change status. Best effort.
		return
	}
}

// writeEnvelopeChunkedJSON splits a large JSON body into multiple
// TypeJSON records of size ≤ MaxRecordPlain-InnerHeaderLen, terminated
// by the sentinel. The client concatenates payloads to recover the
// JSON.
func (s *Server) writeEnvelopeChunkedJSON(w http.ResponseWriter, r *http.Request, ec *envelopeCtx, respH aespkg.Headers, status int, body []byte) {
	applyResponseHeaders(w, respH, status)
	sink := aespkg.NewSink(w, ec.key, respH, aespkg.DirectionResponse, envelopeRoute(r))
	chunk := aespkg.MaxRecordPlain - aespkg.InnerHeaderLen
	for off := 0; off < len(body); off += chunk {
		end := off + chunk
		if end > len(body) {
			end = len(body)
		}
		if err := sink.Write(aespkg.TypeJSON, body[off:end]); err != nil {
			return
		}
	}
	_ = sink.Close()
}

// newServerHeaders mints a fresh Headers value for the response side.
// Distinct streamID + timestamp so request/response cryptographic
// material never overlaps.
func newServerHeaders(keyID string) (aespkg.Headers, error) {
	return aespkg.NewHeaders(keyID, func(b []byte) error {
		_, err := rand.Read(b)
		return err
	})
}

// applyResponseHeaders writes the envelope metadata onto w. Called
// once, before the first record byte.
func applyResponseHeaders(w http.ResponseWriter, respH aespkg.Headers, status int) {
	w.Header().Set(aespkg.HeaderEnvelope, respH.Envelope)
	w.Header().Set(aespkg.HeaderStreamID, respH.StreamIDHex)
	w.Header().Set(aespkg.HeaderTimestamp, strconv.FormatInt(respH.TimestampMillis, 10))
	w.Header().Set("Content-Type", aespkg.ContentType)
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

type aesError struct {
	Code   string
	Status int
	Detail string
}

func (e *aesError) Error() string { return e.Code + ": " + e.Detail }

func writeAESError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"error": code, "detail": detail})
	_, _ = w.Write(body)
}

// -------- AES route handlers ------------------------------------------------

// aesChat mirrors GET /api/sessions/:id/chat over the AES envelope.
// Request body (encrypted, TypeJSON): `{ "since": <seq> }` (optional;
// missing/zero returns the whole list).
type aesChatRequest struct {
	Since uint64 `json:"since,omitempty"`
}

func (s *Server) aesChat(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		var ae *aesError
		if errors.As(err, &ae) {
			writeAESError(w, ae.Status, ae.Code, ae.Detail)
			return
		}
		writeAESError(w, http.StatusBadRequest, "BadEnvelope", err.Error())
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	var req aesChatRequest
	if len(plaintext) > 0 {
		_ = json.Unmarshal(plaintext, &req)
	}
	msgs := aggregateChat(sess.Snapshot())
	if req.Since > 0 {
		msgs = filterSince(msgs, req.Since)
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{
		"session":  sess.ID,
		"last_seq": sess.LastSeq(),
		"messages": msgs,
	})
}

// aesInput mirrors POST /api/sessions/:id/input over the AES envelope.
func (s *Server) aesInput(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		var ae *aesError
		if errors.As(err, &ae) {
			writeAESError(w, ae.Status, ae.Code, ae.Detail)
			return
		}
		writeAESError(w, http.StatusBadRequest, "BadEnvelope", err.Error())
		return
	}

	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}

	var req inputRequest
	if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
		return
	}
	if req.Data == "" {
		s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "empty data"})
		return
	}
	if _, werr := sess.Write([]byte(req.Data)); werr != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": werr.Error()})
		return
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{"bytes": len(req.Data)})
}

// aesEventsStream is the streaming events endpoint. Request body
// (encrypted, TypeJSON):
//
//	{
//	  "from":         <uint64>,   // last seq the device has rendered
//	  "kinds":        ["text.delta","status","stop","usage"],   // optional filter
//	  "max_records":  <int>,      // stop after N frame records (0 = unlimited)
//	  "wait_ms":      <int>,      // overall deadline 0..600_000
//	  "idle_hb_ms":   <int>       // heartbeat cadence during idle (default 5000)
//	}
//
// Response: chunked record stream. Each TypeFrame record carries a
// JSON-encoded stream.Frame. Heartbeats are interleaved every
// idle_hb_ms. The stream ends with the terminator when wait_ms
// elapses, max_records is hit, the session closes, or the request
// context cancels.
type streamRequest struct {
	From       uint64   `json:"from"`
	Kinds      []string `json:"kinds,omitempty"`
	MaxRecords int      `json:"max_records,omitempty"`
	WaitMs     int      `json:"wait_ms,omitempty"`
	IdleHbMs   int      `json:"idle_hb_ms,omitempty"`
}

func (s *Server) aesEventsStream(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		var ae *aesError
		if errors.As(err, &ae) {
			writeAESError(w, ae.Status, ae.Code, ae.Detail)
			return
		}
		writeAESError(w, http.StatusBadRequest, "BadEnvelope", err.Error())
		return
	}

	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}

	var req streamRequest
	if len(plaintext) > 0 {
		if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
			s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
			return
		}
	}
	// Clamp inputs.
	if req.WaitMs <= 0 {
		req.WaitMs = 30_000
	}
	if req.WaitMs > 600_000 {
		req.WaitMs = 600_000
	}
	if req.IdleHbMs <= 0 {
		req.IdleHbMs = 5_000
	}
	if req.IdleHbMs < 1_000 {
		req.IdleHbMs = 1_000
	}
	if req.MaxRecords < 0 {
		req.MaxRecords = 0
	}

	// Wants holds the allowed Kind set. Empty filter = pass everything.
	wants := map[string]bool{}
	for _, k := range req.Kinds {
		wants[k] = true
	}
	passKind := func(k string) bool {
		if len(wants) == 0 {
			return true
		}
		return wants[k]
	}

	// Prepare response: headers + Sink.
	respH, err := newServerHeaders(ec.reqHdrs.KeyID)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	applyResponseHeaders(w, respH, http.StatusOK)
	sink := aespkg.NewSink(w, ec.key, respH, aespkg.DirectionResponse, envelopeRoute(r))
	defer sink.Close()

	// Replay any buffered frames already > From before subscribing for
	// new ones. Match the long-poll semantics of the old endpoint.
	for _, f := range sess.Snapshot() {
		if f.Seq <= req.From {
			continue
		}
		if !passKind(f.Kind) {
			continue
		}
		if !writeFrame(sink, f) {
			return
		}
		if req.MaxRecords > 0 && int(sink.Count()) >= req.MaxRecords {
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.WaitMs)*time.Millisecond)
	defer cancel()

	sub := sess.Bus().Subscribe(ctx, sess.LastSeq(), 256)
	defer sub.Cancel()

	hb := time.NewTicker(time.Duration(req.IdleHbMs) * time.Millisecond)
	defer hb.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if err := sink.Heartbeat(); err != nil {
				return
			}
		case f, ok := <-sub.Frames():
			if !ok {
				_ = sink.Write(aespkg.TypeStreamEnd, []byte(`{"reason":"session_closed"}`))
				return
			}
			if !passKind(f.Kind) {
				continue
			}
			if !writeFrame(sink, f) {
				return
			}
			if req.MaxRecords > 0 && int(sink.Count()) >= req.MaxRecords {
				return
			}
		}
	}
}

// writeFrame serializes a stream.Frame as a TypeFrame record. Returns
// false on write failure (the caller should hang up).
func writeFrame(sink *aespkg.Sink, f stream.Frame) bool {
	b, err := json.Marshal(f)
	if err != nil {
		return false
	}
	if len(b) > aespkg.MaxRecordPlain-aespkg.InnerHeaderLen {
		// Truncate oversized frame data field to fit, preserving the
		// outer envelope. Embedded clients only care about delta text
		// up to ~1 KiB anyway.
		truncated := truncateFrameForWire(f, aespkg.MaxRecordPlain-aespkg.InnerHeaderLen-256)
		b, _ = json.Marshal(truncated)
	}
	return sink.Write(aespkg.TypeFrame, b) == nil
}

// truncateFrameForWire returns a copy of f whose Data JSON has been
// shortened to fit a single record. Used when a single CC frame
// happens to be huge (rare; long tool outputs hit this).
func truncateFrameForWire(f stream.Frame, maxData int) stream.Frame {
	if len(f.Data) <= maxData {
		return f
	}
	// Replace the data field with an annotated truncation marker.
	truncated, _ := json.Marshal(map[string]any{
		"truncated":      true,
		"original_bytes": len(f.Data),
		"preview":        string(f.Data[:maxData]),
	})
	return stream.Frame{
		Session: f.Session,
		Seq:     f.Seq,
		TS:      f.TS,
		Kind:    f.Kind,
		Data:    truncated,
	}
}

// -------- legacy helper used by other tests -------------------------------

// filterAfter returns the first n frames in fs whose Seq > from. Kept
// for compatibility with non-AES tests that share the helper.
func filterAfter(fs []stream.Frame, from uint64, n int) []stream.Frame {
	out := make([]stream.Frame, 0, n)
	for _, f := range fs {
		if f.Seq > from {
			out = append(out, f)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// -------- test seam --------------------------------------------------------

// envelopeCtxFor returns a context the test client can use to seal a
// fake response without going through readEnvelope1. Exported via the
// test file's helpers only; internal to this package otherwise.
var _ = hex.EncodeToString // keep import alive if future helpers need hex
