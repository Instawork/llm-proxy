package admin

import (
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

func testViewerHandler(t *testing.T) *handler {
	t.Helper()
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(t.Context(), "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)
	return h
}

func authenticatedViewerRequest(t *testing.T, h *handler, method, path string, body []byte) *http.Request {
	t.Helper()
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "viewer@example.com")
	return authenticatedRequest(t, h, method, path, body)
}

func TestHandleCreateKeyRequest(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "finch-worker staging",
	})
	req := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "pending", resp.Status)
	assert.Equal(t, "viewer@example.com", resp.RequesterEmail)
	assert.Equal(t, "openai", resp.Provider)
}

func TestHandleCreateKeyRequestRejectsInflatedDailyLimit(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:       "openai",
		Description:    "oversized budget",
		DailyCostLimit: 999999,
	})
	req := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateKeyRequestAdminRejected(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "admin should create directly",
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListMyKeyRequests(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "mine only",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	listReq := authenticatedViewerRequest(t, h, http.MethodGet, "/admin/api/key-requests/mine", nil)
	listRec := httptest.NewRecorder()
	h.handleListMyKeyRequests(listRec, listReq)
	assert.Equal(t, http.StatusOK, listRec.Code)

	var items []KeyRequestResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "viewer@example.com", items[0].RequesterEmail)
}

func TestHandleCreateKeyRequestDuplicatePending(t *testing.T) {
	h := testViewerHandler(t)
	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "first request",
	})

	req1 := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec1 := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec1, req1)
	require.Equal(t, http.StatusCreated, rec1.Code)

	req2 := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec2 := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec2, req2)
	assert.Equal(t, http.StatusConflict, rec2.Code)
}

func TestHandleListKeyRequestsAdmin(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "anthropic",
		Description: "service key please",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	listReq := authenticatedRequest(t, h, http.MethodGet, "/admin/api/key-requests?status=pending", nil)
	listRec := httptest.NewRecorder()
	h.handleListKeyRequests(listRec, listReq)
	assert.Equal(t, http.StatusOK, listRec.Code)

	var items []KeyRequestResponse
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "pending", items[0].Status)
}

func TestHandleApproveKeyRequest(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "approved-service",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	patchBody, _ := json.Marshal(ReviewKeyRequestBody{Action: "approve"})
	patchReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	assert.Equal(t, http.StatusOK, patchRec.Code)

	var approved KeyRequestResponse
	require.NoError(t, json.Unmarshal(patchRec.Body.Bytes(), &approved))
	assert.Equal(t, apikeys.KeyRequestStatusApproved, approved.Status)
	assert.NotEmpty(t, approved.CreatedKey)
}

func TestHandleRejectKeyRequest(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "gemini",
		Description: "reject me",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	patchBody, _ := json.Marshal(ReviewKeyRequestBody{
		Action:          "reject",
		RejectionReason: "use existing key",
	})
	patchReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	assert.Equal(t, http.StatusOK, patchRec.Code)

	var rejected KeyRequestResponse
	require.NoError(t, json.Unmarshal(patchRec.Body.Bytes(), &rejected))
	assert.Equal(t, apikeys.KeyRequestStatusRejected, rejected.Status)
	assert.Equal(t, "use existing key", rejected.RejectionReason)
}

func TestHandleReviewKeyRequestInvalidAction(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "invalid action test",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	patchBody, _ := json.Marshal(ReviewKeyRequestBody{Action: "maybe"})
	patchReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	assert.Equal(t, http.StatusBadRequest, patchRec.Code)
}

func TestHandleApproveKeyRequestTwice(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "double approve",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)

	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	patchBody, _ := json.Marshal(ReviewKeyRequestBody{Action: "approve"})
	patchReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code)

	patchReq2 := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq2 = mux.SetURLVars(patchReq2, map[string]string{"id": created.ID})
	patchRec2 := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec2, patchReq2)
	assert.Equal(t, http.StatusConflict, patchRec2.Code)
}
