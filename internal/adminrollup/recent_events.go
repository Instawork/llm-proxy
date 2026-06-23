package adminrollup

import (
	"context"
	"encoding/json"
	"time"
)

const recentEventsTTL = 48 * time.Hour

func recentEventsKey(metric string) string {
	return keyPrefix + metric + ":recent:events"
}

// AppendRecentEvent appends a JSON event to the metric's rolling recent list.
// No-op when the store is nil or event is nil.
func (s *Store) AppendRecentEvent(ctx context.Context, metric string, event any, maxLen int) error {
	if s == nil || s.be == nil || event == nil {
		return nil
	}
	if maxLen <= 0 {
		maxLen = 50
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.be.appendRecentEvent(ctx, recentEventsKey(metric), payload, maxLen, recentEventsTTL)
}

// LoadRecentEventPayloads reads up to maxLen JSON events (newest first).
func (s *Store) LoadRecentEventPayloads(ctx context.Context, metric string, maxLen int) ([]json.RawMessage, error) {
	if s == nil || s.be == nil {
		return nil, nil
	}
	if maxLen <= 0 {
		maxLen = 50
	}
	raw, err := s.be.loadRecentEvents(ctx, recentEventsKey(metric), maxLen)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *redisBackend) appendRecentEvent(ctx context.Context, key string, payload []byte, maxLen int, ttl time.Duration) error {
	pipe := b.rdb.Pipeline()
	pipe.LPush(ctx, key, payload)
	pipe.LTrim(ctx, key, 0, int64(maxLen-1))
	if ttl > 0 {
		pipe.Expire(ctx, key, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (b *redisBackend) loadRecentEvents(ctx context.Context, key string, maxLen int) ([]byte, error) {
	vals, err := b.rdb.LRange(ctx, key, 0, int64(maxLen-1)).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil
	}
	out := make([]json.RawMessage, 0, len(vals))
	for _, v := range vals {
		out = append(out, json.RawMessage(v))
	}
	return json.Marshal(out)
}

func (b *memoryBackend) appendRecentEvent(_ context.Context, key string, payload []byte, maxLen int, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	list := b.lists[key]
	list = append([]string{string(payload)}, list...)
	if len(list) > maxLen {
		list = list[:maxLen]
	}
	b.lists[key] = list
	b.data[key] = memEntry{value: "list", expiresAt: exp}
	return nil
}

func (b *memoryBackend) loadRecentEvents(_ context.Context, key string, maxLen int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if entry, ok := b.data[key]; ok && !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
		delete(b.data, key)
		delete(b.lists, key)
	}
	list := b.lists[key]
	if len(list) == 0 {
		return nil, nil
	}
	if len(list) > maxLen {
		list = list[:maxLen]
	}
	out := make([]json.RawMessage, 0, len(list))
	for _, v := range list {
		out = append(out, json.RawMessage(v))
	}
	return json.Marshal(out)
}
