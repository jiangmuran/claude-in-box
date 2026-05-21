package stream

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBus_PublishAndSubscribe(t *testing.T) {
	bus := NewBus("s1", 16)
	defer bus.CloseAll()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, 0, 8)
	defer sub.Cancel()

	if _, err := bus.Publish(KindTextDelta, TextDeltaData{Text: "hello"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case f := <-sub.Frames():
		if f.Kind != KindTextDelta {
			t.Fatalf("kind = %q want %q", f.Kind, KindTextDelta)
		}
		if f.Seq != 1 {
			t.Fatalf("seq = %d want 1", f.Seq)
		}
		if f.Session != "s1" {
			t.Fatalf("session = %q want s1", f.Session)
		}
		if !strings.Contains(string(f.Data), `"hello"`) {
			t.Fatalf("data = %s want to contain hello", f.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for frame")
	}
}

func TestBus_ReplayFromSeq(t *testing.T) {
	bus := NewBus("s1", 16)

	for i := 0; i < 5; i++ {
		if _, err := bus.Publish(KindStatus, StatusData{State: StateWorking}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	sub := bus.Subscribe(context.Background(), 2, 16)
	defer sub.Cancel()

	got := drain(sub.Frames(), 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("len = %d want 3", len(got))
	}
	for i, f := range got {
		if f.Seq != uint64(3+i) {
			t.Fatalf("got seq %d want %d", f.Seq, 3+i)
		}
	}
}

func TestBus_RingBufferOverflowDropsOldest(t *testing.T) {
	bus := NewBus("s1", 3)
	for i := 0; i < 5; i++ {
		_, _ = bus.Publish(KindMeta, MetaData{Note: "n"})
	}
	snap := bus.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d want 3", len(snap))
	}
	if snap[0].Seq != 3 || snap[2].Seq != 5 {
		t.Fatalf("snap seqs = [%d ... %d] want [3 ... 5]", snap[0].Seq, snap[2].Seq)
	}
}

func TestBus_SlowSubscriberGetsDropped(t *testing.T) {
	bus := NewBus("s1", 64)
	sub := bus.Subscribe(context.Background(), 0, 1) // tiny channel
	for i := 0; i < 5; i++ {
		_, _ = bus.Publish(KindMeta, MetaData{Note: "n"})
	}
	// Read what we can; channel was closed after the bus dropped this sub.
	for range sub.Frames() {
	}
	// Bus should have published 5 frames regardless of the slow sub.
	if bus.LastSeq() != 5 {
		t.Fatalf("last seq = %d want 5", bus.LastSeq())
	}
}

func TestBus_CancelByContext(t *testing.T) {
	bus := NewBus("s1", 16)
	ctx, cancel := context.WithCancel(context.Background())
	sub := bus.Subscribe(ctx, 0, 8)

	cancel()

	// Give the goroutine a moment to react.
	select {
	case _, ok := <-sub.Frames():
		if ok {
			t.Fatal("got frame after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

func TestBus_ConcurrentPublishers(t *testing.T) {
	bus := NewBus("s1", 1024)
	sub := bus.Subscribe(context.Background(), 0, 4096)
	defer sub.Cancel()

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = bus.Publish(KindMeta, MetaData{Note: "x"})
			}
		}()
	}
	wg.Wait()

	if bus.LastSeq() != 800 {
		t.Fatalf("last seq = %d want 800", bus.LastSeq())
	}
}

func drain(ch <-chan Frame, n int, timeout time.Duration) []Frame {
	out := make([]Frame, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case f, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, f)
		case <-deadline:
			return out
		}
	}
	return out
}
