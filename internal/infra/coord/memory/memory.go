// Package memory implements coord primitives in process.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/eternityx/lagent/internal/core/clock"
)

type lease struct {
	token   int64
	expires time.Time
}

type counter struct {
	n       int64
	expires time.Time
}

type slot struct {
	id      int64
	expires time.Time
}

// Coord implements coord.Lease, Signal, Counter and Semaphore.
type Coord struct {
	clk clock.Clock

	mu       sync.Mutex
	leases   map[string]*lease
	epochs   map[string]int64
	subs     map[string]map[int]chan []byte
	nextSub  int
	counters map[string]*counter
	slots    map[string][]slot
	nextSlot int64
}

func New(clk clock.Clock) *Coord {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Coord{
		clk: clk, leases: map[string]*lease{}, epochs: map[string]int64{},
		subs: map[string]map[int]chan []byte{}, counters: map[string]*counter{}, slots: map[string][]slot{},
	}
}

func (c *Coord) Acquire(_ context.Context, key string, ttl time.Duration) (int64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	if l, ok := c.leases[key]; ok && now.Before(l.expires) {
		return 0, false, nil
	}
	c.epochs[key]++
	tok := c.epochs[key]
	c.leases[key] = &lease{token: tok, expires: now.Add(ttl)}
	return tok, true, nil
}

func (c *Coord) Renew(_ context.Context, key string, token int64, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	l, ok := c.leases[key]
	if !ok || l.token != token || !now.Before(l.expires) {
		return false, nil
	}
	l.expires = now.Add(ttl)
	return true, nil
}

func (c *Coord) Release(_ context.Context, key string, token int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leases[key]; ok && l.token == token {
		delete(c.leases, key)
	}
	return nil
}

func (c *Coord) Publish(_ context.Context, ch string, msg []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.subs[ch] {
		select {
		case s <- msg:
		default:
		}
	}
	return nil
}

func (c *Coord) Subscribe(_ context.Context, ch string) (<-chan []byte, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs[ch] == nil {
		c.subs[ch] = map[int]chan []byte{}
	}
	c.nextSub++
	idx := c.nextSub
	out := make(chan []byte, 16)
	c.subs[ch][idx] = out
	var once sync.Once
	return out, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs[ch], idx)
			c.mu.Unlock()
		})
	}, nil
}

func (c *Coord) IncrBy(_ context.Context, key string, n int64, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	ct, ok := c.counters[key]
	if !ok || (!ct.expires.IsZero() && !now.Before(ct.expires)) {
		ct = &counter{}
		if ttl > 0 {
			ct.expires = now.Add(ttl)
		}
		c.counters[key] = ct
	}
	ct.n += n
	return ct.n, nil
}

func (c *Coord) Get(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ct, ok := c.counters[key]
	if !ok || (!ct.expires.IsZero() && !c.clk.Now().Before(ct.expires)) {
		return 0, nil
	}
	return ct.n, nil
}

func (c *Coord) TryAcquire(_ context.Context, key string, limit int, ttl time.Duration) (func(), bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	live := c.slots[key][:0]
	for _, s := range c.slots[key] {
		if now.Before(s.expires) {
			live = append(live, s)
		}
	}
	c.slots[key] = live
	if limit > 0 && len(live) >= limit {
		return nil, false, nil
	}
	c.nextSlot++
	idv := c.nextSlot
	c.slots[key] = append(c.slots[key], slot{id: idv, expires: now.Add(ttl)})
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			cur := c.slots[key]
			for i, s := range cur {
				if s.id == idv {
					c.slots[key] = append(cur[:i:i], cur[i+1:]...)
					break
				}
			}
		})
	}, true, nil
}
