package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseViewerEmails(t *testing.T) {
	got, err := parseViewerEmails(" A@Example.com, b@example.com ,a@example.com,,")
	require.NoError(t, err)
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, got)

	for _, bad := range []string{"", " , ", "not-an-email", "Name <a@example.com>", "a@example.com,nope"} {
		_, err := parseViewerEmails(bad)
		assert.Error(t, err, "input %q", bad)
	}
}

func TestViewersActionWrites(t *testing.T) {
	assert.False(t, viewersActionWrites(viewersList))
	assert.True(t, viewersActionWrites(viewersAdd))
	assert.True(t, viewersActionWrites(viewersRemove))
}

func TestExecuteViewers(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true})
	require.NoError(t, err)
	ctx := context.Background()
	key, err := store.CreateKey(ctx, "openai", "sk-upstream", "org key", 0, nil, nil)
	require.NoError(t, err)

	out := captureStdout(t, func() {
		require.NoError(t, executeViewers(ctx, store, viewersAdd, key.PK, []string{"a@example.com", "b@example.com"}))
	})
	assert.Contains(t, out, "a@example.com")
	assert.NotContains(t, out, key.PK, "output must only show the redacted key")

	out = captureStdout(t, func() {
		require.NoError(t, executeViewers(ctx, store, viewersRemove, key.PK, []string{"a@example.com"}))
	})
	assert.NotContains(t, out, "a@example.com")
	assert.Contains(t, out, "b@example.com")

	out = captureStdout(t, func() {
		require.NoError(t, executeViewers(ctx, store, viewersList, key.PK, nil))
	})
	assert.Contains(t, out, "Viewers:   b@example.com")

	assert.Error(t, executeViewers(ctx, store, viewersAdd, "sk-iw-missing", []string{"a@example.com"}))
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	require.NoError(t, w.Close())
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
