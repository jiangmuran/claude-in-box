package server

import (
	"net/http"
	"strconv"
)

type exposePortReq struct {
	InternalPort int    `json:"internal_port"`
	InternalHost string `json:"internal_host,omitempty"`
}

func (s *Server) listPorts(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Ports == nil {
		writeErr(w, http.StatusServiceUnavailable, "ports not configured (set CIB_PORT_RANGE)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":    s.cfg.Ports.Range,
		"mappings": s.cfg.Ports.List(),
	})
}

func (s *Server) exposePort(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Ports == nil {
		writeErr(w, http.StatusServiceUnavailable, "ports not configured (set CIB_PORT_RANGE)")
		return
	}
	var req exposePortReq
	if !readJSON(w, r, &req) {
		return
	}
	mp, err := s.cfg.Ports.Expose(req.InternalPort, req.InternalHost)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mp)
}

func (s *Server) unexposePort(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Ports == nil {
		writeErr(w, http.StatusServiceUnavailable, "ports not configured")
		return
	}
	hp, err := strconv.Atoi(r.PathValue("host_port"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "host_port not an int")
		return
	}
	if err := s.cfg.Ports.Unexpose(hp); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

