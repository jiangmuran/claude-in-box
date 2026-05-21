package auth

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is unexported to avoid collisions with other packages' context keys.
type ctxKey int

const (
	ctxTokenKey ctxKey = iota
)

// FromContext returns the Token attached to this request, if any.
func FromContext(ctx context.Context) (Token, bool) {
	t, ok := ctx.Value(ctxTokenKey).(Token)
	return t, ok
}

// Require returns middleware that:
//   - extracts a bearer token from the Authorization header or the
//     `Sec-WebSocket-Protocol: bearer.<token>` subprotocol header,
//   - validates against the store,
//   - checks that the token has every requested scope.
//
// On success it adds the Token to the request context. On failure it writes
// a 401 (missing/bad token) or 403 (scope insufficient) JSON response.
func Require(store Store, requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pt := extractToken(r)
			if pt == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing token")
				return
			}
			tok, ok := store.Lookup(pt)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			if len(requiredScopes) > 0 && !hasAllScopes(tok.Scopes, requiredScopes) {
				writeAuthError(w, http.StatusForbidden, "insufficient scope")
				return
			}
			ctx := context.WithValue(r.Context(), ctxTokenKey, tok)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken pulls the bearer token out of an incoming request. It prefers
// the Authorization header; falls back to Sec-WebSocket-Protocol so WS
// upgrades can authenticate without exposing tokens in URLs.
func extractToken(r *http.Request) string {
	// HTTP Authorization header.
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
		if strings.HasPrefix(h, "bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(h, "bearer "))
		}
	}

	// WebSocket subprotocol header: a comma-separated list of protocols;
	// look for one starting with `bearer.<token>`.
	if h := r.Header.Get("Sec-WebSocket-Protocol"); h != "" {
		for _, p := range strings.Split(h, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "bearer.") {
				return strings.TrimPrefix(p, "bearer.")
			}
		}
	}

	// Last resort: ?token= query param. Less safe (URL logging) but useful
	// for clients that cannot set headers (e.g. some embedded HTTP libs).
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	return ""
}

// SelectedSubprotocol returns the WebSocket subprotocol the server should
// echo back in its handshake response. If the client offered `bearer.<token>`
// alongside other names, we strip the secret and return the most-preferred
// non-bearer protocol — or empty if none, which is also fine.
func SelectedSubprotocol(offered string) string {
	for _, p := range strings.Split(offered, ",") {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "bearer.") {
			continue
		}
		return p
	}
	return ""
}

func writeAuthError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `}`))
}

func jsonString(s string) string {
	// Tiny inline encoder: avoid importing encoding/json just for this.
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[(r>>4)&0xF])
				b.WriteByte(hex[r&0xF])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
