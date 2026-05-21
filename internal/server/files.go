package server

import (
	"errors"
	"net/http"

	"github.com/jiangmuran/claude-in-box/internal/fsapi"
)

func (s *Server) listFSRoots(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": s.cfg.Files.ListRoots()})
}

func (s *Server) fsList(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	root := r.URL.Query().Get("root")
	path := r.URL.Query().Get("path")
	if root == "" {
		root = "workspace"
	}
	entries, err := s.cfg.Files.List(root, path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root":    root,
		"path":    path,
		"entries": entries,
	})
}

func (s *Server) fsRead(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	root := r.URL.Query().Get("root")
	path := r.URL.Query().Get("path")
	if root == "" {
		root = "workspace"
	}
	data, truncated, err := s.cfg.Files.Read(root, path)
	if err != nil {
		fsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root":      root,
		"path":      path,
		"content":   string(data),
		"truncated": truncated,
		"size":      len(data),
	})
}

type fsWriteRequest struct {
	Root    string `json:"root"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) fsWrite(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	var req fsWriteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Root == "" {
		req.Root = "workspace"
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := s.cfg.Files.Write(req.Root, req.Path, []byte(req.Content)); err != nil {
		fsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type fsMkdirRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

func (s *Server) fsMkdir(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	var req fsMkdirRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Root == "" {
		req.Root = "workspace"
	}
	if err := s.cfg.Files.Mkdir(req.Root, req.Path); err != nil {
		fsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fsDelete(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Files == nil {
		writeErr(w, http.StatusServiceUnavailable, "files not configured")
		return
	}
	root := r.URL.Query().Get("root")
	path := r.URL.Query().Get("path")
	if root == "" {
		root = "workspace"
	}
	if err := s.cfg.Files.Delete(root, path); err != nil {
		fsErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func fsErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fsapi.ErrBadRoot):
		writeErr(w, http.StatusBadRequest, "unknown root")
	case errors.Is(err, fsapi.ErrEscape):
		writeErr(w, http.StatusForbidden, "path escapes root")
	case errors.Is(err, fsapi.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, fsapi.ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "payload too large")
	case errors.Is(err, fsapi.ErrBadPath):
		writeErr(w, http.StatusBadRequest, "invalid path")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}
