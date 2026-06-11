package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

func TestNewPerKeyOverrideProvider(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2"})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx := context.Background()
	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil, apikeys.KeyRateLimits{RPM: 7, TPM: 8})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	lookup := newPerKeyOverrideProvider(store, slog.Default())

	lc, ok := lookup(key.PK)
	if !ok {
		t.Fatal("expected override for key with rate limits")
	}
	if lc.RequestsPerMinute != 7 || lc.TokensPerMinute != 8 {
		t.Fatalf("unexpected limits: %+v", lc)
	}

	_, ok = lookup(apikeys.KeyPrefix + "missing")
	if ok {
		t.Fatal("missing key should not produce override")
	}

	// Cached miss should not error on repeat.
	_, ok = lookup(apikeys.KeyPrefix + "missing")
	if ok {
		t.Fatal("cached miss should stay false")
	}
}
