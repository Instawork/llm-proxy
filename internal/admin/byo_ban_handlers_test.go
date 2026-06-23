package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleBYOBanLifecycle(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	masked := "sk-ant-…" + apikeys.CredentialHashSuffix("sk-ant-secret")
	_, err := store.BanBYOCredential(ctx, "anthropic", masked, "admin@example.com", "")
	require.NoError(t, err)

	listReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/byo-bans", nil)
	listRec := httptest.NewRecorder()
	h.handleListBYOBans(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)

	var listed []BYOBanResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, masked, listed[0].MaskedID)

	createBody, _ := json.Marshal(CreateBYOBanRequest{
		Provider: "openai",
		MaskedID: "sk-proj-…" + apikeys.CredentialHashSuffix("sk-proj-other"),
		Reason:   "policy",
	})
	createReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/byo-bans", createBody)
	createRec := httptest.NewRecorder()
	h.handleCreateBYOBan(createRec, createReq)
	assert.Equal(t, http.StatusCreated, createRec.Code)

	delReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodDelete, "/admin/api/byo-bans/anthropic/"+listed[0].Hash, nil)
	delReq = mux.SetURLVars(delReq, map[string]string{"provider": "anthropic", "hash": listed[0].Hash})
	delRec := httptest.NewRecorder()
	h.handleDeleteBYOBan(delRec, delReq)
	assert.Equal(t, http.StatusNoContent, delRec.Code)

	banned, err := store.IsBYOCredentialBanned(ctx, "anthropic", listed[0].Hash)
	require.NoError(t, err)
	assert.False(t, banned)
}

func TestHandleBYOBan_NonAdminForbidden(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	body, _ := json.Marshal(CreateBYOBanRequest{
		Provider: "anthropic",
		MaskedID: "sk-ant-…" + apikeys.CredentialHashSuffix("sk-ant-x"),
	})

	cases := []struct {
		name    string
		email   string
		method  string
		path    string
		body    []byte
		handler http.HandlerFunc
	}{
		{
			name:    "viewer list",
			email:   "viewer@example.com",
			method:  http.MethodGet,
			path:    "/admin/api/byo-bans",
			handler: h.handleListBYOBans,
		},
		{
			name:    "editor list",
			email:   "editor@example.com",
			method:  http.MethodGet,
			path:    "/admin/api/byo-bans",
			handler: h.handleListBYOBans,
		},
		{
			name:    "viewer create",
			email:   "viewer@example.com",
			method:  http.MethodPost,
			path:    "/admin/api/byo-bans",
			body:    body,
			handler: h.handleCreateBYOBan,
		},
		{
			name:    "editor create",
			email:   "editor@example.com",
			method:  http.MethodPost,
			path:    "/admin/api/byo-bans",
			body:    body,
			handler: h.handleCreateBYOBan,
		},
		{
			name:    "editor delete",
			email:   "editor@example.com",
			method:  http.MethodDelete,
			path:    "/admin/api/byo-bans/anthropic/00000000",
			handler: h.handleDeleteBYOBan,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := authenticatedRequestAs(t, h, tc.email, tc.method, tc.path, tc.body)
			if tc.method == http.MethodDelete {
				req = mux.SetURLVars(req, map[string]string{"provider": "anthropic", "hash": "00000000"})
			}
			rec := httptest.NewRecorder()
			roleHandler(h.auth, adminusers.RoleAdmin, tc.handler).ServeHTTP(rec, req)
			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}
