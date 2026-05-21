package server

import (
	"net/http"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/auth"
)

type mintTokenRequest struct {
	Label    string   `json:"label"`
	Scopes   []string `json:"scopes"`
	TTLHours int      `json:"ttl_hours,omitempty"`
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens := s.cfg.Tokens.List()
	out := make([]auth.PublicToken, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, t.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) mintToken(w http.ResponseWriter, r *http.Request) {
	var req mintTokenRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Label == "" {
		writeErr(w, http.StatusBadRequest, "label is required")
		return
	}
	if len(req.Scopes) == 0 {
		writeErr(w, http.StatusBadRequest, "scopes is required (use [\"*\"] for unrestricted)")
		return
	}
	ttl := time.Duration(req.TTLHours) * time.Hour
	res, err := s.cfg.Tokens.Mint(req.Label, req.Scopes, ttl)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Plaintext is returned ONCE here, never again.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     res.Token.Public(),
		"plaintext": res.Plaintext,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == auth.MasterTokenID {
		writeErr(w, http.StatusForbidden, "cannot revoke the master token")
		return
	}
	if err := s.cfg.Tokens.Revoke(id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
