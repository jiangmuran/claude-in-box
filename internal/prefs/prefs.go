// Package prefs persists the user's "default" choices so the Web UI does
// not have to ask the same question every time. Today this is just the
// default auth mode (subscription / api_key) and the default provider id
// used when api_key mode is picked.
//
// Storage is one tiny JSON file. Every Set call performs an atomic
// rewrite (tmp + rename) so a crash mid-update never leaves a torn file.
package prefs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Prefs is the persisted shape. Add fields here as we learn more about
// what the UI wants to remember.
type Prefs struct {
	DefaultAuthMode  string    `json:"default_auth_mode,omitempty"`  // "subscription" | "api_key" | ""
	DefaultProviderID string   `json:"default_provider_id,omitempty"`
	DefaultModel     string    `json:"default_model,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

// Store is the persistent prefs registry.
type Store struct {
	path string

	mu sync.RWMutex
	p  Prefs
}

// NewStore opens or creates the prefs file at `path`.
func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("prefs.NewStore: path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &s.p)
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the current prefs (always safe to read, never nil).
func (s *Store) Get() Prefs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

// Set replaces the whole record atomically. Empty input wipes prefs to
// their zero state — useful for a "reset" button.
func (s *Store) Set(p Prefs) error {
	p.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p = p
	return s.save()
}

// Patch is a partial update. Only the keys actually present in `delta` are
// applied; the rest are left alone. Pass `delta.DefaultAuthMode = "-"` to
// explicitly clear a string field.
func (s *Store) Patch(delta Prefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if delta.DefaultAuthMode != "" {
		if delta.DefaultAuthMode == "-" {
			s.p.DefaultAuthMode = ""
		} else {
			s.p.DefaultAuthMode = delta.DefaultAuthMode
		}
	}
	if delta.DefaultProviderID != "" {
		if delta.DefaultProviderID == "-" {
			s.p.DefaultProviderID = ""
		} else {
			s.p.DefaultProviderID = delta.DefaultProviderID
		}
	}
	if delta.DefaultModel != "" {
		if delta.DefaultModel == "-" {
			s.p.DefaultModel = ""
		} else {
			s.p.DefaultModel = delta.DefaultModel
		}
	}
	s.p.UpdatedAt = time.Now().UTC()
	return s.save()
}
