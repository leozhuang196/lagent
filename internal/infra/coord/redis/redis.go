// Package redis implements the coord primitives on Redis. Redis holds
// coordination state only, never facts (P2): losing it must at worst delay
// work, never corrupt it. That is why fencing tokens are seeded from the Redis
// clock (see acquireScript) instead of trusting a counter that may vanish.
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// minFenceTTL keeps the per-key token counter alive long after its lease is
// released, so a quick re-acquire continues the sequence. Even if it expires,
// the time-based floor keeps tokens increasing.
const minFenceTTL = 7 * 24 * time.Hour

// Coord implements coord.Lease, coord.Signal, coord.Counter and coord.Semaphore.
type Coord struct {
	rdb goredis.UniversalClient
}

// New wraps a go-redis client. The caller owns (and closes) the client.
func New(rdb goredis.UniversalClient) *Coord { return &Coord{rdb: rdb} }

func fenceKey(key string) string { return key + ":fence" }

// acquireScript takes the lease if free and returns a new fencing token, or 0.
//
// Tokens are persisted in turns (Turn.Epoch) and compared there, so they must
// keep increasing even if Redis loses the counter (eviction, failover, flush).
// When the counter is missing it is seeded with the Redis server time in
// microseconds, which is larger than any token issued earlier from a counter
// seeded the same way. The token is stored as the counter's string form
// (GET after INCR) because Lua numbers passed to redis.call are formatted as
// floats and could lose digits.
//
// KEYS[1] lease key, KEYS[2] fence counter; ARGV[1] lease ttl ms, ARGV[2] fence ttl ms.
var acquireScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
if redis.call('EXISTS', KEYS[2]) == 0 then
  local t = redis.call('TIME')
  redis.call('SET', KEYS[2], t[1] .. string.format('%06d', tonumber(t[2])))
end
local tok = redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
redis.call('SET', KEYS[1], redis.call('GET', KEYS[2]), 'PX', ARGV[1])
return tok
`)

// renewScript extends the lease only while token still holds it.
var renewScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

// releaseScript deletes the lease only while token still holds it, so a
// holder that lost its lease cannot free someone else's.
var releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func ms(d time.Duration) int64 {
	n := d.Milliseconds()
	if n < 1 {
		n = 1
	}
	return n
}

func (c *Coord) Acquire(ctx context.Context, key string, ttl time.Duration) (int64, bool, error) {
	fttl := minFenceTTL
	if 10*ttl > fttl {
		fttl = 10 * ttl
	}
	tok, err := acquireScript.Run(ctx, c.rdb, []string{key, fenceKey(key)}, ms(ttl), ms(fttl)).Int64()
	if err != nil {
		return 0, false, err
	}
	if tok == 0 {
		return 0, false, nil
	}
	return tok, true, nil
}

func (c *Coord) Renew(ctx context.Context, key string, token int64, ttl time.Duration) (bool, error) {
	n, err := renewScript.Run(ctx, c.rdb, []string{key}, strconv.FormatInt(token, 10), ms(ttl)).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (c *Coord) Release(ctx context.Context, key string, token int64) error {
	return releaseScript.Run(ctx, c.rdb, []string{key}, strconv.FormatInt(token, 10)).Err()
}

// Publish is fire-and-forget: subscribers that are not connected miss it.
func (c *Coord) Publish(ctx context.Context, channel string, msg []byte) error {
	return c.rdb.Publish(ctx, channel, msg).Err()
}

// Subscribe waits for Redis to confirm the subscription so that messages
// published after it returns are delivered. Delivery to ch never blocks: a
// slow reader loses messages, which the Signal contract allows.
func (c *Coord) Subscribe(ctx context.Context, channel string) (<-chan []byte, func(), error) {
	ps := c.rdb.Subscribe(ctx, channel)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, nil, err
	}
	out := make(chan []byte, 16)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			_ = ps.Close()
		})
	}
	go func() {
		// ps.Channel reconnects and resubscribes on network errors by itself.
		in := ps.Channel()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				cancel()
				return
			case m, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- []byte(m.Payload):
				default:
				}
			}
		}
	}()
	return out, cancel, nil
}

// incrScript sets the TTL only when the counter is created, matching the
// in-memory counter: a budget window does not slide with every increment.
var incrScript = goredis.NewScript(`
local n = redis.call('INCRBY', KEYS[1], ARGV[1])
if tonumber(ARGV[2]) > 0 and redis.call('PTTL', KEYS[1]) == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return n
`)

func (c *Coord) IncrBy(ctx context.Context, key string, n int64, ttl time.Duration) (int64, error) {
	var t int64
	if ttl > 0 {
		t = ms(ttl)
	}
	return incrScript.Run(ctx, c.rdb, []string{key}, n, t).Int64()
}

func (c *Coord) Get(ctx context.Context, key string) (int64, error) {
	n, err := c.rdb.Get(ctx, key).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	return n, err
}

// semAcquireScript keeps one ZSET member per held slot scored by its expiry
// (Redis server time, so instances with skewed clocks agree). Expired slots
// of crashed holders are purged before counting.
//
// KEYS[1] zset; ARGV[1] limit, ARGV[2] ttl ms, ARGV[3] slot id.
var semAcquireScript = goredis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local limit = tonumber(ARGV[1])
if limit > 0 and redis.call('ZCARD', KEYS[1]) >= limit then
  return 0
end
local ttl = tonumber(ARGV[2])
redis.call('ZADD', KEYS[1], now + ttl, ARGV[3])
if redis.call('PTTL', KEYS[1]) < ttl then
  redis.call('PEXPIRE', KEYS[1], ttl)
end
return 1
`)

func (c *Coord) TryAcquire(ctx context.Context, key string, limit int, ttl time.Duration) (func(), bool, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, false, err
	}
	slot := hex.EncodeToString(b[:])
	n, err := semAcquireScript.Run(ctx, c.rdb, []string{key}, limit, ms(ttl), slot).Int64()
	if err != nil || n == 0 {
		return nil, false, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Release usually runs in defers after the caller's ctx ended; the
			// slot would otherwise linger until it expires.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = c.rdb.ZRem(rctx, key, slot).Err()
		})
	}, true, nil
}
