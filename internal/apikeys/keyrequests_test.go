package apikeys

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

func testKeyRequestStore(t *testing.T) *Store {
	t.Helper()
	store, _ := newFakeStore(t)
	return store
}

func TestCreateKeyRequestPendingLock(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	first, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "first",
		Description:    "first",
		DailyCostLimit: 1000,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.ID())

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "duplicate",
		Description:    "duplicate",
		DailyCostLimit: 1000,
	})
	assert.ErrorIs(t, err, ErrPendingKeyRequestExists)
}

func TestBeginCompleteKeyRequestApproval(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "editor@example.com",
		Provider:       "anthropic",
		Name:           "service-key",
		Description:    "service key",
		DailyCostLimit: 2000,
	})
	require.NoError(t, err)

	claimed, err := store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusApproving, claimed.Status)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "other@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotPending)

	completed, err := store.CompleteKeyRequestApproval(ctx, created.ID(), "sk-test-created-key", "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusApproved, completed.Status)
	assert.Equal(t, "sk-test-created-key", completed.CreatedKey)

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "editor@example.com",
		Provider:       "anthropic",
		Name:           "after-approval",
		Description:    "after approval",
		DailyCostLimit: 2000,
	})
	require.NoError(t, err)
}

func TestRejectKeyRequestReleasesLock(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "gemini",
		Name:           "please-reject",
		Description:    "please reject",
	})
	require.NoError(t, err)

	rejected, err := store.RejectKeyRequest(ctx, created.ID(), "admin@example.com", "not needed")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusRejected, rejected.Status)

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "gemini",
		Name:           "retry-after-reject",
		Description:    "retry after reject",
	})
	require.NoError(t, err)
}

func TestRollbackKeyRequestApproval(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "rollback-test",
		Description:    "rollback test",
	})
	require.NoError(t, err)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)

	require.NoError(t, store.RollbackKeyRequestApproval(ctx, created.ID(), "admin@example.com"))

	got, err := store.GetKeyRequest(ctx, created.ID())
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusPending, got.Status)
	assert.Empty(t, got.ReviewedBy)
}

func TestSlugifyKeyName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "Finch Worker", "finch-worker"},
		{"punctuation collapses", "my_service (prod)!!", "my-service-prod"},
		{"leading trailing trimmed", "--edge--", "edge"},
		{"all punctuation", "!!!", ""},
		{"truncated at max length with no trailing hyphen", strings.Repeat("a", 39) + " b", strings.Repeat("a", 39)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, SlugifyKeyName(tc.in))
			assert.LessOrEqual(t, len(SlugifyKeyName(tc.in)), MaxKeyRequestNameLen)
		})
	}
}

func TestCreateKeyRequestSlugifiesName(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "My Service (Prod)",
		Description:    "some description",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-service-prod", created.Name)
}

func TestCreateKeyRequestRejectsEmptyNameSlug(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	_, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "!!!",
		Description:    "some description",
	})
	assert.Error(t, err)
}

func createIntegrationKeyRequest(t *testing.T, store *Store, email, ticket string) *KeyRequest {
	t.Helper()
	req, err := store.CreateKeyRequest(context.Background(), CreateKeyRequestInput{
		RequesterEmail:    email,
		Provider:          "openai",
		Name:              "ticket-" + ticket,
		Description:       "service key for " + ticket,
		Source:            "init",
		ExternalTicketID:  ticket,
		ExternalTicketURL: "https://tickets.example.com/" + ticket,
		Justification:     "nightly batch job",
		MonthlyCostLimit:  50000,
	})
	require.NoError(t, err)
	return req
}

func auditEvents(req *KeyRequest) []string {
	out := make([]string, 0, len(req.Audit))
	for _, ev := range req.Audit {
		out = append(out, ev.Event+":"+ev.ActorType+":"+ev.Actor)
	}
	return out
}

func TestCreateKeyRequestStoresIntegrationFields(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created := createIntegrationKeyRequest(t, store, "Viewer@Example.com", "IH-321")

	got, err := store.GetKeyRequest(ctx, created.ID())
	require.NoError(t, err)
	assert.Equal(t, "init", got.Source)
	assert.Equal(t, "IH-321", got.ExternalTicketID)
	assert.Equal(t, "https://tickets.example.com/IH-321", got.ExternalTicketURL)
	assert.Equal(t, "nightly batch job", got.Justification)
	assert.Equal(t, int64(50000), got.MonthlyCostLimit)
	assert.Equal(t, []string{"created:integration:init"}, auditEvents(got))

	raw, err := store.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(store.tableName),
		Key:       map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: created.PK}},
	})
	require.NoError(t, err)
	assert.NotContains(t, raw.Item, "provider", "key requests must stay out of ProviderIndex")
	assert.NotContains(t, raw.Item, "owner_email", "key requests must stay out of OwnerProviderIndex")
}

func TestCreateKeyRequestDashboardAuditActorIsRequester(t *testing.T) {
	store := testKeyRequestStore(t)

	created, err := store.CreateKeyRequest(context.Background(), CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "dash",
		Description:    "dashboard request",
	})
	require.NoError(t, err)
	assert.Empty(t, created.Source)
	assert.Equal(t, []string{"created:user:viewer@example.com"}, auditEvents(created))
}

func TestCreateKeyRequestIntegrationRequiresTicket(t *testing.T) {
	store := testKeyRequestStore(t)

	_, err := store.CreateKeyRequest(context.Background(), CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "no-ticket",
		Description:    "no ticket",
		Source:         "init",
	})
	assert.Error(t, err)
}

func TestCreateKeyRequestTicketIdempotency(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	first := createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-1")

	replay, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail:   "someone-else@example.com",
		Provider:         "anthropic",
		Name:             "replayed",
		Description:      "replayed",
		Source:           "init",
		ExternalTicketID: "ih-1",
	})
	require.ErrorIs(t, err, ErrKeyRequestTicketExists)
	require.NotNil(t, replay)
	assert.Equal(t, first.ID(), replay.ID())

	byTicket, err := store.GetKeyRequestByExternalTicket(ctx, "init", "IH-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID(), byTicket.ID())

	_, err = store.GetKeyRequestByExternalTicket(ctx, "other-caller", "IH-1")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound)

	// A second ticket for the same requester and provider hits the pending
	// lock, and must not burn its ticket reservation.
	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail:   "viewer@example.com",
		Provider:         "openai",
		Name:             "second",
		Description:      "second",
		Source:           "init",
		ExternalTicketID: "IH-2",
	})
	require.ErrorIs(t, err, ErrPendingKeyRequestExists)
	_, err = store.GetKeyRequestByExternalTicket(ctx, "init", "IH-2")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound)

	_, err = store.CancelKeyRequest(ctx, first.ID(), "init")
	require.NoError(t, err)
	second := createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-2")
	assert.NotEqual(t, first.ID(), second.ID())
}

func TestKeyRequestLockItemsNeverListed(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-9")

	requests, err := store.ListKeyRequests(ctx, "")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.True(t, strings.HasPrefix(requests[0].PK, KeyRequestPrefix))

	keys, err := store.ListKeys(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestKeyRequestAuditTrail(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Name:           "audited",
		Description:    "audited",
	})
	require.NoError(t, err)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)
	require.NoError(t, store.RollbackKeyRequestApproval(ctx, created.ID(), "admin@example.com"))
	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "admin2@example.com")
	require.NoError(t, err)
	approved, err := store.CompleteKeyRequestApproval(ctx, created.ID(), "sk-test-key", "Admin2@Example.com")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"approval_rolled_back:user:admin@example.com",
		"approved:user:admin2@example.com",
	}, auditEvents(approved))
	for _, ev := range approved.Audit {
		assert.False(t, ev.At.IsZero())
	}

	rejectMe, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "gemini",
		Name:           "rejected",
		Description:    "rejected",
	})
	require.NoError(t, err)
	rejected, err := store.RejectKeyRequest(ctx, rejectMe.ID(), "admin@example.com", "no")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"created:user:viewer@example.com",
		"rejected:user:admin@example.com",
	}, auditEvents(rejected))
}

func TestCancelKeyRequest(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	_, err := store.CancelKeyRequest(ctx, "missing", "init")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound)

	dashboard, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "anthropic",
		Name:           "dash",
		Description:    "dashboard request",
	})
	require.NoError(t, err)
	_, err = store.CancelKeyRequest(ctx, dashboard.ID(), "init")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound, "integrations cannot cancel dashboard requests")
	_, err = store.CancelKeyRequest(ctx, dashboard.ID(), "")
	assert.Error(t, err)

	created := createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-5")
	_, err = store.CancelKeyRequest(ctx, created.ID(), "slack-app")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound, "a caller cannot cancel another caller's request")

	cancelled, err := store.CancelKeyRequest(ctx, created.ID(), "init")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusCancelled, cancelled.Status)
	assert.Equal(t, []string{"created:integration:init", "cancelled:integration:init"}, auditEvents(cancelled))

	_, err = store.CancelKeyRequest(ctx, created.ID(), "init")
	assert.ErrorIs(t, err, ErrKeyRequestNotPending)

	// The pending lock is released, so the requester can ask again.
	createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-6")

	// Replaying the cancelled ticket returns the cancelled request.
	replay, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail:   "viewer@example.com",
		Provider:         "openai",
		Name:             "again",
		Description:      "again",
		Source:           "init",
		ExternalTicketID: "IH-5",
	})
	require.ErrorIs(t, err, ErrKeyRequestTicketExists)
	assert.Equal(t, KeyRequestStatusCancelled, replay.Status)
}

func TestClaimKeyRequest(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	_, err := store.ClaimKeyRequest(ctx, "missing", "viewer@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound)

	created := createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-7")
	_, err = store.ClaimKeyRequest(ctx, created.ID(), "viewer@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotApproved)
	_, err = store.ResetKeyRequestClaim(ctx, created.ID(), "admin@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotApproved)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)
	_, err = store.CompleteKeyRequestApproval(ctx, created.ID(), "sk-test-key", "admin@example.com")
	require.NoError(t, err)

	_, err = store.ClaimKeyRequest(ctx, created.ID(), "intruder@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound, "a request that is not yours looks missing")
	_, err = store.ResetKeyRequestClaim(ctx, created.ID(), "admin@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotClaimed)

	claimed, err := store.ClaimKeyRequest(ctx, created.ID(), "Viewer@Example.com")
	require.NoError(t, err)
	require.NotNil(t, claimed.ClaimedAt)
	assert.Equal(t, "sk-test-key", claimed.CreatedKey)

	_, err = store.ClaimKeyRequest(ctx, created.ID(), "viewer@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestAlreadyClaimed)

	reset, err := store.ResetKeyRequestClaim(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)
	assert.Nil(t, reset.ClaimedAt)

	again, err := store.ClaimKeyRequest(ctx, created.ID(), "viewer@example.com")
	require.NoError(t, err)
	assert.NotNil(t, again.ClaimedAt)
	assert.Equal(t, []string{
		"created:integration:init",
		"approved:user:admin@example.com",
		"claimed:user:viewer@example.com",
		"claim_reset:user:admin@example.com",
		"claimed:user:viewer@example.com",
	}, auditEvents(again))
}

func TestCreateKeyRequestLogOmitsRequesterEmail(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	var logs bytes.Buffer
	store, err := NewStore(StoreConfig{
		TableName:       "test-keys",
		Region:          "us-west-2",
		AutoCreateTable: true,
		Logger:          slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	require.NoError(t, err)

	created := createIntegrationKeyRequest(t, store, "secret.person@example.com", "IH-11")

	out := logs.String()
	assert.Contains(t, out, "Created key request")
	assert.Contains(t, out, created.ID())
	assert.Contains(t, out, "source=init")
	assert.NotContains(t, out, "secret.person")
	assert.NotContains(t, out, "nightly batch job")
}

func TestCreateKeyRequestRejectsAmbiguousSource(t *testing.T) {
	store := testKeyRequestStore(t)

	for _, source := range []string{"a:b", "has space", strings.Repeat("a", 33)} {
		_, err := store.CreateKeyRequest(context.Background(), CreateKeyRequestInput{
			RequesterEmail:   "viewer@example.com",
			Provider:         "openai",
			Name:             "bad-source",
			Description:      "bad source",
			Source:           source,
			ExternalTicketID: "c",
		})
		assert.Error(t, err, source)
	}
}

func TestCreateKeyRequestFailedTransactionLeavesNoLocks(t *testing.T) {
	store, fake := newFakeStore(t)
	ctx := context.Background()

	fake.FailOnce("TransactWriteItems", errors.New("InternalServerError"))
	_, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail:   "viewer@example.com",
		Provider:         "openai",
		Name:             "will-fail",
		Description:      "will fail",
		Source:           "init",
		ExternalTicketID: "IH-50",
	})
	require.Error(t, err)

	_, err = store.GetKeyRequestByExternalTicket(ctx, "init", "IH-50")
	assert.ErrorIs(t, err, ErrKeyRequestNotFound, "no ticket reservation survives a failed create")

	retried := createIntegrationKeyRequest(t, store, "viewer@example.com", "IH-50")
	assert.Equal(t, KeyRequestStatusPending, retried.Status)
}
