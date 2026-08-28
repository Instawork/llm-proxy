// Package keyexpiry retires proxy API keys: it best-effort revokes the
// upstream provider credential (when the key was provisioned) and then
// deletes the DynamoDB record. The admin delete handler and the expiry
// sweeper share this single path so both stay in sync.
package keyexpiry

import (
	"context"
	"log/slog"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

// deleter is the subset of apikeys.Store used to retire a key.
type deleter interface {
	DeleteKey(ctx context.Context, key string) error
}

// revoker is the subset of provision.Manager used to revoke an upstream
// credential. Manager.Revoke already no-ops for providers it doesn't manage.
type revoker interface {
	Revoke(ctx context.Context, provider, upstreamID, upstreamKind string) error
}

// RetireKey revokes the upstream provider credential for a provisioned key
// (logging but not failing on revoke errors, since a key that can't be
// revoked upstream should still stop accepting proxy traffic), then deletes
// the proxy key record.
func RetireKey(ctx context.Context, store deleter, rev revoker, record *apikeys.APIKey, logger *slog.Logger) error {
	if record.Provisioned && rev != nil {
		if revokeErr := rev.Revoke(ctx, record.Provider, record.UpstreamKeyID, record.UpstreamKind); revokeErr != nil {
			logger.Warn(
				"keyexpiry: upstream revoke failed",
				"key", apikeys.RedactKey(record.PK),
				"provider", record.Provider,
				"upstream_id", record.UpstreamKeyID,
				"error", revokeErr,
			)
		}
	}
	return store.DeleteKey(ctx, record.PK)
}
