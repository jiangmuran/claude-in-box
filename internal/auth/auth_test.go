package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return s
}

func TestFileStore_SetMasterAndLookup(t *testing.T) {
	s := newStore(t)
	if err := s.SetMaster("master-secret-xyz"); err != nil {
		t.Fatalf("SetMaster: %v", err)
	}
	tok, ok := s.Lookup("master-secret-xyz")
	if !ok {
		t.Fatal("master lookup failed")
	}
	if tok.ID != MasterTokenID {
		t.Fatalf("id = %q want %q", tok.ID, MasterTokenID)
	}
	if !hasScope(tok.Scopes, ScopeSessionsWrite) {
		t.Fatalf("master scopes = %v missing %q", tok.Scopes, ScopeSessionsWrite)
	}
	if _, ok := s.Lookup("nope"); ok {
		t.Fatal("bad token must not match")
	}
}

func TestFileStore_SetMasterReplacesPrevious(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("first")
	_ = s.SetMaster("second")
	if _, ok := s.Lookup("first"); ok {
		t.Fatal("old master still usable")
	}
	if _, ok := s.Lookup("second"); !ok {
		t.Fatal("new master not registered")
	}
}

func TestFileStore_MintAndUseDeviceToken(t *testing.T) {
	s := newStore(t)
	res, err := s.Mint("phone", []string{ScopeSessionsRead}, 0)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if res.Plaintext == "" || len(res.Plaintext) != 64 {
		t.Fatalf("bad plaintext: %q", res.Plaintext)
	}
	if res.Token.ID == "" || res.Token.Label != "phone" {
		t.Fatalf("token meta = %+v", res.Token)
	}
	tok, ok := s.Lookup(res.Plaintext)
	if !ok || tok.ID != res.Token.ID {
		t.Fatalf("lookup roundtrip failed: %+v ok=%v", tok, ok)
	}
}

func TestFileStore_ExpiredTokenIsRejected(t *testing.T) {
	s := newStore(t)
	res, err := s.Mint("temp", []string{"*"}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, ok := s.Lookup(res.Plaintext); ok {
		t.Fatal("expired token must not validate")
	}
}

func TestFileStore_RevokeRemoves(t *testing.T) {
	s := newStore(t)
	res, _ := s.Mint("temp", []string{"*"}, 0)
	if err := s.Revoke(res.Token.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := s.Lookup(res.Plaintext); ok {
		t.Fatal("revoked token must not validate")
	}
}

func TestFileStore_CannotRevokeMaster(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("m")
	if err := s.Revoke(MasterTokenID); err == nil {
		t.Fatal("expected error when revoking master")
	}
}

func TestFileStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	s1, _ := NewFileStore(path)
	_ = s1.SetMaster("m")
	res, _ := s1.Mint("dev", []string{ScopeSessionsRead}, 0)

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := s2.Lookup("m"); !ok {
		t.Fatal("master not persisted")
	}
	if _, ok := s2.Lookup(res.Plaintext); !ok {
		t.Fatal("device token not persisted")
	}
}

func TestRequire_HappyPath(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("ok")

	called := false
	h := Require(s, ScopeSessionsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := FromContext(r.Context())
		if !ok {
			t.Fatal("no token in context")
		}
		if tok.ID != MasterTokenID {
			t.Fatalf("ctx token id = %q", tok.ID)
		}
		called = true
		w.WriteHeader(204)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer ok")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Fatalf("status = %d want 204", w.Code)
	}
	if !called {
		t.Fatal("inner handler not called")
	}
}

func TestRequire_RejectsMissingToken(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("ok")
	h := Require(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status = %d want 401", w.Code)
	}
}

func TestRequire_RejectsBadToken(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("ok")
	h := Require(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer no")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status = %d want 401", w.Code)
	}
}

func TestRequire_RejectsInsufficientScope(t *testing.T) {
	s := newStore(t)
	res, _ := s.Mint("read-only", []string{ScopeSessionsRead}, 0)

	h := Require(s, ScopeSessionsWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+res.Plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("status = %d want 403; body=%s", w.Code, w.Body)
	}
}

func TestRequire_AcceptsWebSocketSubprotocol(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("ok")

	h := Require(s, ScopeSessionsRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "json, bearer.ok")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status = %d want 204; body=%s", w.Code, w.Body)
	}
}

func TestRequire_AcceptsQueryParamFallback(t *testing.T) {
	s := newStore(t)
	_ = s.SetMaster("ok")
	h := Require(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("GET", "/x?token=ok", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status = %d want 204; body=%s", w.Code, w.Body)
	}
}

func TestSelectedSubprotocol(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"bearer.xxx", ""},
		{"json, bearer.xxx", "json"},
		{"bearer.xxx, msgpack", "msgpack"},
	}
	for _, tc := range tests {
		got := SelectedSubprotocol(tc.in)
		if got != tc.want {
			t.Fatalf("SelectedSubprotocol(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestJSONString_EscapesQuotesAndControls(t *testing.T) {
	got := jsonString("a\"b\nc")
	if !strings.Contains(got, `\"`) || !strings.Contains(got, `\n`) {
		t.Fatalf("escape failure: %s", got)
	}
}
