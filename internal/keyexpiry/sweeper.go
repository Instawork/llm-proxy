package keyexpiry

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

const (
	defaultSweepInterval = 15 * time.Minute
	defaultGracePeriod   = 7 * 24 * time.Hour
	sweepOpTimeout       = 10 * time.Second
	perKeyTimeout        = 10 * time.Second
	leaseTTL             = 5 * time.Minute
)

// Config controls the expiry sweeper's cadence.
type Config struct {
	SweepInterval time.Duration
	GracePeriod   time.Duration
}

func (c Config) sweepInterval() time.Duration {
	if c.SweepInterval > 0 {
		return c.SweepInterval
	}
	return defaultSweepInterval
}

func (c Config) gracePeriod() time.Duration {
	if c.GracePeriod > 0 {
		return c.GracePeriod
	}
	return defaultGracePeriod
}

// sweeperStore is the subset of apikeys.Store the sweeper needs.
type sweeperStore interface {
	deleter
	ListExpiredKeys(ctx context.Context, cutoff time.Time) ([]*apikeys.APIKey, error)
	TryAcquireSweepLease(ctx context.Context, holder string, ttl time.Duration) (bool, error)
}

// Sweeper periodically retires keys that expired more than the configured
// grace period ago. A DynamoDB lease ensures only one ECS task sweeps at a
// time; losing the race is a normal no-op, not an error.
type Sweeper struct {
	store  sweeperStore
	rev    revoker
	cfg    Config
	logger *slog.Logger
	holder string
}

// NewSweeper constructs a Sweeper. rev may be nil when upstream provisioning
// is disabled; retirement then just deletes the proxy key record.
func NewSweeper(store sweeperStore, rev revoker, cfg Config, logger *slog.Logger) *Sweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{
		store:  store,
		rev:    rev,
		cfg:    cfg,
		logger: logger,
		holder: sweeperHolderID(),
	}
}

// Run ticks at the configured interval until stop is closed, sweeping
// expired keys on each tick.
func (s *Sweeper) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(s.cfg.sweepInterval())
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), sweepOpTimeout)
			if _, err := s.SweepOnce(ctx); err != nil {
				s.logger.Error("keyexpiry: sweep failed", "error", err)
			}
			cancel()
		}
	}
}

// SweepOnce retires every key whose expiry is older than the grace period,
// provided this instance wins the sweep lease. Returns the number retired.
func (s *Sweeper) SweepOnce(ctx context.Context) (int, error) {
	acquired, err := s.store.TryAcquireSweepLease(ctx, s.holder, leaseTTL)
	if err != nil {
		return 0, err
	}
	if !acquired {
		return 0, nil
	}

	cutoff := time.Now().Add(-s.cfg.gracePeriod())
	expired, err := s.store.ListExpiredKeys(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, record := range expired {
		keyCtx, cancel := context.WithTimeout(ctx, perKeyTimeout)
		err := RetireKey(keyCtx, s.store, s.rev, record, s.logger)
		cancel()
		if err != nil {
			s.logger.Error("keyexpiry: retire failed", "key", apikeys.RedactKey(record.PK), "error", err)
			continue
		}
		deleted++
	}
	return deleted, nil
}

func sweeperHolderID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "llm-proxy"
}
