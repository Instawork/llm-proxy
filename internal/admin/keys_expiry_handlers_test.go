package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleCreateKey_ExpiresAt_RoundTrips(t *testing.T) {
	h, _ := testAdminHandler(t)
	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:    "openai",
		ActualKey:   "sk-real",
		Description: "expiring key",
		ExpiresAt:   &expiresAt,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var resp KeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotNil(t, resp.ExpiresAt)
	assert.WithinDuration(t, expiresAt, *resp.ExpiresAt, time.Second)
}

func TestHandleCreateKey_ExpiresAt_RejectsPastDate(t *testing.T) {
	h, _ := testAdminHandler(t)
	past := time.Now().Add(-1 * time.Hour)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-real",
		ExpiresAt: &past,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateKey_ExpiresAt_RejectsBeyondMaxDays(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.APIKeyManagement.Expiry.MaxDays = 30
	tooFar := time.Now().Add(60 * 24 * time.Hour)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-real",
		ExpiresAt: &tooFar,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateKey_ExpiresAt_SetAndClear(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)

	future := time.Now().Add(24 * time.Hour).Truncate(time.Second).UTC()
	setBody, _ := json.Marshal(map[string]interface{}{"expires_at": future})
	setReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, setBody)
	setReq = mux.SetURLVars(setReq, map[string]string{"key": key.PK})
	setRec := httptest.NewRecorder()
	h.handleUpdateKey(setRec, setReq)
	require.Equal(t, http.StatusOK, setRec.Code, setRec.Body.String())

	updated, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	require.NotNil(t, updated.ExpiresAt)
	assert.WithinDuration(t, future, *updated.ExpiresAt, time.Second)

	clearBody := []byte(`{"expires_at": null}`)
	clearReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, clearBody)
	clearReq = mux.SetURLVars(clearReq, map[string]string{"key": key.PK})
	clearRec := httptest.NewRecorder()
	h.handleUpdateKey(clearRec, clearReq)
	require.Equal(t, http.StatusOK, clearRec.Code, clearRec.Body.String())

	cleared, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Nil(t, cleared.ExpiresAt)
}

func TestHandleUpdateKey_ExpiresAt_RejectsPastDate(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)

	body := []byte(`{"expires_at": "2000-01-01T00:00:00Z"}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateKey_ExpiresAt_ForbiddenForViewer(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	createBody, _ := json.Marshal(CreateKeyRequest{Provider: "bedrock", Description: "viewer bedrock"})
	createReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/keys", createBody)
	createRec := httptest.NewRecorder()
	h.handleCreateKey(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())
	var created KeyResponse
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	future := time.Now().Add(24 * time.Hour)
	body, _ := json.Marshal(map[string]interface{}{"expires_at": future})
	req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPatch, "/admin/api/keys/"+created.Key, body)
	req = mux.SetURLVars(req, map[string]string{"key": created.Key})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
