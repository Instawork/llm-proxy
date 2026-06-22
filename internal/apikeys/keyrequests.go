package apikeys

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const KeyRequestPrefix = "keyreq:"

const (
	KeyRequestStatusPending   = "pending"
	KeyRequestStatusApproving = "approving"
	KeyRequestStatusApproved  = "approved"
	KeyRequestStatusRejected  = "rejected"
)

var (
	ErrKeyRequestNotFound      = errors.New("key request not found")
	ErrPendingKeyRequestExists = errors.New("a pending request already exists for this provider")
	ErrKeyRequestNotPending    = errors.New("key request is not pending")
)

// KeyRequest is a user-submitted request for an org-wide service API key.
type KeyRequest struct {
	PK              string     `dynamodbav:"pk"`
	RequesterEmail  string     `dynamodbav:"requester_email"`
	Provider        string     `dynamodbav:"req_provider"`
	Description     string     `dynamodbav:"description"`
	DailyCostLimit  int64      `dynamodbav:"daily_cost_limit,omitempty"`
	Status          string     `dynamodbav:"status"`
	CreatedAt       time.Time  `dynamodbav:"created_at"`
	UpdatedAt       time.Time  `dynamodbav:"updated_at"`
	ReviewedBy      string     `dynamodbav:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time `dynamodbav:"reviewed_at,omitempty"`
	CreatedKey      string     `dynamodbav:"created_key,omitempty"`
	RejectionReason string     `dynamodbav:"rejection_reason,omitempty"`
}

func (k *KeyRequest) ID() string {
	return strings.TrimPrefix(k.PK, KeyRequestPrefix)
}

// CreateKeyRequestInput is the data needed to submit a new key request.
type CreateKeyRequestInput struct {
	RequesterEmail string
	Provider       string
	Description    string
	DailyCostLimit int64
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
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
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

// CreateKeyRequest stores a pending org key request.
func (s *Store) CreateKeyRequest(ctx context.Context, in CreateKeyRequestInput) (*KeyRequest, error) {
	in.RequesterEmail = strings.TrimSpace(strings.ToLower(in.RequesterEmail))
	in.Provider = strings.TrimSpace(strings.ToLower(in.Provider))
	in.Description = strings.TrimSpace(in.Description)
	if in.RequesterEmail == "" || in.Provider == "" || in.Description == "" {
		return nil, fmt.Errorf("requester_email, provider, and description are required")
	}

	if err := s.acquireKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider); err != nil {
		return nil, err
	}

	id, err := newUUIDv4()
	if err != nil {
		s.releaseKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider)
		return nil, err
	}

	now := time.Now()
	req := &KeyRequest{
		PK:             KeyRequestPrefix + id,
		RequesterEmail: in.RequesterEmail,
		Provider:       in.Provider,
		Description:    in.Description,
		DailyCostLimit: in.DailyCostLimit,
		Status:         KeyRequestStatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	av, err := attributevalue.MarshalMap(req)
	if err != nil {
		s.releaseKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider)
		return nil, fmt.Errorf("failed to marshal key request: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		s.releaseKeyRequestPendingLock(ctx, in.RequesterEmail, in.Provider)
		return nil, fmt.Errorf("failed to create key request: %w", err)
	}

	s.logger.Info("Created key request",
		"id", id,
		"requester", in.RequesterEmail,
		"provider", in.Provider)

	return req, nil
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

// BeginKeyRequestApproval atomically claims a pending request for approval.
func (s *Store) BeginKeyRequestApproval(ctx context.Context, id, reviewedBy string) (*KeyRequest, error) {
	id = strings.TrimPrefix(id, KeyRequestPrefix)
	now := time.Now()
	reviewedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal reviewed_at: %w", err)
	}
	updatedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated_at: %w", err)
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + id},
		},
		UpdateExpression: aws.String("SET #status = :approving, reviewed_by = :by, reviewed_at = :at, updated_at = :updated"),
		ConditionExpression: aws.String("#status = :pending"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending":  &types.AttributeValueMemberS{Value: KeyRequestStatusPending},
			":approving": &types.AttributeValueMemberS{Value: KeyRequestStatusApproving},
			":by":       &types.AttributeValueMemberS{Value: reviewedBy},
			":at":       reviewedAt,
			":updated":  updatedAt,
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			if _, getErr := s.GetKeyRequest(ctx, id); errors.Is(getErr, ErrKeyRequestNotFound) {
				return nil, ErrKeyRequestNotFound
			}
			return nil, ErrKeyRequestNotPending
		}
		return nil, fmt.Errorf("failed to begin key request approval: %w", err)
	}

	var req KeyRequest
	if err := attributevalue.UnmarshalMap(result.Attributes, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal key request: %w", err)
	}
	return &req, nil
}

// CompleteKeyRequestApproval marks an approving request approved with the created key.
func (s *Store) CompleteKeyRequestApproval(ctx context.Context, id, createdKey string) (*KeyRequest, error) {
	id = strings.TrimPrefix(id, KeyRequestPrefix)
	now := time.Now()
	updatedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated_at: %w", err)
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + id},
		},
		UpdateExpression: aws.String("SET #status = :approved, created_key = :key, updated_at = :updated"),
		ConditionExpression: aws.String("#status = :approving"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":approving": &types.AttributeValueMemberS{Value: KeyRequestStatusApproving},
			":approved":  &types.AttributeValueMemberS{Value: KeyRequestStatusApproved},
			":key":       &types.AttributeValueMemberS{Value: createdKey},
			":updated":   updatedAt,
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to complete key request approval: %w", err)
	}

	var req KeyRequest
	if err := attributevalue.UnmarshalMap(result.Attributes, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal key request: %w", err)
	}

	s.releaseKeyRequestPendingLock(ctx, req.RequesterEmail, req.Provider)
	return &req, nil
}

// RollbackKeyRequestApproval returns an approving request to pending.
func (s *Store) RollbackKeyRequestApproval(ctx context.Context, id string) error {
	id = strings.TrimPrefix(id, KeyRequestPrefix)
	now := time.Now()
	updatedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return fmt.Errorf("failed to marshal updated_at: %w", err)
	}

	_, err = s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + id},
		},
		UpdateExpression: aws.String("SET #status = :pending, updated_at = :updated REMOVE reviewed_by, reviewed_at, created_key"),
		ConditionExpression: aws.String("#status = :approving"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":approving": &types.AttributeValueMemberS{Value: KeyRequestStatusApproving},
			":pending":   &types.AttributeValueMemberS{Value: KeyRequestStatusPending},
			":updated":   updatedAt,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to rollback key request approval: %w", err)
	}
	return nil
}

// RejectKeyRequest marks a request rejected with an optional reason.
func (s *Store) RejectKeyRequest(ctx context.Context, id, reviewedBy, reason string) (*KeyRequest, error) {
	id = strings.TrimPrefix(id, KeyRequestPrefix)
	now := time.Now()
	reviewedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal reviewed_at: %w", err)
	}
	updatedAt, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated_at: %w", err)
	}

	result, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: KeyRequestPrefix + id},
		},
		UpdateExpression: aws.String("SET #status = :rejected, reviewed_by = :by, reviewed_at = :at, rejection_reason = :reason, updated_at = :updated"),
		ConditionExpression: aws.String("#status = :pending"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending":  &types.AttributeValueMemberS{Value: KeyRequestStatusPending},
			":rejected": &types.AttributeValueMemberS{Value: KeyRequestStatusRejected},
			":by":       &types.AttributeValueMemberS{Value: reviewedBy},
			":at":       reviewedAt,
			":reason":   &types.AttributeValueMemberS{Value: strings.TrimSpace(reason)},
			":updated":  updatedAt,
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			if _, getErr := s.GetKeyRequest(ctx, id); errors.Is(getErr, ErrKeyRequestNotFound) {
				return nil, ErrKeyRequestNotFound
			}
			return nil, ErrKeyRequestNotPending
		}
		return nil, fmt.Errorf("failed to reject key request: %w", err)
	}

	var req KeyRequest
	if err := attributevalue.UnmarshalMap(result.Attributes, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal key request: %w", err)
	}

	s.releaseKeyRequestPendingLock(ctx, req.RequesterEmail, req.Provider)
	return &req, nil
}
