package providers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tmpStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "providers.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestAddPublicHidesKey(t *testing.T) {
	s := tmpStore(t)
	p, err := s.Add("Anthropic", "https://api.anthropic.com", "sk-ant-secret-tail-XYZ8", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if p.APIKey != "sk-ant-secret-tail-XYZ8" {
		t.Fatalf("Add must return real key, got %q", p.APIKey)
	}
	pub := s.List()[0]
	if pub.APIKey == "sk-ant-secret-tail-XYZ8" {
		t.Fatalf("public must redact key, got %q", pub.APIKey)
	}
	if !strings.HasSuffix(pub.APIKey, "XYZ8") {
		t.Fatalf("public should expose only the tail, got %q", pub.APIKey)
	}
}

func TestAddRejectsInvalid(t *testing.T) {
	s := tmpStore(t)
	if _, err := s.Add("", "https://x", "k", ""); !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("empty label = %v", err)
	}
	if _, err := s.Add("L", "ftp://x", "k", ""); !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("bad host = %v", err)
	}
	if _, err := s.Add("L", "https://x", "", ""); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("empty key = %v", err)
	}
}

func TestReplaceDeletesPriorRecord(t *testing.T) {
	s := tmpStore(t)
	p, _ := s.Add("Initial", "https://h1.example.com", "k-old", "model-a")

	got, err := s.Replace(p.ID, "Updated", "https://h2.example.com/", "k-new", "model-b")
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got.Label != "Updated" || got.APIKey != "k-new" || got.APIHost != "https://h2.example.com" || got.Model != "model-b" {
		t.Fatalf("replaced shape wrong: %+v", got)
	}
	if got.CreatedAt != p.CreatedAt {
		t.Fatal("Replace must preserve CreatedAt")
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatal("UpdatedAt must advance on Replace")
	}
	// Old key must NOT be on disk anywhere.
	raw, _ := os.ReadFile(s.path)
	if strings.Contains(string(raw), "k-old") {
		t.Fatalf("disk still contains old key:\n%s", raw)
	}
	if !strings.Contains(string(raw), "k-new") {
		t.Fatalf("disk missing new key:\n%s", raw)
	}
}

func TestDeleteRemoves(t *testing.T) {
	s := tmpStore(t)
	p, _ := s.Add("Tmp", "https://h", "k", "")
	if err := s.Delete(p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(p.ID); ok {
		t.Fatal("Get after Delete should be miss")
	}
	if err := s.Delete(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double-delete err = %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")

	s1, _ := NewStore(path)
	p, _ := s1.Add("Persist", "https://h", "k-persist", "")
	_ = p

	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("len after reopen = %d", len(s2.List()))
	}
	if got, ok := s2.Get(p.ID); !ok || got.APIKey != "k-persist" {
		t.Fatalf("reopen lost key, got %+v ok=%v", got, ok)
	}
}

func TestNormalizeHostStripsTrailingSlash(t *testing.T) {
	s := tmpStore(t)
	p, _ := s.Add("L", "https://example.com////", "k", "")
	if p.APIHost != "https://example.com" {
		t.Fatalf("APIHost = %q", p.APIHost)
	}
}

func TestMarkUsedBumpsLastUsedAt(t *testing.T) {
	s := tmpStore(t)
	p, _ := s.Add("L", "https://h", "k", "")
	s.MarkUsed(p.ID)
	got, _ := s.Get(p.ID)
	if got.LastUsedAt.IsZero() {
		t.Fatal("LastUsedAt should be set")
	}
}
