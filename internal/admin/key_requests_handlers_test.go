package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/notify"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
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
	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/key-requests", body)
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

	listReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/key-requests?status=pending", nil)
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
	patchReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
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
	patchReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
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
	patchReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
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
	patchReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
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
	patchReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq = mux.SetURLVars(patchReq, map[string]string{"id": created.ID})
	patchRec := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code)

	patchReq2 := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+created.ID, patchBody)
	patchReq2 = mux.SetURLVars(patchReq2, map[string]string{"id": created.ID})
	patchRec2 := httptest.NewRecorder()
	h.handleReviewKeyRequest(patchRec2, patchReq2)
	assert.Equal(t, http.StatusConflict, patchRec2.Code)
}

func createViewerKeyRequest(t *testing.T, h *handler, provider, name string) KeyRequestResponse {
	t.Helper()
	body, _ := json.Marshal(CreateKeyRequestBody{Provider: provider, Name: name, Description: name})
	req := authenticatedViewerRequest(t, h, http.MethodPost, "/admin/api/key-requests", body)
	rec := httptest.NewRecorder()
	h.handleCreateKeyRequest(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	var created KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	return created
}

func createStoreIntegrationKeyRequest(t *testing.T, h *handler, requester, ticket string) *apikeys.KeyRequest {
	t.Helper()
	req, err := h.deps.APIKeyStore.CreateKeyRequest(t.Context(), apikeys.CreateKeyRequestInput{
		RequesterEmail:    requester,
		Provider:          "openai",
		Name:              "init-" + ticket,
		Description:       "requested in " + ticket,
		DailyCostLimit:    1500,
		MonthlyCostLimit:  50000,
		Source:            "init",
		ExternalTicketID:  ticket,
		ExternalTicketURL: "https://tickets.example.com/" + ticket,
		Justification:     "confidential justification text",
	})
	require.NoError(t, err)
	return req
}

func reviewKeyRequestAsAdmin(t *testing.T, h *handler, id string, body ReviewKeyRequestBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPatch, "/admin/api/key-requests/"+id, b)
	req = mux.SetURLVars(req, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.handleReviewKeyRequest(rec, req)
	return rec
}

func keyRequestAuditEvents(t *testing.T, h *handler, id string) []string {
	t.Helper()
	req, err := h.deps.APIKeyStore.GetKeyRequest(t.Context(), id)
	require.NoError(t, err)
	out := make([]string, 0, len(req.Audit))
	for _, ev := range req.Audit {
		out = append(out, ev.Event+":"+ev.ActorType+":"+ev.Actor)
	}
	return out
}

func TestHandleApproveKeyRequest_SelfApprovalForbidden(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")
	own := createStoreIntegrationKeyRequest(t, h, "Admin@Example.com", "IH-100")

	rec := reviewKeyRequestAsAdmin(t, h, own.ID(), ReviewKeyRequestBody{Action: "approve"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	got, err := h.deps.APIKeyStore.GetKeyRequest(t.Context(), own.ID())
	require.NoError(t, err)
	assert.Equal(t, apikeys.KeyRequestStatusPending, got.Status)
	assert.Empty(t, got.ReviewedBy)
	assert.Equal(t, []string{"created:integration:init"}, keyRequestAuditEvents(t, h, own.ID()))

	// Rejecting your own request stays allowed.
	rec = reviewKeyRequestAsAdmin(t, h, own.ID(), ReviewKeyRequestBody{Action: "reject"})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleApproveKeyRequest_MissingRequest(t *testing.T) {
	h := testViewerHandler(t)
	rec := reviewKeyRequestAsAdmin(t, h, "does-not-exist", ReviewKeyRequestBody{Action: "approve"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleApproveKeyRequest_LimitOverridesAndTags(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")
	created := createViewerKeyRequest(t, h, "openai", "override-limits")

	rec := reviewKeyRequestAsAdmin(t, h, created.ID, ReviewKeyRequestBody{
		Action:           "approve",
		DailyCostLimit:   new(int64(2500)),
		MonthlyCostLimit: new(int64(40000)),
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var approved KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &approved))

	key, err := h.deps.APIKeyStore.GetKey(t.Context(), approved.CreatedKey)
	require.NoError(t, err)
	assert.Equal(t, int64(2500), key.DailyCostLimit)
	assert.Equal(t, int64(40000), key.MonthlyCostLimit)
	assert.Equal(t, created.ID, key.Tags["key_request"])
	assert.Equal(t, "dashboard", key.Tags["source"])
}

func TestHandleApproveKeyRequest_IntegrationRequestLimitsAndTags(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")
	pending := createStoreIntegrationKeyRequest(t, h, "viewer@example.com", "IH-101")

	rec := reviewKeyRequestAsAdmin(t, h, pending.ID(), ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusOK, rec.Code)
	var approved KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &approved))

	key, err := h.deps.APIKeyStore.GetKey(t.Context(), approved.CreatedKey)
	require.NoError(t, err)
	assert.Equal(t, int64(1500), key.DailyCostLimit)
	assert.Equal(t, int64(50000), key.MonthlyCostLimit)
	assert.Equal(t, pending.ID(), key.Tags["key_request"])
	assert.Equal(t, "init", key.Tags["source"])
	assert.Equal(t, "viewer@example.com", key.RequesterEmail)
}

func TestHandleApproveKeyRequest_RejectsInvalidOverrides(t *testing.T) {
	cases := []struct {
		name string
		body ReviewKeyRequestBody
	}{
		{"daily above maximum", ReviewKeyRequestBody{Action: "approve", DailyCostLimit: new(int64(999999))}},
		{"negative daily", ReviewKeyRequestBody{Action: "approve", DailyCostLimit: new(int64(-1))}},
		{"negative monthly", ReviewKeyRequestBody{Action: "approve", MonthlyCostLimit: new(int64(-1))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testViewerHandler(t)
			withTestProvisioner(t, h, "openai")
			created := createViewerKeyRequest(t, h, "openai", "bad-override")

			rec := reviewKeyRequestAsAdmin(t, h, created.ID, tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)

			got, err := h.deps.APIKeyStore.GetKeyRequest(t.Context(), created.ID)
			require.NoError(t, err)
			assert.Equal(t, apikeys.KeyRequestStatusPending, got.Status)
			assert.Equal(t, []string{"created:user:viewer@example.com"}, keyRequestAuditEvents(t, h, created.ID),
				"invalid overrides are rejected before the approval starts")
		})
	}
}

func TestHandleReviewKeyRequest_AppendsAuditEvents(t *testing.T) {
	h := testViewerHandler(t)
	withTestProvisioner(t, h, "openai")

	approved := createViewerKeyRequest(t, h, "openai", "audit-approve")
	require.Equal(t, http.StatusOK, reviewKeyRequestAsAdmin(t, h, approved.ID, ReviewKeyRequestBody{Action: "approve"}).Code)
	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"approved:user:admin@example.com",
	}, keyRequestAuditEvents(t, h, approved.ID))

	rejected := createViewerKeyRequest(t, h, "anthropic", "audit-reject")
	require.Equal(t, http.StatusOK, reviewKeyRequestAsAdmin(t, h, rejected.ID, ReviewKeyRequestBody{Action: "reject"}).Code)
	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"rejected:user:admin@example.com",
	}, keyRequestAuditEvents(t, h, rejected.ID))

	// No provisioner for gemini, so createOrgKey fails and the approval rolls back.
	rolledBack := createViewerKeyRequest(t, h, "gemini", "audit-rollback")
	require.Equal(t, http.StatusBadRequest, reviewKeyRequestAsAdmin(t, h, rolledBack.ID, ReviewKeyRequestBody{Action: "approve"}).Code)
	got, err := h.deps.APIKeyStore.GetKeyRequest(t.Context(), rolledBack.ID)
	require.NoError(t, err)
	assert.Equal(t, apikeys.KeyRequestStatusPending, got.Status)
	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"approval_rolled_back:user:admin@example.com",
	}, keyRequestAuditEvents(t, h, rolledBack.ID))
}

func TestHandleListKeyRequests_IncludesTicketFields(t *testing.T) {
	h := testViewerHandler(t)
	pending := createStoreIntegrationKeyRequest(t, h, "viewer@example.com", "IH-102")

	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/key-requests", nil)
	rec := httptest.NewRecorder()
	h.handleListKeyRequests(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var items []KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, pending.ID(), items[0].ID)
	assert.Equal(t, "init", items[0].Source)
	assert.Equal(t, "IH-102", items[0].ExternalTicketID)
	assert.Equal(t, "https://tickets.example.com/IH-102", items[0].ExternalTicketURL)
	assert.Equal(t, "confidential justification text", items[0].Justification)
	assert.Equal(t, int64(50000), items[0].MonthlyCostLimit)
}

// hookProvisioner runs before() ahead of each provision, which lands between
// BeginKeyRequestApproval and CompleteKeyRequestApproval.
type hookProvisioner struct {
	*fakeProvisioner
	before func()
}

func (p *hookProvisioner) Provision(ctx context.Context, req provision.ProvisionRequest) (provision.Result, error) {
	p.before()
	return p.fakeProvisioner.Provision(ctx, req)
}

// Logs from the key request flow must never carry proxy keys, upstream keys,
// requester/reviewer emails, or justification text.
func TestKeyRequestFlow_LogsOmitKeysAndEmails(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h, sender := testViewerHandlerWithSender(t)

	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true, Logger: logger})
	require.NoError(t, err)
	h.deps.Logger = logger
	h.deps.APIKeyStore = store
	h.deps.Notifier = notify.NewNotifier(sender, h.deps.UserStore, nil, "", logger)

	failComplete := false
	h.deps.KeyProvisioner = provision.NewManager(logger, map[string]provision.Provisioner{
		"openai": &hookProvisioner{
			fakeProvisioner: &fakeProvisioner{key: "sk-upstream-secret-openai"},
			before: func() {
				if failComplete {
					fake.FailOnce("UpdateItem", errors.New("InternalServerError"))
				}
			},
		},
	})

	dashboard := createViewerKeyRequest(t, h, "openai", "log-dashboard")
	sender.waitForSend(t)
	rec := reviewKeyRequestAsAdmin(t, h, dashboard.ID, ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusOK, rec.Code)
	sender.waitForSend(t)
	var approved KeyRequestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &approved))
	require.NotEmpty(t, approved.CreatedKey)

	integration := createStoreIntegrationKeyRequest(t, h, "requester.person@example.com", "IH-103")
	failComplete = true
	rec = reviewKeyRequestAsAdmin(t, h, integration.ID(), ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	out := logs.String()
	require.Contains(t, out, "Created key request")
	require.Contains(t, out, "complete key request approval failed")
	assert.NotContains(t, out, approved.CreatedKey)
	assert.NotRegexp(t, regexp.MustCompile(`sk-iw-[0-9a-f]{9,}`), out, "full proxy keys must be redacted")
	assert.NotContains(t, out, "sk-upstream-secret")
	assert.NotContains(t, out, "viewer@example.com")
	assert.NotContains(t, out, "requester.person")
	assert.NotContains(t, out, "admin@example.com")
	assert.NotContains(t, out, "confidential justification")
}

// syncBuffer is a bytes.Buffer safe for the notifier's background sends.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
