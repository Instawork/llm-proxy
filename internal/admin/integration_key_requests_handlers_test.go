package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	integrationRequester     = "requester.person@example.com"
	integrationJustification = "confidential justification text"
	integrationCreatePath    = "/admin/api/integrations/key-requests"
)

func validIntegrationBody(ticket string) IntegrationCreateKeyRequestBody {
	return IntegrationCreateKeyRequestBody{
		RequesterEmail:    integrationRequester,
		Provider:          "openai",
		Name:              "init " + ticket,
		Description:       "Key for the widget bot",
		Justification:     integrationJustification,
		DailyCostLimit:    1500,
		MonthlyCostLimit:  50000,
		ExternalTicketID:  ticket,
		ExternalTicketURL: "https://app.init.inc/tickets/" + ticket,
	}
}

func decodeIntegrationResponse(t *testing.T, body []byte) IntegrationKeyRequestResponse {
	t.Helper()
	var resp IntegrationKeyRequestResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp
}

func TestIntegrationCreateKeyRequest(t *testing.T) {
	f := newIntegrationFixture(t, nil)

	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	resp := decodeIntegrationResponse(t, rec.Body.Bytes())
	assert.Equal(t, apikeys.KeyRequestStatusPending, resp.Status)
	assert.Equal(t, "INIT-1", resp.ExternalTicketID)
	assert.Equal(t, int64(1500), resp.DailyCostLimit)
	assert.Equal(t, int64(50000), resp.MonthlyCostLimit)
	assert.Empty(t, resp.ClaimURL)
	assert.False(t, resp.Claimed)
	assert.Contains(t, resp.ReviewURL, "/admin/keys?tab=requests&request_id="+resp.ID)
	assert.NotContains(t, rec.Body.String(), integrationRequester)
	assert.NotContains(t, rec.Body.String(), integrationJustification)

	stored, err := f.h.deps.APIKeyStore.GetKeyRequest(t.Context(), resp.ID)
	require.NoError(t, err)
	assert.Equal(t, "init", stored.Source)
	assert.Equal(t, integrationRequester, stored.RequesterEmail)
	assert.Equal(t, integrationJustification, stored.Justification)
	assert.Equal(t, "https://app.init.inc/tickets/INIT-1", stored.ExternalTicketURL)
	assert.Equal(t, []string{"created:integration:init"}, keyRequestAuditEvents(t, f.h, resp.ID))

	f.sender.waitForSend(t)
	msgs := f.sender.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"admin@example.com"}, msgs[0].To)
	assert.Contains(t, msgs[0].Body, "Ticket: INIT-1\n")
	assert.Contains(t, msgs[0].Body, "Requested via: init\n")
	assert.Contains(t, msgs[0].Body, "https://llm.example.com/admin/keys?tab=requests&request_id="+resp.ID)
	assert.NotContains(t, msgs[0].Body, integrationJustification)
}

func TestIntegrationCreateKeyRequest_Validation(t *testing.T) {
	f := newIntegrationFixture(t, nil)

	cases := map[string]struct {
		mutate func(*IntegrationCreateKeyRequestBody)
		status int
	}{
		"requester outside domain": {func(b *IntegrationCreateKeyRequestBody) { b.RequesterEmail = "someone@evil.com" }, http.StatusBadRequest},
		"requester not an email":   {func(b *IntegrationCreateKeyRequestBody) { b.RequesterEmail = "someone" }, http.StatusBadRequest},
		"missing provider":         {func(b *IntegrationCreateKeyRequestBody) { b.Provider = "" }, http.StatusBadRequest},
		"unprovisioned provider":   {func(b *IntegrationCreateKeyRequestBody) { b.Provider = "anthropic" }, http.StatusBadRequest},
		"name without slug":        {func(b *IntegrationCreateKeyRequestBody) { b.Name = "!!!" }, http.StatusBadRequest},
		"missing description":      {func(b *IntegrationCreateKeyRequestBody) { b.Description = "  " }, http.StatusBadRequest},
		"description too long":     {func(b *IntegrationCreateKeyRequestBody) { b.Description = strings.Repeat("d", 501) }, http.StatusBadRequest},
		"justification too long":   {func(b *IntegrationCreateKeyRequestBody) { b.Justification = strings.Repeat("j", 2001) }, http.StatusBadRequest},
		"negative daily":           {func(b *IntegrationCreateKeyRequestBody) { b.DailyCostLimit = -1 }, http.StatusBadRequest},
		"negative monthly":         {func(b *IntegrationCreateKeyRequestBody) { b.MonthlyCostLimit = -1 }, http.StatusBadRequest},
		"daily over integration":   {func(b *IntegrationCreateKeyRequestBody) { b.DailyCostLimit = 4001 }, http.StatusBadRequest},
		"monthly over integration": {func(b *IntegrationCreateKeyRequestBody) { b.MonthlyCostLimit = 100001 }, http.StatusBadRequest},
		"missing ticket id":        {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketID = "" }, http.StatusBadRequest},
		"ticket id with spaces":    {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketID = "INIT 1" }, http.StatusBadRequest},
		"ticket id too long":       {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketID = strings.Repeat("A", 65) }, http.StatusBadRequest},
		"ticket url http":          {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketURL = "http://app.init.inc/t/1" }, http.StatusBadRequest},
		"ticket url other host":    {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketURL = "https://evil.example/t/1" }, http.StatusBadRequest},
		"ticket url subdomain":     {func(b *IntegrationCreateKeyRequestBody) { b.ExternalTicketURL = "https://x.app.init.inc/t/1" }, http.StatusBadRequest},
		"ticket url userinfo": {func(b *IntegrationCreateKeyRequestBody) {
			b.ExternalTicketURL = "https://app.init.inc@evil.example/t/1"
		}, http.StatusBadRequest},
		"body too large": {func(b *IntegrationCreateKeyRequestBody) { b.Justification = strings.Repeat("j", 17<<10) }, http.StatusRequestEntityTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := validIntegrationBody("INIT-V")
			tc.mutate(&body)
			rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, body)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "someone@evil.com")
		})
	}

	reqs, err := f.h.deps.APIKeyStore.ListKeyRequests(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, reqs, "no invalid request may be stored")
}

func TestIntegrationCreateKeyRequest_InvalidJSON(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, "not an object")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestIntegrationCreateKeyRequest_TicketURLRejectedWithoutHosts(t *testing.T) {
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) { ic.TicketURLHosts = "" })
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	body := validIntegrationBody("INIT-1")
	body.ExternalTicketURL = ""
	rec = f.do(f.initToken, http.MethodPost, integrationCreatePath, body)
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestIntegrationCreateKeyRequest_DefaultsLimitsToIntegrationMaxima(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	body := validIntegrationBody("INIT-1")
	body.DailyCostLimit = 0
	body.MonthlyCostLimit = 0
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	resp := decodeIntegrationResponse(t, rec.Body.Bytes())
	assert.Equal(t, int64(4000), resp.DailyCostLimit, "editor default 5000 clamped to integration max")
	assert.Equal(t, int64(100000), resp.MonthlyCostLimit)
}

func TestIntegrationCreateKeyRequest_ReplayReturnsExisting(t *testing.T) {
	f := newIntegrationFixture(t, nil)

	first := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, first.Code)
	f.sender.waitForSend(t)

	replay := validIntegrationBody("INIT-1")
	replay.Description = "a different description on retry"
	second := f.do(f.initToken, http.MethodPost, integrationCreatePath, replay)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, decodeIntegrationResponse(t, first.Body.Bytes()).ID, decodeIntegrationResponse(t, second.Body.Bytes()).ID)

	reqs, err := f.h.deps.APIKeyStore.ListKeyRequests(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, reqs, 1)
	assert.Len(t, f.sender.messages(), 1, "a replay must not email admins again")
}

func TestIntegrationCreateKeyRequest_SecondPendingTicketConflicts(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	require.Equal(t, http.StatusCreated, f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1")).Code)
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-2"))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestIntegrationKeyRequest_OtherCallersGet404(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, rec.Code)
	id := decodeIntegrationResponse(t, rec.Body.Bytes()).ID

	assert.Equal(t, http.StatusOK, f.do(f.initToken, http.MethodGet, integrationCreatePath+"/"+id, nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.otherToken, http.MethodGet, integrationCreatePath+"/"+id, nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.otherToken, http.MethodPost, integrationCreatePath+"/"+id+"/cancel", nil).Code)

	stored, err := f.h.deps.APIKeyStore.GetKeyRequest(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, apikeys.KeyRequestStatusPending, stored.Status)

	dashboard, err := f.h.deps.APIKeyStore.CreateKeyRequest(t.Context(), apikeys.CreateKeyRequestInput{
		RequesterEmail: "dash.user@example.com", Provider: "openai", Name: "dash", Description: "dash",
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, integrationCreatePath+"/"+dashboard.ID(), nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodPost, integrationCreatePath+"/"+dashboard.ID()+"/cancel", nil).Code)
}

func TestIntegrationKeyRequest_CancelOnlyWhilePending(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, rec.Code)
	id := decodeIntegrationResponse(t, rec.Body.Bytes()).ID

	rec = f.do(f.initToken, http.MethodPost, integrationCreatePath+"/"+id+"/cancel", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, apikeys.KeyRequestStatusCancelled, decodeIntegrationResponse(t, rec.Body.Bytes()).Status)

	assert.Equal(t, http.StatusConflict, f.do(f.initToken, http.MethodPost, integrationCreatePath+"/"+id+"/cancel", nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodPost, integrationCreatePath+"/missing/cancel", nil).Code)

	// The ticket can be filed again once its request is no longer pending.
	assert.Equal(t, http.StatusCreated, f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-2")).Code)
}

func TestIntegrationKeyRequest_StatusAfterApprovalOmitsSecrets(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, rec.Code)
	id := decodeIntegrationResponse(t, rec.Body.Bytes()).ID

	review := reviewKeyRequestAsAdmin(t, f.h, id, ReviewKeyRequestBody{Action: "approve"})
	require.Equal(t, http.StatusOK, review.Code, review.Body.String())

	rec = f.do(f.initToken, http.MethodGet, integrationCreatePath+"/"+id, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeIntegrationResponse(t, rec.Body.Bytes())
	assert.Equal(t, apikeys.KeyRequestStatusApproved, resp.Status)
	assert.NotNil(t, resp.ReviewedAt)
	assert.Contains(t, resp.ClaimURL, "/admin/keys?claim="+id)
	assert.False(t, resp.Claimed)

	raw := rec.Body.String()
	assert.NotContains(t, raw, "sk-")
	assert.NotContains(t, raw, "@example.com")
	assert.NotContains(t, raw, integrationJustification)
	assert.NotContains(t, raw, "created_key")
}

func TestKeyRequestApprovalLimits_IntegrationMaxima(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	h := f.h
	withTestProvisioner(t, h, "openai")
	integration := createStoreIntegrationKeyRequest(t, h, integrationRequester, "INIT-9")
	dashboard := &apikeys.KeyRequest{DailyCostLimit: 4500}

	over := int64(4500)
	_, _, err := h.keyRequestApprovalLimits(integration, ReviewKeyRequestBody{DailyCostLimit: &over})
	assert.Error(t, err, "daily over the integration maximum")
	_, _, err = h.keyRequestApprovalLimits(dashboard, ReviewKeyRequestBody{})
	assert.NoError(t, err, "dashboard requests keep the editor maximum only")

	noMonthly := int64(0)
	_, _, err = h.keyRequestApprovalLimits(integration, ReviewKeyRequestBody{MonthlyCostLimit: &noMonthly})
	assert.Error(t, err, "an integration request can't drop its monthly cap")
	bigMonthly := int64(100001)
	_, _, err = h.keyRequestApprovalLimits(integration, ReviewKeyRequestBody{MonthlyCostLimit: &bigMonthly})
	assert.Error(t, err)

	zero := int64(0)
	daily, monthly, err := h.keyRequestApprovalLimits(integration, ReviewKeyRequestBody{DailyCostLimit: &zero})
	require.NoError(t, err)
	assert.Equal(t, int64(4000), daily, "default daily clamped to the integration maximum")
	assert.Equal(t, int64(50000), monthly)

	rec := reviewKeyRequestAsAdmin(t, h, integration.ID(), ReviewKeyRequestBody{Action: "approve", DailyCostLimit: &over})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = reviewKeyRequestAsAdmin(t, h, integration.ID(), ReviewKeyRequestBody{Action: "approve"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// Integration logs must never carry the bearer token, its hash, requester or
// reviewer emails, or justification text.
func TestIntegrationKeyRequest_LogsOmitTokenEmailsAndJustification(t *testing.T) {
	f := newIntegrationFixture(t, nil)

	rec := f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1"))
	require.Equal(t, http.StatusCreated, rec.Code)
	id := decodeIntegrationResponse(t, rec.Body.Bytes()).ID
	f.sender.waitForSend(t)
	require.Equal(t, http.StatusOK, f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-1")).Code)
	require.Equal(t, http.StatusConflict, f.do(f.initToken, http.MethodPost, integrationCreatePath, validIntegrationBody("INIT-2")).Code)
	require.Equal(t, http.StatusOK, f.do(f.initToken, http.MethodGet, integrationCreatePath+"/"+id, nil).Code)
	require.Equal(t, http.StatusUnauthorized, f.do(f.initToken+"x", http.MethodGet, integrationCreatePath+"/"+id, nil).Code)
	bad := validIntegrationBody("INIT-3")
	bad.RequesterEmail = "outsider.person@evil.com"
	require.Equal(t, http.StatusBadRequest, f.do(f.initToken, http.MethodPost, integrationCreatePath, bad).Code)
	require.Equal(t, http.StatusOK, reviewKeyRequestAsAdmin(t, f.h, id, ReviewKeyRequestBody{Action: "approve"}).Code)
	f.sender.waitForSend(t)

	out := f.logs.String()
	require.Contains(t, out, "integration key request created")
	require.Contains(t, out, "integration request rejected")
	assert.NotContains(t, out, f.initToken)
	assert.NotContains(t, out, f.initToken[:20], "not even a token prefix beyond the caller id")
	assert.NotContains(t, out, f.initHash)
	assert.NotContains(t, out, "requester.person")
	assert.NotContains(t, out, "outsider.person")
	assert.NotContains(t, out, "admin@example.com")
	assert.NotContains(t, out, "confidential justification")
}
