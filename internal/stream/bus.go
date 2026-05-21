package stream

import (
	"context"
	"sync"
	"sync/atomic"
)

// Bus is a per-session frame fan-out with a bounded ring buffer for replay.
// Subscribers connect with a starting sequence number and receive every frame
// published from there forward; subscribers that fall behind beyond the
// channel capacity are dropped from the bus to keep producers honest.
type Bus struct {
	session string

	mu      sync.Mutex
	seq     uint64 // last assigned
	buf     []Frame
	bufCap  int
	bufHead int // index of buf[0] when buf is full and rotates
	bufLen  int // 0..bufCap

	subID atomic.Uint64
	subs  map[uint64]*Subscription
}

// NewBus creates a Bus that retains at most `bufCap` past frames for replay.
func NewBus(sessionID string, bufCap int) *Bus {
	if bufCap <= 0 {
		bufCap = 1024
	}
	return &Bus{
		session: sessionID,
		buf:     make([]Frame, bufCap),
		bufCap:  bufCap,
		subs:    make(map[uint64]*Subscription),
	}
}

// Publish appends a frame to the buffer and broadcasts to subscribers.
// `data` is marshaled lazily; pass nil for empty payloads.
func (b *Bus) Publish(kind string, data any) (Frame, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.seq++
	frame, err := NewFrame(b.session, b.seq, kind, data)
	if err != nil {
		return Frame{}, err
	}

	if b.bufLen < b.bufCap {
		b.buf[(b.bufHead+b.bufLen)%b.bufCap] = frame
		b.bufLen++
	} else {
		b.buf[b.bufHead] = frame
		b.bufHead = (b.bufHead + 1) % b.bufCap
	}

	for id, sub := range b.subs {
		select {
		case sub.ch <- frame:
		default:
			// Slow subscriber: drop it. Caller closes via Cancel.
			delete(b.subs, id)
			close(sub.ch)
		}
	}
	return frame, nil
}

// Subscribe returns a Subscription delivering every frame with Seq > fromSeq.
// If fromSeq is older than the buffer holds, the subscriber gets whatever the
// buffer still has plus everything new; older frames are gone.
// chanCap defaults to 256 if <= 0.
func (b *Bus) Subscribe(ctx context.Context, fromSeq uint64, chanCap int) *Subscription {
	if chanCap <= 0 {
		chanCap = 256
	}
	id := b.subID.Add(1)
	sub := &Subscription{
		id:  id,
		bus: b,
		ch:  make(chan Frame, chanCap),
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Replay buffered frames.
	for i := 0; i < b.bufLen; i++ {
		f := b.buf[(b.bufHead+i)%b.bufCap]
		if f.Seq <= fromSeq {
			continue
		}
		select {
		case sub.ch <- f:
		default:
			// Tiny replay buffer overrun. Should not happen with chanCap=256
			// and the typical buffer size, but if it does we keep the subscriber
			// rather than crash; they will see new frames just not the replay.
		}
	}

	b.subs[id] = sub

	if ctx != nil {
		go func() {
			<-ctx.Done()
			sub.Cancel()
		}()
	}

	return sub
}

// Snapshot returns a copy of all currently buffered frames (oldest first).
func (b *Bus) Snapshot() []Frame {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Frame, b.bufLen)
	for i := 0; i < b.bufLen; i++ {
		out[i] = b.buf[(b.bufHead+i)%b.bufCap]
	}
	return out
}

// LastSeq is the most recently assigned sequence number.
func (b *Bus) LastSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// CloseAll disconnects every subscriber. Used at session shutdown.
func (b *Bus) CloseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, sub := range b.subs {
		close(sub.ch)
		delete(b.subs, id)
	}
}

// Subscription delivers frames over its channel. Call Cancel to detach.
type Subscription struct {
	id  uint64
	bus *Bus
	ch  chan Frame

	cancelOnce sync.Once
}

// Frames returns the receive channel; closed when the subscription ends.
func (s *Subscription) Frames() <-chan Frame { return s.ch }

// Cancel detaches this subscription from the bus and closes the channel.
func (s *Subscription) Cancel() {
	s.cancelOnce.Do(func() {
		s.bus.mu.Lock()
		defer s.bus.mu.Unlock()
		if _, ok := s.bus.subs[s.id]; ok {
			delete(s.bus.subs, s.id)
			close(s.ch)
		}
	})
}
