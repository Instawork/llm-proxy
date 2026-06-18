package adminrollup

import (
	"context"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Cost reservations make daily cost-limit enforcement correct under
// concurrency and across instances. The async cost tracker records ACTUAL
// spend into the per-key by_key hash only AFTER a request finishes, so a check
// that reads only recorded spend lets many concurrent (and multi-instance)
// requests all pass the cap before any of them is charged — an unbounded
// overshoot. A reservation closes that window: before a request runs we
// atomically check (recorded_spend + outstanding_reservations) against the cap
// and, if under, add an estimate to a fleet-wide "reserved" hash. The estimate
// is reconciled to the actual cost when the request completes and released
// after a short grace period (long enough for the async actual to land in
// by_key), so the in-flight portion is always visible to concurrent checks.
//
// The reserved hash lives alongside the other per-day cost aggregates
// (dim "reserved", field = masked key id) so it shares the day's TTL and is
// cleaned up by the same day-rollover archive path.

const reservedDim = "reserved"

// reserveUnderLimitScript atomically reserves estimate for a key IFF the
// combined recorded spend + outstanding reservations is still under the cap.
// Returns 1 when reserved, 0 when blocked. limitCents <= 0 means unlimited and
// is handled by the caller (never invoked here).
//
//	KEYS[1] = by_key spend hash         ARGV[1] = spend field (key|spend_usd)
//	KEYS[2] = reserved hash             ARGV[2] = reserved field (masked key)
//	ARGV[3] = estimate USD              ARGV[4] = limit cents
//	ARGV[5] = ttl seconds
var reserveUnderLimitScript = redis.NewScript(`
local spend = tonumber(redis.call("HGET", KEYS[1], ARGV[1]) or "0")
local reserved = tonumber(redis.call("HGET", KEYS[2], ARGV[2]) or "0")
local effCents = math.ceil((spend + reserved) * 100)
if effCents >= tonumber(ARGV[4]) then
  return 0
end
redis.call("HINCRBYFLOAT", KEYS[2], ARGV[2], ARGV[3])
redis.call("EXPIRE", KEYS[2], tonumber(ARGV[5]))
return 1
`)

// addReservedScript adds delta (may be negative) to a reserved field, flooring
// the resulting value at 0 so concurrent over-releases can never drive a
// reservation negative (which would erroneously create cap headroom).
//
//	KEYS[1] = reserved hash   ARGV[1] = field   ARGV[2] = delta   ARGV[3] = ttl seconds
var addReservedScript = redis.NewScript(`
local v = redis.call("HINCRBYFLOAT", KEYS[1], ARGV[1], ARGV[2])
if tonumber(v) < 0 then
  redis.call("HSET", KEYS[1], ARGV[1], "0")
end
redis.call("EXPIRE", KEYS[1], tonumber(ARGV[3]))
return 1
`)

func (b *redisBackend) reserveUnderLimit(ctx context.Context, spendHashKey, spendField, reservedHashKey, reservedField string, estimate float64, limitCents int64, ttl time.Duration) (bool, error) {
	res, err := reserveUnderLimitScript.Run(
		ctx, b.rdb,
		[]string{spendHashKey, reservedHashKey},
		spendField, reservedField,
		strconv.FormatFloat(estimate, 'f', -1, 64),
		strconv.FormatInt(limitCents, 10),
		ttlSeconds(ttl),
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (b *redisBackend) addReserved(ctx context.Context, reservedHashKey, reservedField string, delta float64, ttl time.Duration) error {
	return addReservedScript.Run(
		ctx, b.rdb,
		[]string{reservedHashKey},
		reservedField,
		strconv.FormatFloat(delta, 'f', -1, 64),
		ttlSeconds(ttl),
	).Err()
}

func (b *memoryBackend) reserveUnderLimit(_ context.Context, spendHashKey, spendField, reservedHashKey, reservedField string, estimate float64, limitCents int64, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var spend float64
	if h := b.hash[spendHashKey]; h != nil {
		spend = h[spendField]
	}
	var reserved float64
	if h := b.hash[reservedHashKey]; h != nil {
		reserved = h[reservedField]
	}
	effCents := int64(ceil((spend + reserved) * 100))
	if effCents >= limitCents {
		return false, nil
	}
	h := b.hash[reservedHashKey]
	if h == nil {
		h = make(memHash)
		b.hash[reservedHashKey] = h
	}
	h[reservedField] += estimate
	b.data[reservedHashKey] = memEntry{value: "hash", expiresAt: memExpiry(ttl)}
	return true, nil
}

func (b *memoryBackend) addReserved(_ context.Context, reservedHashKey, reservedField string, delta float64, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.hash[reservedHashKey]
	if h == nil {
		h = make(memHash)
		b.hash[reservedHashKey] = h
	}
	h[reservedField] += delta
	if h[reservedField] < 0 {
		h[reservedField] = 0
	}
	b.data[reservedHashKey] = memEntry{value: "hash", expiresAt: memExpiry(ttl)}
	return nil
}

func ttlSeconds(ttl time.Duration) int {
	sec := int(ttl.Seconds())
	if sec <= 0 {
		sec = int(todayTTL.Seconds())
	}
	return sec
}

func memExpiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		ttl = todayTTL
	}
	return time.Now().Add(ttl)
}

// ceil avoids importing math just for one call in the memory backend.
func ceil(f float64) float64 {
	i := float64(int64(f))
	if f > i {
		return i + 1
	}
	return i
}

// ReserveKeySpend atomically reserves an estimated cost for keyID against its
// daily cap. Returns allowed=true when the reservation was made (request may
// proceed), false when the combined recorded+reserved spend has reached the
// cap (request should be blocked). limitCents must be > 0.
func (s *Store) ReserveKeySpend(ctx context.Context, metric, day, keyID string, estimateUSD float64, limitCents int64) (bool, error) {
	if s == nil || s.be == nil || keyID == "" {
		return true, nil
	}
	return s.be.reserveUnderLimit(
		ctx,
		dimKey(metric, day, "by_key"), dimMemberField(keyID, "spend_usd"),
		dimKey(metric, day, reservedDim), keyID,
		estimateUSD, limitCents, todayTTL,
	)
}

// AddKeyReservation adjusts keyID's outstanding reservation by delta (negative
// to release). The stored value is floored at 0.
func (s *Store) AddKeyReservation(ctx context.Context, metric, day, keyID string, deltaUSD float64) error {
	if s == nil || s.be == nil || keyID == "" {
		return nil
	}
	return s.be.addReserved(ctx, dimKey(metric, day, reservedDim), keyID, deltaUSD, todayTTL)
}

// ReservedKeySpendUSD reads keyID's current outstanding reservation total.
func (s *Store) ReservedKeySpendUSD(ctx context.Context, metric, day, keyID string) (float64, error) {
	if s == nil || s.be == nil || keyID == "" {
		return 0, nil
	}
	return s.be.hget(ctx, dimKey(metric, day, reservedDim), keyID)
}
