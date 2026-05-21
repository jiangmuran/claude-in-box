package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// MasterTokenID is the id reserved for the master token.
	MasterTokenID = "master"

	tokenIDBytes = 8  // 16 hex chars
	tokenBytes   = 32 // 64 hex chars
)

// Token is a single bearer credential. Plaintext is only known at mint time.
type Token struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Hash is hex(sha256(plaintext bearer)). Never returns the bearer.
	Hash string `json:"hash"`
	// AESSecretHex is the optional 32-byte AES-256 master secret for the
	// device (docs/AES-TRANSPORT.md). Hex-encoded, stored at rest as-is
	// because it must be retrievable plaintext for AES-GCM decrypt. Empty
	// when this token was minted with WithAES=false.
	AESSecretHex string `json:"aes_secret_hex,omitempty"`
}

// IsExpired returns true if ExpiresAt is set and in the past.
func (t Token) IsExpired() bool {
	return t.ExpiresAt != nil && time.Now().UTC().After(*t.ExpiresAt)
}

// Public is the redacted shape returned by GET /api/tokens etc.
func (t Token) Public() PublicToken {
	return PublicToken{
		ID:        t.ID,
		Label:     t.Label,
		Scopes:    t.Scopes,
		CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt,
	}
}

// PublicToken is the safe-to-return shape (no hash, no plaintext).
type PublicToken struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// MintResult is what NewToken returns at mint time — Token + the plaintext
// bearer + (optional) the AES master secret.
type MintResult struct {
	Token        Token  `json:"token"`
	Plaintext    string `json:"plaintext"`               // returned to caller exactly once
	AESSecretHex string `json:"aes_secret_hex,omitempty"` // only present if WithAES was true
}

// MintOptions controls Mint behaviour.
type MintOptions struct {
	// WithAES asks Mint to also generate a 32-byte AES master secret for
	// the AES envelope transport. Defaults to true; pass false on Store's
	// Mint variants that take MintOptions.
	WithAES bool
}

// Store persists tokens. The default implementation is FileStore.
type Store interface {
	// Lookup returns the Token associated with `plaintext` (and `true`) or a
	// zero Token and `false`. Lookups must be constant time wrt incoming
	// strings of equal length.
	Lookup(plaintext string) (Token, bool)

	// Mint creates and persists a new token; an AES secret is generated
	// alongside the bearer by default.
	Mint(label string, scopes []string, ttl time.Duration) (MintResult, error)

	// SetMaster registers the master token from the boot env var. Replaces
	// any existing master entry.
	SetMaster(plaintext string) error

	// List returns all tokens (master + device).
	List() []Token

	// Get returns a token by id.
	Get(id string) (Token, bool)

	// GetAESSecret returns the raw 32-byte AES master secret for the
	// device whose token id is keyID. Returns (nil, false) if the token
	// does not exist or has no AES secret on file.
	GetAESSecret(keyID string) ([]byte, bool)

	// Revoke deletes a token by id. Cannot revoke the master.
	Revoke(id string) error
}

// FileStore is a thread-safe Store persisted as JSON on disk.
type FileStore struct {
	path string
	mu   sync.RWMutex
	// tokens are keyed by hex hash for O(1) lookup.
	tokens map[string]Token
}

// NewFileStore loads (or creates) a token database at `path`.
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("auth.NewFileStore: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("auth: mkdir %s: %w", filepath.Dir(path), err)
	}
	s := &FileStore{path: path, tokens: map[string]Token{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	var list []Token
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("auth: parse %s: %w", s.path, err)
	}
	for _, t := range list {
		s.tokens[t.Hash] = t
	}
	return nil
}

func (s *FileStore) save() error {
	list := make([]Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		list = append(list, t)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// hashPlaintext returns hex(sha256(plaintext)).
func hashPlaintext(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Lookup implements Store. Constant-time comparison happens at the hash
// level (sha256 over the plaintext is constant time wrt incoming length).
func (s *FileStore) Lookup(plaintext string) (Token, bool) {
	if plaintext == "" {
		return Token{}, false
	}
	h := hashPlaintext(plaintext)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, t := range s.tokens {
		// subtle.ConstantTimeCompare is constant time only when both inputs
		// have the same length; hex hashes always do.
		if subtle.ConstantTimeCompare([]byte(h), []byte(k)) == 1 {
			if t.IsExpired() {
				return Token{}, false
			}
			return t, true
		}
	}
	return Token{}, false
}

// Mint implements Store. An AES master secret is generated alongside the
// bearer; callers who do not want one can simply ignore AESSecretHex on
// MintResult and on Token (it remains harmless on disk until the token is
// revoked).
func (s *FileStore) Mint(label string, scopes []string, ttl time.Duration) (MintResult, error) {
	pt, err := randomHex(tokenBytes)
	if err != nil {
		return MintResult{}, err
	}
	id, err := randomHex(tokenIDBytes)
	if err != nil {
		return MintResult{}, err
	}
	aesHex, err := randomHex(32)
	if err != nil {
		return MintResult{}, err
	}
	t := Token{
		ID:           id,
		Label:        label,
		Scopes:       append([]string{}, scopes...),
		CreatedAt:    time.Now().UTC(),
		Hash:         hashPlaintext(pt),
		AESSecretHex: aesHex,
	}
	if ttl > 0 {
		exp := t.CreatedAt.Add(ttl)
		t.ExpiresAt = &exp
	}

	s.mu.Lock()
	s.tokens[t.Hash] = t
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return MintResult{}, err
	}
	return MintResult{Token: t, Plaintext: pt, AESSecretHex: aesHex}, nil
}

// GetAESSecret implements Store.
func (s *FileStore) GetAESSecret(keyID string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tokens {
		if t.ID == keyID && t.AESSecretHex != "" {
			b, err := hex.DecodeString(t.AESSecretHex)
			if err != nil {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}

// SetMaster implements Store.
func (s *FileStore) SetMaster(plaintext string) error {
	if plaintext == "" {
		return errors.New("auth.SetMaster: empty plaintext")
	}
	t := Token{
		ID:        MasterTokenID,
		Label:     "master",
		Scopes:    AllScopes,
		CreatedAt: time.Now().UTC(),
		Hash:      hashPlaintext(plaintext),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Remove any existing master entry (the hash may have changed).
	for k, v := range s.tokens {
		if v.ID == MasterTokenID {
			delete(s.tokens, k)
		}
	}
	s.tokens[t.Hash] = t
	return s.save()
}

// List implements Store.
func (s *FileStore) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	return out
}

// Get implements Store.
func (s *FileStore) Get(id string) (Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tokens {
		if t.ID == id {
			return t, true
		}
	}
	return Token{}, false
}

// Revoke implements Store.
func (s *FileStore) Revoke(id string) error {
	if id == MasterTokenID {
		return errors.New("auth: cannot revoke master token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.tokens {
		if v.ID == id {
			delete(s.tokens, k)
			return s.save()
		}
	}
	return fmt.Errorf("auth: token %q not found", id)
}

// randomHex returns 2n hex characters from crypto/rand.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
