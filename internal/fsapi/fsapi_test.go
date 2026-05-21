package fsapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newM(t *testing.T) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.md"), []byte("# title"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return &Manager{Roots: map[string]string{"r": root}}, root
}

func TestList_RootAndSub(t *testing.T) {
	m, _ := newM(t)
	entries, err := m.List("r", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 { // sub/, a.txt
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if !entries[0].IsDir || entries[0].Name != "sub" {
		t.Fatalf("expected sub first; got %+v", entries[0])
	}

	entries, err = m.List("r", "sub")
	if err != nil {
		t.Fatalf("List sub: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "b.md" {
		t.Fatalf("entries[sub] = %+v", entries)
	}
}

func TestRead_HappyPath(t *testing.T) {
	m, _ := newM(t)
	b, trunc, err := m.Read("r", "a.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(b) != "hello" || trunc {
		t.Fatalf("Read = %q trunc=%v", b, trunc)
	}
}

func TestRead_DirIsError(t *testing.T) {
	m, _ := newM(t)
	if _, _, err := m.Read("r", "sub"); err == nil {
		t.Fatal("expected error reading a directory")
	}
}

func TestWriteRead_Roundtrip(t *testing.T) {
	m, _ := newM(t)
	if err := m.Write("r", "deep/nested/note.md", []byte("# created")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, _, err := m.Read("r", "deep/nested/note.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(b) != "# created" {
		t.Fatalf("Read = %q", b)
	}
}

func TestEscape_Rejected(t *testing.T) {
	m, _ := newM(t)
	if _, err := m.List("r", "../../../etc"); !errors.Is(err, ErrEscape) {
		t.Fatalf("expected ErrEscape, got %v", err)
	}
	if _, _, err := m.Read("r", "../../etc/passwd"); !errors.Is(err, ErrEscape) {
		t.Fatalf("expected ErrEscape, got %v", err)
	}
	if err := m.Write("r", "../escape.txt", []byte("x")); !errors.Is(err, ErrEscape) {
		t.Fatalf("expected ErrEscape, got %v", err)
	}
}

func TestBadRoot_Rejected(t *testing.T) {
	m, _ := newM(t)
	if _, err := m.List("nope", ""); !errors.Is(err, ErrBadRoot) {
		t.Fatalf("got %v", err)
	}
}

func TestDelete_RootIsForbidden(t *testing.T) {
	m, _ := newM(t)
	if err := m.Delete("r", ""); !errors.Is(err, ErrBadPath) {
		t.Fatalf("deleting root should be ErrBadPath; got %v", err)
	}
}

func TestList_NotFound(t *testing.T) {
	m, _ := newM(t)
	if _, err := m.List("r", "definitely/not/here"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestList_FileTargetReturnsSingle(t *testing.T) {
	m, _ := newM(t)
	entries, err := m.List("r", "a.txt")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].IsDir || entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRead_TruncatesOverMax(t *testing.T) {
	m, root := newM(t)
	big := strings.Repeat("x", MaxReadBytes+1024)
	if err := os.WriteFile(filepath.Join(root, "big.log"), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, trunc, err := m.Read("r", "big.log")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !trunc {
		t.Fatal("expected truncated=true")
	}
	if int64(len(b)) != MaxReadBytes {
		t.Fatalf("len = %d want %d", len(b), MaxReadBytes)
	}
}
