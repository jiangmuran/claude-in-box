// Package ports lets the box expose an in-container service port to the
// outside on demand. The container exposes a pre-allocated host range
// (CIB_HOST_PORT_RANGE — set by docker run -p) and ports maps an
// internal port to one of those public ones by running socat as a
// forwarder.
package ports

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mapping is one live forwarder.
type Mapping struct {
	HostPort     int       `json:"host_port"`
	InternalPort int       `json:"internal_port"`
	InternalHost string    `json:"internal_host,omitempty"` // default 127.0.0.1
	CreatedAt    time.Time `json:"created_at"`
	cmd          *exec.Cmd
}

// Manager owns the active mappings.
type Manager struct {
	// Range is the inclusive port range we may use for new mappings. Set
	// from CIB_PORT_RANGE env (e.g. "9000-9019"). Empty = ports disabled.
	Range [2]int

	// SocatBin overrides the path to the socat binary. Default: "socat".
	SocatBin string

	mu       sync.Mutex
	mappings map[int]*Mapping
}

// NewManager validates the port range and returns a manager.
func NewManager(rangeSpec string) (*Manager, error) {
	m := &Manager{mappings: map[int]*Mapping{}, SocatBin: "socat"}
	if rangeSpec == "" {
		return m, nil
	}
	parts := strings.Split(rangeSpec, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("ports.NewManager: range must be N-M, got %q", rangeSpec)
	}
	a, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("ports.NewManager: bad lower: %w", err)
	}
	b, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("ports.NewManager: bad upper: %w", err)
	}
	if a < 1 || b < a || b > 65535 {
		return nil, fmt.Errorf("ports.NewManager: invalid range %d-%d", a, b)
	}
	m.Range = [2]int{a, b}
	return m, nil
}

// List returns active mappings, sorted by host port.
func (m *Manager) List() []*Mapping {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Mapping, 0, len(m.mappings))
	for _, mp := range m.mappings {
		out = append(out, mp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostPort < out[j].HostPort })
	return out
}

// Expose allocates a host port from the configured range and starts a
// socat forwarder TCP-LISTEN:<host>,fork → TCP:<internalHost>:<internal>.
// internalHost may be empty (defaults to 127.0.0.1).
//
// If internal == 0, fails.
func (m *Manager) Expose(internalPort int, internalHost string) (*Mapping, error) {
	if internalPort < 1 || internalPort > 65535 {
		return nil, errors.New("ports: invalid internal port")
	}
	if m.Range[0] == 0 {
		return nil, errors.New("ports: not configured — set CIB_PORT_RANGE on container start")
	}
	if internalHost == "" {
		internalHost = "127.0.0.1"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find a free host port in the range.
	host := -1
	for p := m.Range[0]; p <= m.Range[1]; p++ {
		if _, taken := m.mappings[p]; !taken {
			host = p
			break
		}
	}
	if host < 0 {
		return nil, errors.New("ports: no free host port in configured range")
	}

	cmd := exec.Command(m.SocatBin,
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", host),
		fmt.Sprintf("TCP:%s:%d", internalHost, internalPort),
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ports: socat start: %w", err)
	}
	mp := &Mapping{
		HostPort:     host,
		InternalPort: internalPort,
		InternalHost: internalHost,
		CreatedAt:    time.Now().UTC(),
		cmd:          cmd,
	}
	m.mappings[host] = mp
	return mp, nil
}

// Unexpose tears down the forwarder for the given host port.
func (m *Manager) Unexpose(hostPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, ok := m.mappings[hostPort]
	if !ok {
		return errors.New("ports: no such mapping")
	}
	if mp.cmd != nil && mp.cmd.Process != nil {
		_ = mp.cmd.Process.Kill()
	}
	delete(m.mappings, hostPort)
	return nil
}

// CloseAll tears down every forwarder. Called on cib shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hp, mp := range m.mappings {
		if mp.cmd != nil && mp.cmd.Process != nil {
			_ = mp.cmd.Process.Kill()
		}
		delete(m.mappings, hp)
	}
}
