package ratelimit

import (
	"context"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
)

type redisLimiter struct{}

func NewRedisLimiter(cfg *config.YAMLConfig) (RateLimiter, error) {
	_ = cfg
	return &redisLimiter{}, nil
}

func (r *redisLimiter) CheckAndReserve(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) (ReservationResult, error) {
	return ReservationResult{Allowed: true, ReservationID: id}, nil
}

func (r *redisLimiter) Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, now time.Time) error {
	return nil
}

func (r *redisLimiter) Cancel(ctx context.Context, id string, scope ScopeKeys, now time.Time) error {
	return nil
}
