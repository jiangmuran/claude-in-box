package ports

import (
	"errors"
	"testing"
)

func TestNewManager_RangeParsing(t *testing.T) {
	cases := []struct {
		spec    string
		ok      bool
		lo, hi  int
	}{
		{"", true, 0, 0},
		{"9000-9019", true, 9000, 9019},
		{"  9000 - 9019 ", true, 9000, 9019},
		{"9000", false, 0, 0},
		{"-9019", false, 0, 0},
		{"9000-8000", false, 0, 0}, // upper < lower
		{"abc-def", false, 0, 0},
		{"0-1024", false, 0, 0}, // lower < 1
		{"9000-99999", false, 0, 0}, // upper > 65535
	}
	for _, c := range cases {
		m, err := NewManager(c.spec)
		if c.ok && err != nil {
			t.Fatalf("NewManager(%q): unexpected error %v", c.spec, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("NewManager(%q): expected error", c.spec)
		}
		if c.ok && (m.Range[0] != c.lo || m.Range[1] != c.hi) {
			t.Fatalf("NewManager(%q): range = %v want %d-%d", c.spec, m.Range, c.lo, c.hi)
		}
	}
}

func TestExpose_RequiresConfiguredRange(t *testing.T) {
	m, _ := NewManager("")
	_, err := m.Expose(8000, "")
	if err == nil {
		t.Fatal("expected error when ports manager has no range")
	}
}

func TestExpose_BadInternalPort(t *testing.T) {
	m, _ := NewManager("9000-9001")
	_, err := m.Expose(0, "")
	if err == nil {
		t.Fatal("expected error for internal=0")
	}
}

func TestUnexpose_NotFound(t *testing.T) {
	m, _ := NewManager("9000-9001")
	if err := m.Unexpose(9000); !errors.Is(err, errors.Unwrap(err)) && err.Error() != "ports: no such mapping" {
		t.Fatalf("got %v", err)
	}
}
