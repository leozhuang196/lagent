// Package redis is the production event bus: one Redis Stream per
// (tenant, stream) acting as live channel and replay buffer with a TTL. It is
// not the source of truth (P2); a client whose cursor fell out of the buffer
// gets reset=true and reloads a snapshot.
//
// Keys:
//
//	t:{tenant}:events:{stream}         the Redis Stream; entry ID is "<seq>-1"
//	t:{tenant}:events:{stream}:seq     the seq counter
//	t:{tenant}:events:{stream}:notify  pub/sub channel carrying the new seq
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/tenant"
	"github.com/eternityx/lagent/internal/core/types"
)

const (
	// DefaultMaxLen bounds a stream (approximately) like the memory bus cap.
	DefaultMaxLen = 20000
	// minSeqTTL keeps the counter much longer than the stream so that a
	// stream that expired while idle continues its seq instead of restarting
	// at 1 under clients that still hold an old cursor.
	minSeqTTL = 30 * 24 * time.Hour
	// pollEvery is the safety net under pub/sub notifications, which are
	// best-effort (lost across reconnects).
	pollEvery = 2 * time.Second
	subBuffer = 256
	readBatch = 500
)

// Options configures a Bus.
type Options struct {
	TTL    time.Duration // stream TTL, refreshed on append (default 24h)
	MaxLen int64         // approximate MAXLEN (default DefaultMaxLen)
	Clock  clock.Clock
	Log    *slog.Logger
}

// Bus implements eventbus.Bus.
type Bus struct {
	rdb    goredis.UniversalClient
	ttl    time.Duration
	seqTTL time.Duration
	maxLen int64
	clk    clock.Clock
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	ps       *goredis.PubSub
	watchers map[string]*watcher        // by notify channel
	waiters  map[string][]chan struct{} // pending SUBSCRIBE confirmations
	nextSub  int
	closed   bool
	wg       sync.WaitGroup
}

// New builds a bus on rdb. The caller owns (and closes) the client; Close
// stops the bus's own goroutines and pub/sub connection.
func New(rdb goredis.UniversalClient, o Options) *Bus {
	if o.TTL <= 0 {
		o.TTL = 24 * time.Hour
	}
	if o.MaxLen <= 0 {
		o.MaxLen = DefaultMaxLen
	}
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	seqTTL := minSeqTTL
	if 7*o.TTL > seqTTL {
		seqTTL = 7 * o.TTL
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Bus{
		rdb: rdb, ttl: o.TTL, seqTTL: seqTTL, maxLen: o.MaxLen, clk: o.Clock, log: o.Log,
		ctx: ctx, cancel: cancel,
		watchers: map[string]*watcher{}, waiters: map[string][]chan struct{}{},
	}
}

func streamKey(t, s string) string { return "t:" + t + ":events:" + s }
func seqKey(k string) string       { return k + ":seq" }
func notifyChan(k string) string   { return k + ":notify" }

// appendScript allocates the seq and appends in one step, so seq is strictly
// increasing per stream even with appenders in different processes. The seq
// is the stream entry ID ("<seq>-1"), which Redis itself forces to increase.
// If the counter was lost while the stream survived (eviction), it is
// re-seeded from the newest entry instead of colliding with it.
//
// KEYS[1] stream, KEYS[2] seq counter; ARGV[1] event JSON, ARGV[2] maxlen,
// ARGV[3] stream ttl ms, ARGV[4] counter ttl ms, ARGV[5] notify channel.
var appendScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 0 then
  local last = redis.call('XREVRANGE', KEYS[1], '+', '-', 'COUNT', 1)
  if #last > 0 then
    local id = last[1][1]
    redis.call('SET', KEYS[2], string.sub(id, 1, string.find(id, '-', 1, true) - 1))
  end
end
local seq = redis.call('INCR', KEYS[2])
local s = redis.call('GET', KEYS[2])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
redis.call('XADD', KEYS[1], 'MAXLEN', '~', ARGV[2], s .. '-1', 'e', ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('PUBLISH', ARGV[5], s)
return seq
`)

// Append stores ev under its own tenant. Seq is not part of the stored JSON:
// it is the entry ID and is restored on read.
func (b *Bus) Append(ctx context.Context, ev types.Event) (int64, error) {
	if ev.TenantID == "" {
		return 0, errors.New("eventbus: event without tenant")
	}
	if ev.TS.IsZero() {
		ev.TS = b.clk.Now()
	}
	ev.Seq = 0
	raw, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	k := streamKey(ev.TenantID, ev.Stream)
	return appendScript.Run(ctx, b.rdb, []string{k, seqKey(k)},
		string(raw), b.maxLen, b.ttl.Milliseconds(), b.seqTTL.Milliseconds(), notifyChan(k)).Int64()
}

func decode(msgs []goredis.XMessage) ([]types.Event, error) {
	out := make([]types.Event, 0, len(msgs))
	for _, m := range msgs {
		seq, err := idSeq(m.ID)
		if err != nil {
			return nil, err
		}
		raw, _ := m.Values["e"].(string)
		var ev types.Event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil, fmt.Errorf("eventbus: decode %s: %w", m.ID, err)
		}
		ev.Seq = seq
		out = append(out, ev)
	}
	return out, nil
}

func idSeq(id string) (int64, error) {
	ms, _, _ := strings.Cut(id, "-")
	return strconv.ParseInt(ms, 10, 64)
}

// Read returns events with Seq > after. reset is true when after is older
// than the oldest retained entry, or newer than the current seq (the cursor
// belongs to a stream incarnation that expired).
func (b *Bus) Read(ctx context.Context, s string, after int64, limit int) ([]types.Event, bool, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 1000
	}
	if after < 0 {
		after = 0
	}
	k := streamKey(t, s)
	var rng, first *goredis.XMessageSliceCmd
	var last *goredis.StringCmd
	// One MULTI so the window, oldest entry and counter are a consistent view.
	_, err = b.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		// Entries always use <seq>-1. Starting inclusively at <after>-2
		// skips that sequence without Redis 6.2 exclusive-bound syntax.
		// It also avoids overflowing after+1 for a maximal client cursor.
		rng = p.XRangeN(ctx, k, strconv.FormatInt(after, 10)+"-2", "+", int64(limit))
		first = p.XRangeN(ctx, k, "-", "+", 1)
		last = p.Get(ctx, seqKey(k))
		return nil
	})
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, false, err
	}
	evs, err := decode(rng.Val())
	if err != nil {
		return nil, false, err
	}
	var lastSeq int64
	if v, e := last.Int64(); e == nil {
		lastSeq = v
	}
	reset := after > lastSeq
	if f := first.Val(); len(f) > 0 {
		oldest, err := idSeq(f[0].ID)
		if err != nil {
			return nil, false, err
		}
		if after < oldest-1 {
			reset = true
		}
	}
	if len(first.Val()) == 0 && after < lastSeq {
		reset = true // The entire buffer expired, including its oldest entry.
	}
	return evs, reset, nil
}

// LastSeq returns the newest seq of a stream (0 if empty or expired).
func (b *Bus) LastSeq(ctx context.Context, s string) (int64, error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return 0, err
	}
	n, err := b.rdb.Get(ctx, seqKey(streamKey(t, s))).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	return n, err
}

// watcher serves all local subscribers of one stream. Appends publish the new
// seq; the watcher then reads the stream from its cursor, so a lost
// notification only delays delivery until the next one or the next poll.
type watcher struct {
	key    string
	cursor int64 // touched only by the watcher goroutine after start
	subs   map[int]*sub
	kick   chan struct{}
	stop   chan struct{}
}

type sub struct {
	ch    chan types.Event
	floor int64 // deliver only events appended after Subscribe
}

// Subscribe delivers events appended after the call. Delivery never blocks
// the bus: a subscriber whose buffer is full misses events and must catch up
// through Read (the SSE handler fills gaps that way).
func (b *Bus) Subscribe(ctx context.Context, s string) (<-chan types.Event, func(), error) {
	t, err := tenant.ID(ctx)
	if err != nil {
		return nil, nil, err
	}
	k := streamKey(t, s)
	ch := notifyChan(k)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, errors.New("eventbus: closed")
	}
	if b.ps == nil {
		b.ps = b.rdb.Subscribe(b.ctx)
		b.wg.Add(1)
		go b.dispatch(b.ps)
	}
	var confirmed chan struct{}
	if b.watchers[ch] == nil {
		confirmed = make(chan struct{})
		b.waiters[ch] = append(b.waiters[ch], confirmed)
		if err := b.ps.Subscribe(ctx, ch); err != nil {
			b.dropWaiter(ch, confirmed)
			b.mu.Unlock()
			return nil, nil, err
		}
	}
	b.mu.Unlock()

	// Wait until Redis has the subscription so the very next append notifies
	// us. If confirmation is slow, polling still delivers.
	if confirmed != nil {
		select {
		case <-confirmed:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	// Read the floor only after subscribing: anything appended later is
	// > floor and will be seen either by notification or by poll.
	floor, err := b.LastSeq(ctx, s)
	if err != nil {
		b.mu.Lock()
		if b.watchers[ch] == nil {
			b.dropWaiter(ch, confirmed)
			_ = b.ps.Unsubscribe(context.WithoutCancel(ctx), ch)
		}
		b.mu.Unlock()
		return nil, nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, errors.New("eventbus: closed")
	}
	w := b.watchers[ch]
	if w == nil {
		w = &watcher{key: k, cursor: floor, subs: map[int]*sub{}, kick: make(chan struct{}, 1), stop: make(chan struct{})}
		b.watchers[ch] = w
		b.wg.Add(1)
		go b.watch(w)
	}
	b.nextSub++
	id := b.nextSub
	sb := &sub{ch: make(chan types.Event, subBuffer), floor: floor}
	w.subs[id] = sb
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(w.subs, id)
			if len(w.subs) == 0 && b.watchers[ch] == w {
				delete(b.watchers, ch)
				close(w.stop)
				if !b.closed {
					// Under b.mu so it cannot reorder with a re-subscribe.
					_ = b.ps.Unsubscribe(b.ctx, ch)
				}
			}
		})
	}
	return sb.ch, cancel, nil
}

func (b *Bus) dropWaiter(ch string, c chan struct{}) {
	ws := b.waiters[ch]
	for i, x := range ws {
		if x == c {
			b.waiters[ch] = append(ws[:i:i], ws[i+1:]...)
			break
		}
	}
	if len(b.waiters[ch]) == 0 {
		delete(b.waiters, ch)
	}
}

// dispatch reads the shared pub/sub connection. go-redis reconnects and
// resubscribes on errors; after a reconnect every watcher is kicked because
// notifications may have been lost meanwhile.
func (b *Bus) dispatch(ps *goredis.PubSub) {
	defer b.wg.Done()
	for {
		msg, err := ps.Receive(b.ctx)
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			b.log.Debug("eventbus pubsub receive", "err", err)
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			b.mu.Lock()
			for _, w := range b.watchers {
				w.poke()
			}
			b.mu.Unlock()
			continue
		}
		switch m := msg.(type) {
		case *goredis.Subscription:
			if m.Kind == "subscribe" {
				b.mu.Lock()
				for _, c := range b.waiters[m.Channel] {
					close(c)
				}
				delete(b.waiters, m.Channel)
				if w := b.watchers[m.Channel]; w != nil {
					w.poke() // resubscribed after reconnect
				}
				b.mu.Unlock()
			}
		case *goredis.Message:
			b.mu.Lock()
			if w := b.watchers[m.Channel]; w != nil {
				w.poke()
			}
			b.mu.Unlock()
		}
	}
}

func (w *watcher) poke() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (b *Bus) watch(w *watcher) {
	defer b.wg.Done()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-w.stop:
			return
		case <-w.kick:
		case <-tick.C:
		}
		b.drain(w)
	}
}

// drain reads everything after the watcher's cursor and fans it out.
func (b *Bus) drain(w *watcher) {
	for {
		msgs, err := b.rdb.XRangeN(b.ctx, w.key, strconv.FormatInt(w.cursor+1, 10), "+", readBatch).Result()
		if err != nil {
			if b.ctx.Err() == nil {
				b.log.Warn("eventbus watch read", "stream", w.key, "err", err)
			}
			return
		}
		evs, err := decode(msgs)
		if err != nil {
			b.log.Warn("eventbus watch decode", "stream", w.key, "err", err)
			return
		}
		if len(evs) == 0 {
			return
		}
		b.mu.Lock()
		for _, ev := range evs {
			for _, sb := range w.subs {
				if ev.Seq <= sb.floor {
					continue
				}
				select {
				case sb.ch <- ev:
				default: // slow subscriber: it will catch up through Read
				}
			}
		}
		b.mu.Unlock()
		w.cursor = evs[len(evs)-1].Seq
		if len(evs) < readBatch {
			return
		}
	}
}

// Close stops all watchers and the pub/sub connection. Subscriber channels
// are left open (as with the memory bus); callers stop on their own ctx.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	ps := b.ps
	b.mu.Unlock()
	b.cancel()
	var err error
	if ps != nil {
		err = ps.Close()
	}
	b.wg.Wait()
	return err
}
