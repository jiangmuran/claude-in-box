package prefs

import (
	"path/filepath"
	"testing"
)

func TestSetAndGetRoundtrips(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "p.json"))
	if err := s.Set(Prefs{DefaultAuthMode: "subscription", DefaultModel: "claude-opus-4-7"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := s.Get()
	if got.DefaultAuthMode != "subscription" || got.DefaultModel != "claude-opus-4-7" {
		t.Fatalf("got = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set")
	}
}

func TestPatchAppliesOnlyProvidedKeys(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "p.json"))
	_ = s.Set(Prefs{DefaultAuthMode: "api_key", DefaultProviderID: "p_initial", DefaultModel: "m1"})

	if err := s.Patch(Prefs{DefaultModel: "m2"}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	got := s.Get()
	if got.DefaultAuthMode != "api_key" {
		t.Fatalf("auth_mode untouched expected, got %q", got.DefaultAuthMode)
	}
	if got.DefaultProviderID != "p_initial" {
		t.Fatalf("provider untouched expected, got %q", got.DefaultProviderID)
	}
	if got.DefaultModel != "m2" {
		t.Fatalf("model = %q", got.DefaultModel)
	}
}

func TestPatchDashClears(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "p.json"))
	_ = s.Set(Prefs{DefaultProviderID: "p_keep"})
	_ = s.Patch(Prefs{DefaultProviderID: "-"})
	if s.Get().DefaultProviderID != "" {
		t.Fatalf("dash didn't clear: %+v", s.Get())
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")
	s1, _ := NewStore(path)
	_ = s1.Set(Prefs{DefaultAuthMode: "subscription"})

	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Get().DefaultAuthMode != "subscription" {
		t.Fatalf("lost across reopen: %+v", s2.Get())
	}
}
