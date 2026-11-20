package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// approvedViewerKeyRequest files a dashboard request as viewer@example.com and
// approves it as admin@example.com.
func approvedViewerKeyRequest(t *testing.T, h *handler, name string) string {
	t.Helper()
	withTestProvisioner(t, h, "openai")
	created := createViewerKeyRequest(t, h, "openai", name)
	rec := reviewKeyRequestAsAdmin(t, h, created.ID, ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusOK, rec.Code)
	return created.ID
}

func claimKeyRequestAs(t *testing.T, h *handler, email, id string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := authenticatedRequestAs(t, h, email, http.MethodPost, "/admin/api/key-requests/"+id+"/claim", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = mux.SetURLVars(req, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.handleClaimKeyRequest(rec, req)
	return rec
}

func resetKeyRequestClaimAsAdmin(t *testing.T, h *handler, id string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodDelete, "/admin/api/key-requests/"+id+"/claim", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = mux.SetURLVars(req, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.handleResetKeyRequestClaim(rec, req)
	return rec
}

func myKeyRequestsBody(t *testing.T, h *handler) string {
	t.Helper()
	req := authenticatedViewerRequest(t, h, http.MethodGet, "/admin/api/key-requests/mine", nil)
	rec := httptest.NewRecorder()
	h.handleListMyKeyRequests(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestClaimKeyRequest_OnlyRequesterOnlyOnce(t *testing.T) {
	h := testViewerHandler(t)
	_, err := h.deps.UserStore.CreateUser(t.Context(), "other@example.com", adminusers.RoleViewer)
	require.NoError(t, err)
	id := approvedViewerKeyRequest(t, h, "claim-once")
	key := storedCreatedKey(t, h, id)

	rec := claimKeyRequestAs(t, h, "other@example.com", id, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "someone else's request looks missing")
	assert.NotContains(t, rec.Body.String(), key)

	rec = claimKeyRequestAs(t, h, "viewer@example.com", id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var claimed ClaimKeyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &claimed))
	assert.Equal(t, key, claimed.Key)
	assert.Equal(t, apikeys.MaskKeyID(key), claimed.MaskedKeyID)
	assert.Equal(t, "openai", claimed.Provider)
	assert.Regexp(t, `/openai/v1$`, claimed.BaseURL)

	rec = claimKeyRequestAs(t, h, "viewer@example.com", id, nil)
	assert.Equal(t, http.StatusGone, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.NotContains(t, rec.Body.String(), key)
}

func TestClaimKeyRequest_BeforeApproval(t *testing.T) {
	h := testViewerHandler(t)
	created := createViewerKeyRequest(t, h, "openai", "not-yet")

	rec := claimKeyRequestAs(t, h, "viewer@example.com", created.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)

	rec = claimKeyRequestAs(t, h, "viewer@example.com", "missing", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestClaimKeyRequest_CrossSiteRejected(t *testing.T) {
	h := testViewerHandler(t)
	id := approvedViewerKeyRequest(t, h, "cross-site")

	rec := claimKeyRequestAs(t, h, "viewer@example.com", id, map[string]string{"Origin": "https://evil.example.net"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = claimKeyRequestAs(t, h, "viewer@example.com", id, map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Rejected attempts must not use up the claim.
	rec = claimKeyRequestAs(t, h, "viewer@example.com", id, map[string]string{"Origin": "http://example.com"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestResetKeyRequestClaim_AllowsOneMoreClaim(t *testing.T) {
	h := testViewerHandler(t)
	id := approvedViewerKeyRequest(t, h, "reset-claim")

	rec := resetKeyRequestClaimAsAdmin(t, h, id, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "nothing to reset before the first claim")

	require.Equal(t, http.StatusOK, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)

	rec = resetKeyRequestClaimAsAdmin(t, h, id, map[string]string{"Origin": "https://evil.example.net"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = resetKeyRequestClaimAsAdmin(t, h, id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Claimable)
	assert.NotContains(t, rec.Body.String(), storedCreatedKey(t, h, id))

	require.Equal(t, http.StatusOK, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)
	assert.Equal(t, http.StatusGone, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)

	assert.Equal(t, http.StatusNotFound, resetKeyRequestClaimAsAdmin(t, h, "missing", nil).Code)
	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"approved:user:admin@example.com",
		"claimed:user:viewer@example.com",
		"claim_reset:user:admin@example.com",
		"claimed:user:viewer@example.com",
	}, keyRequestAuditEvents(t, h, id))
}

func TestKeyRequestResponses_NeverCarryFullKey(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")
	created := createViewerKeyRequest(t, h, "openai", "masked-only")

	approveRec := reviewKeyRequestAsAdmin(t, h, created.ID, ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusOK, approveRec.Code)
	key := storedCreatedKey(t, h, created.ID)
	assert.NotContains(t, approveRec.Body.String(), key)
	assert.NotContains(t, approveRec.Body.String(), `"created_key"`)

	mine := myKeyRequestsBody(t, h)
	assert.NotContains(t, mine, key)
	assert.NotContains(t, mine, `"created_key"`)
	var items []KeyRequestResponse
	require.NoError(t, json.Unmarshal([]byte(mine), &items))
	require.Len(t, items, 1)
	assert.Equal(t, apikeys.MaskKeyID(key), items[0].CreatedKeyID)
	assert.True(t, items[0].Claimable)
	assert.Nil(t, items[0].ClaimedAt)

	listReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/key-requests", nil)
	listRec := httptest.NewRecorder()
	h.handleListKeyRequests(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	assert.NotContains(t, listRec.Body.String(), key)

	require.Equal(t, http.StatusOK, claimKeyRequestAs(t, h, "viewer@example.com", created.ID, nil).Code)
	require.NoError(t, json.Unmarshal([]byte(myKeyRequestsBody(t, h)), &items))
	assert.False(t, items[0].Claimable)
	assert.NotNil(t, items[0].ClaimedAt)
}

// Requests approved before claims existed carry created_key but no
// claimed_at or audit trail; their requester can still claim them once.
func TestClaimKeyRequest_LegacyApprovedRequest(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h := testViewerHandler(t)

	key, err := h.deps.APIKeyStore.CreateKey(t.Context(), "openai", "sk-upstream", "legacy", 0, nil, nil)
	require.NoError(t, err)
	pk := apikeys.KeyRequestPrefix + "legacy-1"
	fake.InjectItem("test-keys", pk, map[string]any{
		"pk":              map[string]any{"S": pk},
		"requester_email": map[string]any{"S": "viewer@example.com"},
		"req_provider":    map[string]any{"S": "openai"},
		"key_name":        map[string]any{"S": "legacy"},
		"description":     map[string]any{"S": "legacy request"},
		"status":          map[string]any{"S": apikeys.KeyRequestStatusApproved},
		"created_at":      map[string]any{"S": "2026-01-02T03:04:05Z"},
		"updated_at":      map[string]any{"S": "2026-01-02T03:04:05Z"},
		"reviewed_by":     map[string]any{"S": "admin@example.com"},
		"created_key":     map[string]any{"S": key.PK},
	})

	mine := myKeyRequestsBody(t, h)
	assert.NotContains(t, mine, key.PK)
	assert.Contains(t, mine, `"claimable":true`)

	rec := claimKeyRequestAs(t, h, "viewer@example.com", "legacy-1", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var claimed ClaimKeyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &claimed))
	assert.Equal(t, key.PK, claimed.Key)

	assert.Equal(t, http.StatusGone, claimKeyRequestAs(t, h, "viewer@example.com", "legacy-1", nil).Code)
}

func TestClaimRoutes_RoleGates(t *testing.T) {
	a := newRoutedAdmin(t)
	a.addUser("viewer@example.com", adminusers.RoleViewer)
	withTestProvisioner(t, a.h, "openai")
	created, err := a.store.CreateKeyRequest(t.Context(), apikeys.CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "routed",
		Description:    "routed",
	})
	require.NoError(t, err)
	_, err = a.store.BeginKeyRequestApproval(t.Context(), created.ID(), "admin@example.com")
	require.NoError(t, err)
	_, err = a.store.CompleteKeyRequestApproval(t.Context(), created.ID(), "sk-iw-routed-test-key", "admin@example.com")
	require.NoError(t, err)
	path := "/admin/api/key-requests/" + created.ID() + "/claim"

	rec := a.do("viewer@example.com", http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, "only admins may reset a claim")

	rec = a.do("viewer@example.com", http.MethodPost, path, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	rec = a.do("admin@example.com", http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// The claim and reset paths log the request id and a redacted key, never the
// key itself or anyone's email.
func TestClaimFlow_LogsOmitKeysAndEmails(t *testing.T) {
	h := testViewerHandler(t)
	id := approvedViewerKeyRequest(t, h, "claim-logs")
	key := storedCreatedKey(t, h, id)

	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true, Logger: logger})
	require.NoError(t, err)
	h.deps.Logger = logger
	h.deps.APIKeyStore = store

	require.Equal(t, http.StatusOK, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)
	require.Equal(t, http.StatusGone, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)
	require.Equal(t, http.StatusOK, resetKeyRequestClaimAsAdmin(t, h, id, nil).Code)
	require.Equal(t, http.StatusOK, claimKeyRequestAs(t, h, "viewer@example.com", id, nil).Code)

	out := logs.String()
	require.Contains(t, out, "admin: key request claimed")
	require.Contains(t, out, "admin: key request claim reset")
	assert.Contains(t, out, apikeys.RedactKey(key))
	assert.NotContains(t, out, key)
	assert.NotRegexp(t, regexp.MustCompile(`sk-iw-[0-9a-f]{9,}`), out)
	assert.NotContains(t, out, "viewer@example.com")
	assert.NotContains(t, out, "admin@example.com")
}
