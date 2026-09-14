package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/notify"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSender records sent messages for assertions and never touches the network.
type fakeSender struct {
	mu   sync.Mutex
	sent []notify.Notification
	sig  chan struct{}
}

func newFakeSender() *fakeSender {
	return &fakeSender{sig: make(chan struct{}, 16)}
}

func (m *fakeSender) Send(_ context.Context, msg notify.Notification) error {
	m.mu.Lock()
	m.sent = append(m.sent, msg)
	m.mu.Unlock()
	m.sig <- struct{}{}
	return nil
}

func (m *fakeSender) waitForSend(t *testing.T) {
	t.Helper()
	select {
	case <-m.sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for email send")
	}
}

func (m *fakeSender) messages() []notify.Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]notify.Notification, len(m.sent))
	copy(out, m.sent)
	return out
}

// testViewerHandlerWithSender builds a viewer-authenticated handler wired to
// a fake sender, so KeyRequested/KeyRequestApproved emails can be asserted.
func testViewerHandlerWithSender(t *testing.T) (*handler, *fakeSender) {
	t.Helper()
	h := testViewerHandler(t)
	sender := newFakeSender()
	h.deps.Notifier = notify.NewNotifier(sender, h.deps.UserStore, nil, "", h.deps.Logger)
	return h, sender
}

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
		Name:        "finch-worker",
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
	assert.Equal(t, "finch-worker", resp.Name)
}

func TestHandleCreateKeyRequestMissingName(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Description: "finch-worker staging",
	})
	req := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateKeyRequestRejectsInflatedDailyLimit(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:       "openai",
		Name:           "oversized-budget",
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
		Name:        "admin-direct",
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
		Name:        "mine-only",
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
		Name:        "first-request",
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
		Name:        "service-key",
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
		Name:        "approved-service",
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

	key, err := h.deps.APIKeyStore.GetKey(patchReq.Context(), approved.CreatedKey)
	require.NoError(t, err)
	assert.Equal(t, "approved-service", key.Description)
}

func TestHandleCreateKeyRequest_NotifiesAdmins(t *testing.T) {
	h, sender := testViewerHandlerWithSender(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Name:        "notify-admins",
		Description: "notify admins on request",
	})
	req := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	sender.waitForSend(t)
	msgs := sender.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"admin@example.com"}, msgs[0].To)
	assert.Contains(t, msgs[0].Title, "viewer@example.com")
}

func TestHandleApproveKeyRequest_NotifiesRequesterAndPersistsEmail(t *testing.T) {
	h, sender := testViewerHandlerWithSender(t)
	withTestProvisioner(t, h, "openai")

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "openai",
		Name:        "notify-approval",
		Description: "notify-approval",
	})
	createReq := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKeyRequest(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code)
	sender.waitForSend(t) // drain the KeyRequested admin notification

	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &created))

	patchBody, _ := json.Marshal(ReviewKeyRequestBody{Action: "approve"})
	patchReq := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code)

	var approved KeyRequestResponse
	require.NoError(t, json.Unmarshal(patchRec.Body.Bytes(), &approved))

	sender.waitForSend(t)
	msgs := sender.messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, []string{"viewer@example.com"}, msgs[1].To)

	key, err := h.deps.APIKeyStore.GetKey(patchReq.Context(), approved.CreatedKey)
	require.NoError(t, err)
	assert.Equal(t, "viewer@example.com", key.RequesterEmail)
}

func TestHandleRejectKeyRequest(t *testing.T) {
	h := testViewerHandler(t)

	body, _ := json.Marshal(CreateKeyRequestBody{
		Provider:    "gemini",
		Name:        "reject-me",
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
		Name:        "invalid-action-test",
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
		Name:        "double-approve",
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
