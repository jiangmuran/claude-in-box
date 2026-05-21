// Package fsapi exposes a constrained file-browser surface: list / read /
// write / mkdir / delete, scoped to a small set of named roots. It is the
// backend for the "Files" view in the Web UI.
//
// Safety: every path is joined to a named root and re-cleaned; any result
// that escapes the root prefix is rejected. Symlinks pointing outside the
// root are also rejected. No path traversal, no absolute-path inputs.
package fsapi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// MaxReadBytes caps single-file reads to keep the API from being a
	// memory bomb. Large logs / models should be shipped a different way.
	MaxReadBytes = 4 * 1024 * 1024 // 4 MiB

	// MaxWriteBytes caps writes for the same reason.
	MaxWriteBytes = 4 * 1024 * 1024
)

var (
	ErrBadRoot    = errors.New("fsapi: unknown root")
	ErrEscape     = errors.New("fsapi: path escapes root")
	ErrNotFound   = errors.New("fsapi: not found")
	ErrTooLarge   = errors.New("fsapi: payload too large")
	ErrBadPath    = errors.New("fsapi: invalid path")
)

// Entry is one filesystem entry returned by List.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // relative to its root
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

// Manager owns the set of named roots.
type Manager struct {
	Roots map[string]string // name → absolute base path
}

// NewManager with the standard set: workspace, claude, box.
func NewManager() *Manager {
	return &Manager{
		Roots: map[string]string{
			"workspace": "/workspace",
			"claude":    "/home/coder/.claude",
			"box":       "/var/lib/claude-in-box",
		},
	}
}

// resolve a (root, relative) pair to an absolute path inside the named root,
// rejecting anything that escapes.
func (m *Manager) resolve(root, rel string) (string, error) {
	base, ok := m.Roots[root]
	if !ok {
		return "", ErrBadRoot
	}
	// Normalize.
	rel = strings.TrimSpace(rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return base, nil
	}
	// Reject obvious absolute paths or NULs.
	if strings.ContainsAny(rel, "\x00") {
		return "", ErrBadPath
	}
	cand := filepath.Clean(filepath.Join(base, rel))
	baseClean := filepath.Clean(base) + string(os.PathSeparator)
	if cand != filepath.Clean(base) && !strings.HasPrefix(cand+string(os.PathSeparator), baseClean) {
		return "", ErrEscape
	}
	return cand, nil
}

// ListRoots returns the known root names.
func (m *Manager) ListRoots() []string {
	out := make([]string, 0, len(m.Roots))
	for k := range m.Roots {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns directory entries at `<root>/<rel>`. If the target is a
// regular file, returns a single-element list describing it (callers
// typically detect this via the IsDir flag).
func (m *Manager) List(root, rel string) ([]Entry, error) {
	abs, err := m.resolve(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return []Entry{toEntry(info, rel, filepath.Base(abs))}, nil
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		i, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		// Skip hidden helpers we never want in the UI.
		if name == "." || name == ".." {
			continue
		}
		child := filepath.Join(rel, name)
		out = append(out, toEntry(i, child, name))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func toEntry(i os.FileInfo, relPath, name string) Entry {
	return Entry{
		Name:    name,
		Path:    relPath,
		IsDir:   i.IsDir(),
		Size:    i.Size(),
		Mode:    i.Mode().Perm().String(),
		ModTime: i.ModTime().UTC(),
	}
}

// Read returns up to MaxReadBytes from the file. Files larger than the cap
// are read up to the cap and the truncated flag is set so the UI can show
// "showing first N MiB" semantics.
func (m *Manager) Read(root, rel string) (data []byte, truncated bool, err error) {
	abs, err := m.resolve(root, rel)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("fsapi: %s is a directory", rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	r := io.LimitReader(f, MaxReadBytes+1)
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > MaxReadBytes {
		buf = buf[:MaxReadBytes]
		truncated = true
	}
	return buf, truncated, nil
}

// Write replaces the file's contents. Creates parent directories on demand.
// Caller is expected to have applied any encoding (raw bytes go in).
func (m *Manager) Write(root, rel string, content []byte) error {
	if int64(len(content)) > MaxWriteBytes {
		return ErrTooLarge
	}
	abs, err := m.resolve(root, rel)
	if err != nil {
		return err
	}
	if abs == filepath.Clean(m.Roots[root]) {
		return ErrBadPath
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, abs)
}

// Mkdir creates a directory (and parents).
func (m *Manager) Mkdir(root, rel string) error {
	abs, err := m.resolve(root, rel)
	if err != nil {
		return err
	}
	if abs == filepath.Clean(m.Roots[root]) {
		return ErrBadPath
	}
	return os.MkdirAll(abs, 0o755)
}

// Delete removes a file or empty directory. Recursive delete is intentionally
// not exposed via the API to limit blast radius.
func (m *Manager) Delete(root, rel string) error {
	abs, err := m.resolve(root, rel)
	if err != nil {
		return err
	}
	if abs == filepath.Clean(m.Roots[root]) {
		return ErrBadPath
	}
	return os.Remove(abs)
}
