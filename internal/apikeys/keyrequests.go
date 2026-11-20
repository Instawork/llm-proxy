package apikeys

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const KeyRequestPrefix = "keyreq:"

// MaxKeyRequestNameLen is the maximum length of a slugified key request name.
const MaxKeyRequestNameLen = 40

const (
	KeyRequestStatusPending   = "pending"
	KeyRequestStatusApproving = "approving"
	KeyRequestStatusApproved  = "approved"
	KeyRequestStatusRejected  = "rejected"
	KeyRequestStatusCancelled = "cancelled"
)

// Audit event names recorded on KeyRequest.Audit.
const (
	KeyRequestEventCreated            = "created"
	KeyRequestEventApproved           = "approved"
	KeyRequestEventApprovalRolledBack = "approval_rolled_back"
	KeyRequestEventRejected           = "rejected"
	KeyRequestEventCancelled          = "cancelled"
	KeyRequestEventClaimed            = "claimed"
	KeyRequestEventClaimReset         = "claim_reset"
)

// Audit actor types: a dashboard user (actor is an email) or an integration
// caller (actor is the caller id, which is also KeyRequest.Source).
const (
	KeyRequestActorUser        = "user"
	KeyRequestActorIntegration = "integration"
)

var (
	ErrKeyRequestNotFound       = errors.New("key request not found")
	ErrPendingKeyRequestExists  = errors.New("a pending request already exists for this provider")
	ErrKeyRequestNotPending     = errors.New("key request is not pending")
	ErrKeyRequestTicketExists   = errors.New("a key request already exists for this ticket")
	ErrKeyRequestNotApproved    = errors.New("key request is not approved")
	ErrKeyRequestAlreadyClaimed = errors.New("key request has already been claimed")
	ErrKeyRequestNotClaimed     = errors.New("key request has not been claimed")

	errKeyRequestConditionFailed = errors.New("key request condition failed")
)

// KeyRequest is a request for an org-wide service API key, submitted from the
// dashboard (Source == "") or by an integration caller (Source == caller id).
//
// Never add an attribute named "provider" or "owner_email": the item would
// then be projected into ProviderIndex/OwnerProviderIndex next to real keys.
type KeyRequest struct {
	PK                string            `dynamodbav:"pk"`
	RequesterEmail    string            `dynamodbav:"requester_email"`
	Provider          string            `dynamodbav:"req_provider"`
	Name              string            `dynamodbav:"key_name,omitempty"`
	Description       string            `dynamodbav:"description"`
	DailyCostLimit    int64             `dynamodbav:"daily_cost_limit,omitempty"`
	MonthlyCostLimit  int64             `dynamodbav:"monthly_cost_limit,omitempty"`
	Status            string            `dynamodbav:"status"`
	CreatedAt         time.Time         `dynamodbav:"created_at"`
	UpdatedAt         time.Time         `dynamodbav:"updated_at"`
	ReviewedBy        string            `dynamodbav:"reviewed_by,omitempty"`
	ReviewedAt        *time.Time        `dynamodbav:"reviewed_at,omitempty"`
	CreatedKey        string            `dynamodbav:"created_key,omitempty"`
	RejectionReason   string            `dynamodbav:"rejection_reason,omitempty"`
	Source            string            `dynamodbav:"source,omitempty"`
	ExternalTicketID  string            `dynamodbav:"external_ticket_id,omitempty"`
	ExternalTicketURL string            `dynamodbav:"external_ticket_url,omitempty"`
	Justification     string            `dynamodbav:"justification,omitempty"`
	ClaimedAt         *time.Time        `dynamodbav:"claimed_at,omitempty"`
	Audit             []KeyRequestEvent `dynamodbav:"audit,omitempty"`
}

// KeyRequestEvent is one state change in a key request's audit trail. Events
// are appended in the same UpdateItem as the transition they record.
type KeyRequestEvent struct {
	At        time.Time `dynamodbav:"at"`
	Event     string    `dynamodbav:"event"`
	ActorType string    `dynamodbav:"actor_type"`
	Actor     string    `dynamodbav:"actor"`
}

func (k *KeyRequest) ID() string {
	return strings.TrimPrefix(k.PK, KeyRequestPrefix)
}

// CreateKeyRequestInput is the data needed to submit a new key request.
// Source is empty for dashboard requests; integration requests set it to the
// caller id and must carry an ExternalTicketID.
type CreateKeyRequestInput struct {
	RequesterEmail    string
	Provider          string
	Name              string
	Description       string
	DailyCostLimit    int64
	MonthlyCostLimit  int64
	Source            string
	ExternalTicketID  string
	ExternalTicketURL string
	Justification     string
}

// SlugifyKeyName makes a short, url/tag-safe slug from a free-form key name.
// It lowercases, keeps only a-z0-9, collapses any other run of characters
// into a single hyphen, trims leading/trailing hyphens, and caps the length
// at MaxKeyRequestNameLen.
func SlugifyKeyName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > MaxKeyRequestNameLen {
		out = strings.TrimRight(out[:MaxKeyRequestNameLen], "-")
	}
	return out
}

func keyRequestPendingLockPK(requesterEmail, provider string) string {
	return "keyreq-pending:" + normalizeOwnerEmail(requesterEmail) + ":" + normalizeProvider(provider)
}

func (s *Store) acquireKeyRequestPendingLock(ctx context.Context, requesterEmail, provider string) error {
	lockPK := keyRequestPendingLockPK(requesterEmail, provider)
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: lockPK},
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return ErrPendingKeyRequestExists
		}
		return fmt.Errorf("failed to acquire key request lock: %w", err)
	}
	return nil
}

func (s *Store) releaseKeyRequestPendingLock(ctx context.Context, requesterEmail, provider string) {
	_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: keyRequestPendingLockPK(requesterEmail, provider)},
		},
	})
}

// keyRequestTicketLockPK maps an integration ticket to the request it opened.
// The "keyreq-ext:" prefix does not match "keyreq:" or the key-list scan
// prefixes, so these items never appear as requests or keys.
func keyRequestTicketLockPK(source, ticketID string) string {
	return "keyreq-ext:" + strings.ToLower(strings.TrimSpace(source)) + ":" + strings.ToLower(strings.TrimSpace(ticketID))
}

func (s *Store) acquireKeyRequestTicketLock(ctx context.Context, source, ticketID, requestID string) error {
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item: map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: keyRequestTicketLockPK(source, ticketID)},
			"request_id": &types.AttributeValueMemberS{Value: requestID},
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return ErrKeyRequestTicketExists
		}
		return fmt.Errorf("failed to acquire key request ticket lock: %w", err)
	}
	return nil
}

func (s *Store) releaseKeyRequestTicketLock(ctx context.Context, source, ticketID string) {
	_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: keyRequestTicketLockPK(source, ticketID)},
		},
	})
}

// CreateKeyRequest stores a pending org key request.
//
// When in.Source is set, the (source, ticket) pair is reserved first. If that
// ticket already opened a request, CreateKeyRequest returns the existing
// request together with ErrKeyRequestTicketExists so callers can replay it.
func (s *Store) CreateKeyRequest(ctx context.Context, in CreateKeyRequestInput) (*KeyRequest, error) {
	in.RequesterEmail = strings.TrimSpace(strings.ToLower(in.RequesterEmail))
	in.Provider = strings.TrimSpace(strings.ToLower(in.Provider))
	in.Description = strings.TrimSpace(in.Description)
	in.Name = SlugifyKeyName(in.Name)
	in.Source = strings.TrimSpace(strings.ToLower(in.Source))
	in.ExternalTicketID = strings.TrimSpace(in.ExternalTicketID)
	in.ExternalTicketURL = strings.TrimSpace(in.ExternalTicketURL)
	in.Justification = strings.TrimSpace(in.Justification)
	if in.RequesterEmail == "" || in.Provider == "" || in.Description == "" {
		return nil, fmt.Errorf("requester_email, provider, and description are required")
	}
	if in.Name == "" {
		return nil, fmt.Errorf("name must contain at least one letter or number")
	}
	if in.Source != "" && in.ExternalTicketID == "" {
		return nil, fmt.Errorf("external_ticket_id is required for integration requests")
	}

	id, err := newUUIDv4()
	if err != nil {
		return nil, err
	}

	if in.Source != "" {
		if err := s.acquireKeyRequestTicketLock(ctx, in.Source, in.ExternalTicketID, id); err != nil {
			if !errors.Is(err, ErrKeyRequestTicketExists) {
				return nil, err
			}
			existing, getErr := s.GetKeyRequestByExternalTicket(ctx, in.Source, in.ExternalTicketID)
			if getErr != nil {
				return nil, getErr
			}
			return existing, ErrKeyRequestTicketExists
		}
	}
	releaseTicket := func() {
		if in.Source != "" {
			s.releaseKeyRequestTicketLock(ctx, in.Source, in.ExternalTicketID)
		}
	}

	if err := s.acquireKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider); err != nil {
		releaseTicket()
		return nil, err
	}
	releaseLocks := func() {
		s.releaseKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider)
		releaseTicket()
	}

	actorType, actor := KeyRequestActorUser, in.RequesterEmail
	if in.Source != "" {
		actorType, actor = KeyRequestActorIntegration, in.Source
	}
	now := time.Now()
	req := &KeyRequest{
		PK:                KeyRequestPrefix + id,
		RequesterEmail:    in.RequesterEmail,
		Provider:          in.Provider,
		Name:              in.Name,
		Description:       in.Description,
		DailyCostLimit:    in.DailyCostLimit,
		MonthlyCostLimit:  in.MonthlyCostLimit,
		Status:            KeyRequestStatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
		Source:            in.Source,
		ExternalTicketID:  in.ExternalTicketID,
		ExternalTicketURL: in.ExternalTicketURL,
		Justification:     in.Justification,
		Audit: []KeyRequestEvent{{
			At:        now,
			Event:     KeyRequestEventCreated,
			ActorType: actorType,
			Actor:     actor,
		}},
	}

	av, err := attributevalue.MarshalMap(req)
	if err != nil {
		releaseLocks()
		return nil, fmt.Errorf("failed to marshal key request: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		releaseLocks()
		return nil, fmt.Errorf("failed to create key request: %w", err)
	}

	s.logger.Info("Created key request",
		"id", id,
		"source", in.Source,
		"provider", in.Provider)

	return req, nil
}

// GetKeyRequestByExternalTicket loads the request an integration caller
// opened for a ticket. The ticket ID is matched case-insensitively.
func (s *Store) GetKeyRequestByExternalTicket(ctx context.Context, source, ticketID string) (*KeyRequest, error) {
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: keyRequestTicketLockPK(source, ticketID)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get key request ticket: %w", err)
	}
	ref, ok := result.Item["request_id"].(*types.AttributeValueMemberS)
	if !ok || ref.Value == "" {
		return nil, ErrKeyRequestNotFound
	}
	return s.GetKeyRequest(ctx, ref.Value)
}

// ListKeyRequests returns key requests, optionally filtered by status.
func (s *Store) ListKeyRequests(ctx context.Context, status string) ([]*KeyRequest, error) {
	status = strings.TrimSpace(strings.ToLower(status))
	filter := "begins_with(pk, :pfx)"
	values := map[string]types.AttributeValue{
		":pfx": &types.AttributeValueMemberS{Value: KeyRequestPrefix},
	}
	if status != "" {
		filter += " AND #status = :status"
		values[":status"] = &types.AttributeValueMemberS{Value: status}
	}
	return s.listKeyRequests(ctx, filter, values)
}

// ListKeyRequestsForRequester returns all requests submitted by a user.
func (s *Store) ListKeyRequestsForRequester(ctx context.Context, requesterEmail string) ([]*KeyRequest, error) {
	requesterEmail = strings.TrimSpace(strings.ToLower(requesterEmail))
	return s.listKeyRequests(ctx, "begins_with(pk, :pfx) AND requester_email = :email", map[string]types.AttributeValue{
		":pfx":   &types.AttributeValueMemberS{Value: KeyRequestPrefix},
		":email": &types.AttributeValueMemberS{Value: requesterEmail},
	})
}

func (s *Store) listKeyRequests(ctx context.Context, filterExpr string, values map[string]types.AttributeValue) ([]*KeyRequest, error) {
	scanInput := &dynamodb.ScanInput{
		TableName:                 aws.String(s.tableName),
		FilterExpression:          aws.String(filterExpr),
		ExpressionAttributeValues: values,
	}
	if strings.Contains(filterExpr, "#status") {
		scanInput.ExpressionAttributeNames = map[string]string{"#status": "status"}
	}

	var requests []*KeyRequest
	for {
		result, err := s.client.Scan(ctx, scanInput)
		if err != nil {
			return nil, fmt.Errorf("failed to list key requests: %w", err)
		}
		for _, item := range result.Items {
			var req KeyRequest
			if err := attributevalue.UnmarshalMap(item, &req); err != nil {
				s.logger.Warn("Failed to unmarshal key request", "error", err)
				continue
			}
			requests = append(requests, &req)
		}
		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		scanInput.ExclusiveStartKey = result.LastEvaluatedKey
	}

	sort.Slice(requests, func(i, j int) bool {
		return requests[i].CreatedAt.After(requests[j].CreatedAt)
	})
	return requests, nil
}

// GetKeyRequest loads a key request by ID.
func (s *Store) GetKeyRequest(ctx context.Context, id string) (*KeyRequest, error) {
	id = strings.TrimPrefix(id, KeyRequestPrefix)
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + id},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get key request: %w", err)
	}
	if result.Item == nil {
		return nil, ErrKeyRequestNotFound
	}

	var req KeyRequest
	if err := attributevalue.UnmarshalMap(result.Item, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal key request: %w", err)
	}
	return &req, nil
}

// keyRequestUpdate is one conditional state change on a key request item.
// Every condition references #status, which updateKeyRequest always binds.
type keyRequestUpdate struct {
	set       []string
	remove    []string
	condition string
	names     map[string]string
	values    map[string]types.AttributeValue
	// event, when set, is appended to the audit list in the same UpdateItem
	// so a transition and its audit record can never diverge.
	event *KeyRequestEvent
}

// updateKeyRequest applies u and returns the updated item, or
// errKeyRequestConditionFailed when u.condition does not hold.
func (s *Store) updateKeyRequest(ctx context.Context, id string, now time.Time, u keyRequestUpdate) (*KeyRequest, error) {
	updatedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated_at: %w", err)
	}
	set := append(slices.Clone(u.set), "updated_at = :updated")
	names := map[string]string{"#status": "status"}
	values := map[string]types.AttributeValue{":updated": updatedAt}
	if u.event != nil {
		ev, err := attributevalue.Marshal([]KeyRequestEvent{*u.event})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal audit event: %w", err)
		}
		set = append(set, "#audit = list_append(if_not_exists(#audit, :empty), :ev)")
		names["#audit"] = "audit"
		values[":ev"] = ev
		values[":empty"] = &types.AttributeValueMemberL{Value: []types.AttributeValue{}}
	}
	maps.Copy(names, u.names)
	maps.Copy(values, u.values)

	expr := "SET " + strings.Join(set, ", ")
	if len(u.remove) > 0 {
		expr += " REMOVE " + strings.Join(u.remove, ", ")
	}
	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + strings.TrimPrefix(id, KeyRequestPrefix)},
		},
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String(u.condition),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
			return nil, errKeyRequestConditionFailed
		}
		return nil, err
	}

	var req KeyRequest
	if err := attributevalue.UnmarshalMap(result.Attributes, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal key request: %w", err)
	}
	return &req, nil
}

func userEvent(event, email string, at time.Time) *KeyRequestEvent {
	return &KeyRequestEvent{
		At:        at,
		Event:     event,
		ActorType: KeyRequestActorUser,
		Actor:     strings.ToLower(strings.TrimSpace(email)),
	}
}

func stringAV(v string) types.AttributeValue {
	return &types.AttributeValueMemberS{Value: v}
}

// notPendingOrMissing distinguishes a missing request from one that has
// already left the pending state after a failed "#status = :pending" update.
func (s *Store) notPendingOrMissing(ctx context.Context, id string) error {
	if _, err := s.GetKeyRequest(ctx, id); errors.Is(err, ErrKeyRequestNotFound) {
		return ErrKeyRequestNotFound
	}
	return ErrKeyRequestNotPending
}

// BeginKeyRequestApproval atomically claims a pending request for approval.
// It records no audit event: the outcome (approved or approval_rolled_back)
// always follows and carries the reviewer.
func (s *Store) BeginKeyRequestApproval(ctx context.Context, id, reviewedBy string) (*KeyRequest, error) {
	now := time.Now()
	reviewedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal reviewed_at: %w", err)
	}
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"#status = :approving", "reviewed_by = :by", "reviewed_at = :at"},
		condition: "#status = :pending",
		values: map[string]types.AttributeValue{
			":pending":   stringAV(KeyRequestStatusPending),
			":approving": stringAV(KeyRequestStatusApproving),
			":by":        stringAV(reviewedBy),
			":at":        reviewedAt,
		},
	})
	if err != nil {
		if errors.Is(err, errKeyRequestConditionFailed) {
			return nil, s.notPendingOrMissing(ctx, id)
		}
		return nil, fmt.Errorf("failed to begin key request approval: %w", err)
	}
	return req, nil
}

// CompleteKeyRequestApproval marks an approving request approved with the
// created key and records an "approved" event for actor.
func (s *Store) CompleteKeyRequestApproval(ctx context.Context, id, createdKey, actor string) (*KeyRequest, error) {
	now := time.Now()
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"#status = :approved", "created_key = :key"},
		condition: "#status = :approving",
		values: map[string]types.AttributeValue{
			":approving": stringAV(KeyRequestStatusApproving),
			":approved":  stringAV(KeyRequestStatusApproved),
			":key":       stringAV(createdKey),
		},
		event: userEvent(KeyRequestEventApproved, actor, now),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to complete key request approval: %w", err)
	}

	s.releaseKeyRequestPendingLock(ctx, req.RequesterEmail, req.Provider)
	return req, nil
}

// RollbackKeyRequestApproval returns an approving request to pending and
// records an "approval_rolled_back" event for actor.
func (s *Store) RollbackKeyRequestApproval(ctx context.Context, id, actor string) error {
	now := time.Now()
	_, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"#status = :pending"},
		remove:    []string{"reviewed_by", "reviewed_at", "created_key"},
		condition: "#status = :approving",
		values: map[string]types.AttributeValue{
			":approving": stringAV(KeyRequestStatusApproving),
			":pending":   stringAV(KeyRequestStatusPending),
		},
		event: userEvent(KeyRequestEventApprovalRolledBack, actor, now),
	})
	if err != nil {
		return fmt.Errorf("failed to rollback key request approval: %w", err)
	}
	return nil
}

// RejectKeyRequest marks a request rejected with an optional reason.
func (s *Store) RejectKeyRequest(ctx context.Context, id, reviewedBy, reason string) (*KeyRequest, error) {
	now := time.Now()
	reviewedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal reviewed_at: %w", err)
	}
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"#status = :rejected", "reviewed_by = :by", "reviewed_at = :at", "rejection_reason = :reason"},
		condition: "#status = :pending",
		values: map[string]types.AttributeValue{
			":pending":  stringAV(KeyRequestStatusPending),
			":rejected": stringAV(KeyRequestStatusRejected),
			":by":       stringAV(reviewedBy),
			":at":       reviewedAt,
			":reason":   stringAV(strings.TrimSpace(reason)),
		},
		event: userEvent(KeyRequestEventRejected, reviewedBy, now),
	})
	if err != nil {
		if errors.Is(err, errKeyRequestConditionFailed) {
			return nil, s.notPendingOrMissing(ctx, id)
		}
		return nil, fmt.Errorf("failed to reject key request: %w", err)
	}

	s.releaseKeyRequestPendingLock(ctx, req.RequesterEmail, req.Provider)
	return req, nil
}

// CancelKeyRequest cancels a pending request on behalf of the integration
// caller that opened it. A request from any other source is reported as
// ErrKeyRequestNotFound so callers cannot probe each other's requests. The
// ticket lock is kept, so replaying the ticket returns the cancelled request.
func (s *Store) CancelKeyRequest(ctx context.Context, id, source string) (*KeyRequest, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		return nil, fmt.Errorf("source is required to cancel a key request")
	}
	now := time.Now()
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"#status = :cancelled"},
		condition: "#status = :pending AND #source = :src",
		names:     map[string]string{"#source": "source"},
		values: map[string]types.AttributeValue{
			":pending":   stringAV(KeyRequestStatusPending),
			":cancelled": stringAV(KeyRequestStatusCancelled),
			":src":       stringAV(source),
		},
		event: &KeyRequestEvent{At: now, Event: KeyRequestEventCancelled, ActorType: KeyRequestActorIntegration, Actor: source},
	})
	if err != nil {
		if errors.Is(err, errKeyRequestConditionFailed) {
			existing, getErr := s.GetKeyRequest(ctx, id)
			if getErr != nil {
				return nil, getErr
			}
			if existing.Source != source {
				return nil, ErrKeyRequestNotFound
			}
			return nil, ErrKeyRequestNotPending
		}
		return nil, fmt.Errorf("failed to cancel key request: %w", err)
	}

	s.releaseKeyRequestPendingLock(ctx, req.RequesterEmail, req.Provider)
	return req, nil
}

// ClaimKeyRequest marks an approved request's key as revealed to its
// requester. It succeeds once per approval (or per ResetKeyRequestClaim).
// A request that belongs to someone else is reported as ErrKeyRequestNotFound.
func (s *Store) ClaimKeyRequest(ctx context.Context, id, email string) (*KeyRequest, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	now := time.Now()
	claimedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal claimed_at: %w", err)
	}
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		set:       []string{"claimed_at = :claimed"},
		condition: "#status = :approved AND requester_email = :me AND attribute_not_exists(claimed_at)",
		values: map[string]types.AttributeValue{
			":approved": stringAV(KeyRequestStatusApproved),
			":me":       stringAV(email),
			":claimed":  claimedAt,
		},
		event: userEvent(KeyRequestEventClaimed, email, now),
	})
	if err != nil {
		if errors.Is(err, errKeyRequestConditionFailed) {
			existing, getErr := s.GetKeyRequest(ctx, id)
			switch {
			case getErr != nil:
				return nil, getErr
			case existing.RequesterEmail != email:
				return nil, ErrKeyRequestNotFound
			case existing.Status != KeyRequestStatusApproved:
				return nil, ErrKeyRequestNotApproved
			default:
				return nil, ErrKeyRequestAlreadyClaimed
			}
		}
		return nil, fmt.Errorf("failed to claim key request: %w", err)
	}
	return req, nil
}

// ResetKeyRequestClaim lets the requester claim an approved key once more
// (for example after losing it) and records a "claim_reset" event for actor.
func (s *Store) ResetKeyRequestClaim(ctx context.Context, id, actor string) (*KeyRequest, error) {
	now := time.Now()
	req, err := s.updateKeyRequest(ctx, id, now, keyRequestUpdate{
		remove:    []string{"claimed_at"},
		condition: "#status = :approved AND attribute_exists(claimed_at)",
		values: map[string]types.AttributeValue{
			":approved": stringAV(KeyRequestStatusApproved),
		},
		event: userEvent(KeyRequestEventClaimReset, actor, now),
	})
	if err != nil {
		if errors.Is(err, errKeyRequestConditionFailed) {
			existing, getErr := s.GetKeyRequest(ctx, id)
			switch {
			case getErr != nil:
				return nil, getErr
			case existing.Status != KeyRequestStatusApproved:
				return nil, ErrKeyRequestNotApproved
			default:
				return nil, ErrKeyRequestNotClaimed
			}
		}
		return nil, fmt.Errorf("failed to reset key request claim: %w", err)
	}
	return req, nil
}
