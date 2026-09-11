package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendGrid_Send(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotBody sendGridRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	sg := NewSendGrid("test-api-key", "proxy@example.com", "LLM Proxy", srv.URL)
	err := sg.Send(context.Background(), Message{
		To:      []string{"user@example.com"},
		Subject: "Test subject",
		Text:    "Test body",
	})
	require.NoError(t, err)

	assert.Equal(t, "Bearer test-api-key", gotAuth)
	assert.Equal(t, "proxy@example.com", gotBody.From.Email)
	assert.Equal(t, "Test subject", gotBody.Subject)
	require.Len(t, gotBody.Personalizations, 1)
	require.Len(t, gotBody.Personalizations[0].To, 1)
	assert.Equal(t, "user@example.com", gotBody.Personalizations[0].To[0].Email)
	require.Len(t, gotBody.Content, 1)
	assert.Equal(t, "Test body", gotBody.Content[0].Value)
}

func TestSendGrid_Send_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bad key"}]}`))
	}))
	t.Cleanup(srv.Close)

	sg := NewSendGrid("bad-key", "proxy@example.com", "", srv.URL)
	err := sg.Send(context.Background(), Message{To: []string{"user@example.com"}, Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestSendGrid_Send_NoRecipients(t *testing.T) {
	t.Parallel()
	sg := NewSendGrid("key", "proxy@example.com", "", "http://unused.invalid")
	err := sg.Send(context.Background(), Message{Subject: "s", Text: "t"})
	require.Error(t, err)
}
