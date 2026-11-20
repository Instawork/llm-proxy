package main

import (
	"bytes"
	"encoding/hex"
	"regexp"
	"testing"

	"github.com/Instawork/llm-proxy/internal/integrationtoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTokenCommand_New(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := integrationTokenCommand([]string{"new", "-caller", "init"}, &stdout, &stderr)
	require.Equal(t, 0, code, stderr.String())

	token := regexp.MustCompile(`(?m)^Token:  (\S+)$`).FindStringSubmatch(stdout.String())
	sum := regexp.MustCompile(`(?m)^SHA256: ([0-9a-f]{64})$`).FindStringSubmatch(stdout.String())
	require.Len(t, token, 2, "output must contain a Token line")
	require.Len(t, sum, 2, "output must contain a SHA256 line")
	assert.Regexp(t, `^llmpi_init_[A-Za-z0-9_-]{43}$`, token[1])

	got := integrationtoken.Hash(token[1])
	assert.Equal(t, hex.EncodeToString(got[:]), sum[1])
	assert.Empty(t, stderr.String())
}

func TestIntegrationTokenCommand_Usage(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":  nil,
		"unknown sub":    {"rotate"},
		"missing caller": {"new"},
		"bad caller":     {"new", "-caller", "Init!"},
		"extra args":     {"new", "-caller", "init", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			assert.Equal(t, 2, integrationTokenCommand(args, &stdout, &stderr))
			assert.Empty(t, stdout.String(), "nothing, least of all a token, goes to stdout on error")
			assert.NotEmpty(t, stderr.String())
		})
	}
}
