// Package providers persists third-party Claude / Anthropic-compatible
// endpoint configurations (ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY + model)
// and lets the Web UI probe them for reachability before saving.
//
// Storage is a single JSON file. Every mutation (Add, Replace, Delete)
// performs an atomic write (tmp + rename) so a crash mid-update never
// leaves a torn file on disk. "Replace" deletes the prior record and
// writes the new one in one transaction.
package providers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Provider is one configured upstream Claude-compatible endpoint.
type Provider struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	APIHost    string    `json:"api_host"`
	Model      string    `json:"model,omitempty"`
	APIKey     string    `json:"api_key,omitempty"` // empty in the Public() shape
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// Public returns the redacted view safe for List endpoints.
func (p Provider) Public() Provider {
	out := p
	out.APIKey = ""
	if len(p.APIKey) > 8 {
		// expose a short fingerprint so the UI can hint "ends in ...abcd"
		out.APIKey = "…" + p.APIKey[len(p.APIKey)-4:]
	}
	return out
}

// Store is the persistent provider registry.
type Store struct {
	path string

	mu    sync.RWMutex
	items map[string]Provider // by ID
}

// NewStore opens or creates a provider file at `path`.
func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("providers.NewStore: path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: path, items: map[string]Provider{}}
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
	var arr []Provider
	if err := json.Unmarshal(b, &arr); err != nil {
		return fmt.Errorf("providers.load: %w", err)
	}
	for _, p := range arr {
		s.items[p.ID] = p
	}
	return nil
}

func (s *Store) save() error {
	arr := make([]Provider, 0, len(s.items))
	for _, p := range s.items {
		arr = append(arr, p)
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].CreatedAt.Before(arr[j].CreatedAt) })
	b, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns the redacted public view of all providers.
func (s *Store) List() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Provider, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns the FULL provider (including api_key) by id.
func (s *Store) Get(id string) (Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[id]
	return p, ok
}

// PublicGet returns the redacted provider by id.
func (s *Store) PublicGet(id string) (Provider, bool) {
	p, ok := s.Get(id)
	if !ok {
		return Provider{}, false
	}
	return p.Public(), true
}

// Add creates a new provider with a fresh id.
func (s *Store) Add(label, host, key, model string) (Provider, error) {
	if err := validate(label, host, key); err != nil {
		return Provider{}, err
	}
	id, err := randomID()
	if err != nil {
		return Provider{}, err
	}
	now := time.Now().UTC()
	p := Provider{
		ID:        id,
		Label:     label,
		APIHost:   normalizeHost(host),
		APIKey:    key,
		Model:     model,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	s.items[id] = p
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return Provider{}, err
	}
	return p, nil
}

// Replace overwrites the entire entry for `id` in one atomic write. The
// previous secret is deleted from disk (and from memory) before the new
// one lands — the prior record is GONE after this call returns.
func (s *Store) Replace(id, label, host, key, model string) (Provider, error) {
	if err := validate(label, host, key); err != nil {
		return Provider{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, ok := s.items[id]
	if !ok {
		return Provider{}, ErrNotFound
	}
	// Atomic delete-then-write: clear in-memory entry, then assemble a
	// fresh one. save() does tmp+rename so a crash leaves either the
	// pre-update or post-update file but never a half-written one.
	delete(s.items, id)
	now := time.Now().UTC()
	p := Provider{
		ID:        id,
		Label:     label,
		APIHost:   normalizeHost(host),
		APIKey:    key,
		Model:     model,
		CreatedAt: prior.CreatedAt,
		UpdatedAt: now,
	}
	s.items[id] = p
	if err := s.save(); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// Delete removes a provider by id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return s.save()
}

// MarkUsed bumps last_used_at; called when a session picks this provider.
func (s *Store) MarkUsed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	if !ok {
		return
	}
	p.LastUsedAt = time.Now().UTC()
	s.items[id] = p
	_ = s.save() // best-effort, do not fail the session on this
}

// --- helpers --------------------------------------------------------------

var (
	ErrNotFound       = errors.New("providers: not found")
	ErrInvalidLabel   = errors.New("providers: label is required")
	ErrInvalidHost    = errors.New("providers: api_host must be http(s)://…")
	ErrInvalidAPIKey  = errors.New("providers: api_key is required")
)

func validate(label, host, key string) error {
	if label == "" {
		return ErrInvalidLabel
	}
	if !looksLikeURL(host) {
		return ErrInvalidHost
	}
	if key == "" {
		return ErrInvalidAPIKey
	}
	return nil
}

func looksLikeURL(s string) bool {
	if len(s) < 8 {
		return false
	}
	if s[:7] == "http://" || s[:8] == "https://" {
		return true
	}
	return false
}

func normalizeHost(s string) string {
	// Strip trailing slash so callers can compose `${host}/v1/messages`.
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func randomID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "p_" + hex.EncodeToString(b), nil
}
