package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	aespkg "github.com/jiangmuran/claude-in-box/internal/aes"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// MaxAESBody caps the encrypted body size to prevent abuse. CC tool calls
// can carry large outputs; 8 MiB is generous for an embedded client.
const MaxAESBody = 8 << 20

// -------- cleartext bootstrap endpoints -------------------------------------

func (s *Server) aesTime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"server_now":   time.Now().UTC().UnixMilli(),
		"tolerance_ms": int64(aespkg.DefaultReplayWindow / time.Millisecond / 2),
		"envelope":     aespkg.EnvelopeVersion,
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
		"id":             id,
		"algorithm":      "aes-256-gcm",
		"envelope":       aespkg.EnvelopeVersion,
		"derive_subkeys": false,
	})
}

// -------- envelope plumbing -------------------------------------------------

// envelopeRoute is the canonical AAD route used by both sides. The actual
// HTTP path includes the dynamic session id; we keep the path verbatim so
// devices can build AAD without a route table.
func envelopeRoute(r *http.Request) string { return r.URL.Path }

// readEnvelope parses envelope headers, validates timestamp + replay,
// retrieves the device key, and decrypts the body. Returns plaintext + the
// envelope headers (for response signing).
func (s *Server) readEnvelope(r *http.Request) ([]byte, aespkg.Headers, []byte, error) {
	h, err := aespkg.ParseHeaders(r)
	if err != nil {
		return nil, h, nil, &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: err.Error()}
	}
	if err := s.cfg.AESReplay.CheckTimestamp(h.TimestampMillis); err != nil {
		return nil, h, nil, &aesError{Code: "ClockDrift", Status: http.StatusUnauthorized, Detail: err.Error()}
	}
	key, ok := s.cfg.Tokens.GetAESSecret(h.KeyID)
	if !ok {
		return nil, h, nil, &aesError{Code: "UnknownKeyId", Status: http.StatusUnauthorized, Detail: "no key for KeyId"}
	}
	if err := s.cfg.AESReplay.CheckAndRecord(h.KeyID, h.NonceHex); err != nil {
		return nil, h, nil, &aesError{Code: "ReplayedNonce", Status: http.StatusConflict, Detail: err.Error()}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxAESBody+1))
	if err != nil {
		return nil, h, key, &aesError{Code: "BadEnvelope", Status: http.StatusBadRequest, Detail: "read body: " + err.Error()}
	}
	if len(body) > MaxAESBody {
		return nil, h, key, &aesError{Code: "BadEnvelope", Status: http.StatusRequestEntityTooLarge, Detail: "payload too large"}
	}
	plaintext, err := aespkg.Open(key, h, r.Method, envelopeRoute(r), body)
	if err != nil {
		return nil, h, key, &aesError{Code: "BadTag", Status: http.StatusBadRequest, Detail: "decryption failed"}
	}
	return plaintext, h, key, nil
}

// writeEnvelope encrypts `out` with the same per-device key and writes the
// ciphertext as the response body, signed by a server-chosen response nonce
// and the "RESPONSE" AAD pseudo-method.
func (s *Server) writeEnvelope(w http.ResponseWriter, r *http.Request, h aespkg.Headers, key, out []byte) {
	respH, err := aespkg.NewHeaders(h.KeyID)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	ct, err := aespkg.Seal(key, respH, "RESPONSE", envelopeRoute(r), out)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	w.Header().Set(aespkg.HeaderEnvelope, respH.Envelope)
	w.Header().Set(aespkg.HeaderNonce, respH.NonceHex)
	w.Header().Set(aespkg.HeaderTimestamp, strconv.FormatInt(respH.TimestampMillis, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(ct)
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
	_, _ = w.Write([]byte(`{"error":"` + code + `","detail":` + jsonStringLiteral(detail) + `}`))
}

func jsonStringLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return string(b)
}

// -------- AES route handlers ------------------------------------------------

// aesChat mirrors GET /api/sessions/:id/chat over the AES envelope.
// Embedded-friendly slim chat shape; expects an envelope-encrypted JSON
// body `{ "since": <seq> }` (since=0 returns the whole list).
type aesChatRequest struct {
	Since uint64 `json:"since,omitempty"`
}

func (s *Server) aesChat(w http.ResponseWriter, r *http.Request) {
	plaintext, h, key, err := s.readEnvelope(r)
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
		s.aesRespJSON(w, r, h, key, http.StatusNotFound, map[string]any{"error": "no such session"})
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
	s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{
		"session":  sess.ID,
		"last_seq": sess.LastSeq(),
		"messages": msgs,
	})
}

// aesInput mirrors POST /api/sessions/:id/input over the AES envelope.
func (s *Server) aesInput(w http.ResponseWriter, r *http.Request) {
	plaintext, h, key, err := s.readEnvelope(r)
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
		s.aesRespJSON(w, r, h, key, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}

	var req inputRequest
	if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
		s.aesRespJSON(w, r, h, key, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
		return
	}
	if req.Data == "" {
		s.aesRespJSON(w, r, h, key, http.StatusBadRequest, map[string]any{"error": "empty data"})
		return
	}
	if _, werr := sess.Write([]byte(req.Data)); werr != nil {
		s.aesRespJSON(w, r, h, key, http.StatusInternalServerError, map[string]any{"error": werr.Error()})
		return
	}
	s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{"bytes": len(req.Data)})
}

// aesEventsPoll long-polls for new frames since `from`. Body shape:
//
//	{ "from": <uint64>, "max": <int>, "wait_ms": <int 0..30000> }
//
// Returns up to max frames with seq > from, or waits wait_ms for them.
type pollRequest struct {
	From   uint64 `json:"from"`
	Max    int    `json:"max"`
	WaitMs int    `json:"wait_ms"`
}

func (s *Server) aesEventsPoll(w http.ResponseWriter, r *http.Request) {
	plaintext, h, key, err := s.readEnvelope(r)
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
		s.aesRespJSON(w, r, h, key, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}

	var req pollRequest
	if len(plaintext) > 0 {
		if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
			s.aesRespJSON(w, r, h, key, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
			return
		}
	}
	if req.Max <= 0 {
		req.Max = 32
	}
	if req.Max > 256 {
		req.Max = 256
	}
	if req.WaitMs < 0 {
		req.WaitMs = 0
	}
	if req.WaitMs > 30_000 {
		req.WaitMs = 30_000
	}

	// First pass: any buffered frames?
	out := filterAfter(sess.Snapshot(), req.From, req.Max)
	if len(out) > 0 || req.WaitMs == 0 {
		s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{
			"frames":   out,
			"last_seq": sess.LastSeq(),
		})
		return
	}

	// Long-poll path: subscribe and wait.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.WaitMs)*time.Millisecond)
	defer cancel()
	sub := sess.Subscribe(ctx, req.From)
	defer sub.Cancel()

	var collected []stream.Frame
	for len(collected) < req.Max {
		select {
		case <-ctx.Done():
			s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{
				"frames":   collected,
				"last_seq": sess.LastSeq(),
			})
			return
		case f, ok := <-sub.Frames():
			if !ok {
				s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{
					"frames":   collected,
					"last_seq": sess.LastSeq(),
					"closed":   true,
				})
				return
			}
			collected = append(collected, f)
		}
	}
	s.aesRespJSON(w, r, h, key, http.StatusOK, map[string]any{
		"frames":   collected,
		"last_seq": sess.LastSeq(),
	})
}

// aesRespJSON encodes v to JSON and writes it as an encrypted response.
// status only affects the implicit 200 — actual HTTP status is always 200
// on a successful envelope; errors at the protocol layer go through
// writeAESError above.
func (s *Server) aesRespJSON(w http.ResponseWriter, r *http.Request, h aespkg.Headers, key []byte, status int, v any) {
	_ = status // reserved for future status-in-envelope semantics
	b, err := json.Marshal(v)
	if err != nil {
		writeAESError(w, http.StatusInternalServerError, "ServerError", err.Error())
		return
	}
	s.writeEnvelope(w, r, h, key, b)
}

// filterAfter returns the first n frames in fs whose Seq > from.
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

// Compile-time only; ensures we keep `hex` linked even if other call-sites
// drop it.
var _ = hex.EncodeToString
var _ = strconv.Itoa
