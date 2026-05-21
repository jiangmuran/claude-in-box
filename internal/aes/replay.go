package aes

import (
	"sync"
	"time"
)

// DefaultReplayWindow matches the spec: requests must be within 5 minutes of
// server time, and the same (KeyId, Nonce) pair cannot be reused inside
// that window.
const DefaultReplayWindow = 5 * time.Minute

// ReplayCache tracks recently-seen (KeyId, Nonce) pairs. Reject any pair
// that has been observed within the configured window.
//
// Implementation is intentionally simple: a map keyed by "<keyId>|<nonceHex>"
// holding the seen-at instant. We sweep expired entries opportunistically on
// CheckAndRecord; for a single box with a few devices and a 5-minute window,
// that keeps memory bounded without needing a background goroutine.
type ReplayCache struct {
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayCache constructs a cache with the default 5-minute window.
func NewReplayCache() *ReplayCache {
	return &ReplayCache{
		window: DefaultReplayWindow,
		now:    time.Now,
		seen:   make(map[string]time.Time),
	}
}

// WithWindow lets tests pin a tighter window.
func (c *ReplayCache) WithWindow(d time.Duration) *ReplayCache {
	c.window = d
	return c
}

// CheckAndRecord returns nil if (keyID, nonceHex) has NOT been seen inside
// the window, and also records it for future checks. Otherwise returns
// ErrReplay. The caller is responsible for the timestamp-drift check.
func (c *ReplayCache) CheckAndRecord(keyID, nonceHex string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.evictLocked(now)

	key := keyID + "|" + nonceHex
	if _, ok := c.seen[key]; ok {
		return ErrReplay
	}
	c.seen[key] = now
	return nil
}

// CheckTimestamp returns nil iff the request timestamp is within
// `window/2` of server time. Allowing half the window on each side gives
// devices with mild clock drift room without expanding the replay surface.
func (c *ReplayCache) CheckTimestamp(reqMillis int64) error {
	now := c.now().UTC().UnixMilli()
	diff := reqMillis - now
	if diff < 0 {
		diff = -diff
	}
	half := c.window.Milliseconds() / 2
	if diff > half {
		return ErrClockDrift
	}
	return nil
}

// Size returns the number of records currently retained (after a sweep).
func (c *ReplayCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(c.now())
	return len(c.seen)
}

func (c *ReplayCache) evictLocked(now time.Time) {
	for k, t := range c.seen {
		if now.Sub(t) > c.window {
			delete(c.seen, k)
		}
	}
}

// Sentinel errors so callers can map them to specific 4xx codes per spec.
var (
	ErrReplay     = sentinel("ReplayedNonce")
	ErrClockDrift = sentinel("ClockDrift")
)

type sentinel string

func (s sentinel) Error() string { return string(s) }
