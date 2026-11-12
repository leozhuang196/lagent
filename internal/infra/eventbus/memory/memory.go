// Package memory is an in-process event bus.
package memory

import (
	"context"
	"sync"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

type stream struct {
	seq    int64
	events []types.Event
	subs   map[int]chan types.Event
}

// Bus keeps the last Cap events per stream.
type Bus struct {
	Cap int
	clk clock.Clock

	mu      sync.Mutex
	streams map[string]*stream
	nextSub int
}

func New(capacity int, clk clock.Clock) *Bus {
	if capacity <= 0 {
		capacity = 10000
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Bus{Cap: capacity, clk: clk, streams: map[string]*stream{}}
}

func key(t, s string) string { return t + "/" + s }

func (b *Bus) get(k string) *stream {
	st, ok := b.streams[k]
	if !ok {
		st = &stream{subs: map[int]chan types.Event{}}
		b.streams[k] = st
	}
	return st
}

func (b *Bus) Append(_ context.Context, ev types.Event) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(key(ev.TenantID, ev.Stream))
	st.seq++
	ev.Seq = st.seq
	if ev.TS.IsZero() {
		ev.TS = b.clk.Now()
	}
	st.events = append(st.events, ev)
	if len(st.events) > b.Cap {
		st.events = st.events[len(st.events)-b.Cap:]
	}
	for _, ch := range st.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: it will catch up through Read
		}
	}
	return ev.Seq, nil
}

func (b *Bus) Read(ctx context.Context, s string, after int64, limit int) ([]types.Event, bool, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return nil, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(key(t, s))
	if limit <= 0 {
		limit = 1000
	}
	reset := after > st.seq || (len(st.events) > 0 && after < st.events[0].Seq-1)
	var out []types.Event
	for _, ev := range st.events {
		if ev.Seq > after {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out, reset, nil
}

func (b *Bus) Subscribe(ctx context.Context, s string) (<-chan types.Event, func(), error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return nil, nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.get(key(t, s))
	b.nextSub++
	idx := b.nextSub
	ch := make(chan types.Event, 256)
	st.subs[idx] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(st.subs, idx)
			b.mu.Unlock()
		})
	}
	return ch, cancel, nil
}

func (b *Bus) LastSeq(ctx context.Context, s string) (int64, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.get(key(t, s)).seq, nil
}
