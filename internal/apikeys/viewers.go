package apikeys

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// MaxKeyViewers caps explicit viewers per key. The cap is checked against the
// record read just before the write, so concurrent adds can overshoot it by a
// few entries; it exists to bound blast radius, not as a hard quota.
const MaxKeyViewers = 50

var (
	// ErrViewersNotAllowed is returned when viewers are managed on a personal key.
	ErrViewersNotAllowed = errors.New("viewers can only be granted on org-wide keys")
	// ErrTooManyViewers is returned when a grant would exceed MaxKeyViewers.
	ErrTooManyViewers = fmt.Errorf("a key can have at most %d viewers", MaxKeyViewers)
)

// NormalizeViewerEmail lowercases and trims an email for storage and comparison.
func NormalizeViewerEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// IsKeyViewer reports whether email may read k as a viewer: it is listed in
// ViewerEmails or is the requester the key was approved for. Personal keys
// have no viewers; their owner is handled separately.
func IsKeyViewer(k *APIKey, email string) bool {
	email = NormalizeViewerEmail(email)
	if k == nil || email == "" || IsPersonalKey(k) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(k.RequesterEmail), email) {
		return true
	}
	for _, v := range k.ViewerEmails {
		if strings.EqualFold(strings.TrimSpace(v), email) {
			return true
		}
	}
	return false
}

// AddKeyViewers grants read-only access to emails and returns the updated
// record. Adding an existing viewer is a no-op.
func (s *Store) AddKeyViewers(ctx context.Context, key string, emails []string) (*APIKey, error) {
	record, err := s.GetKeyRecord(ctx, key)
	if err != nil {
		return nil, err
	}
	if IsPersonalKey(record) {
		return nil, ErrViewersNotAllowed
	}

	existing := make(map[string]struct{}, len(record.ViewerEmails))
	for _, v := range record.ViewerEmails {
		existing[NormalizeViewerEmail(v)] = struct{}{}
	}
	var fresh []string
	for _, e := range emails {
		e = NormalizeViewerEmail(e)
		if e == "" {
			continue
		}
		if _, ok := existing[e]; ok {
			continue
		}
		existing[e] = struct{}{}
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 {
		return record, nil
	}
	if len(existing) > MaxKeyViewers {
		return nil, ErrTooManyViewers
	}

	if err := s.updateViewerSet(ctx, key, "ADD", fresh); err != nil {
		return nil, err
	}
	s.logger.Info("Granted API key viewers", "key", RedactKey(key), "added", len(fresh))
	return s.GetKeyRecord(ctx, key)
}

// RemoveKeyViewer revokes an explicit viewer grant and returns the updated
// record. Removing an absent viewer is a no-op. It does not affect the
// implicit requester grant.
func (s *Store) RemoveKeyViewer(ctx context.Context, key, email string) (*APIKey, error) {
	email = NormalizeViewerEmail(email)
	if email == "" {
		return nil, fmt.Errorf("viewer email is required")
	}
	if _, err := s.GetKeyRecord(ctx, key); err != nil {
		return nil, err
	}
	if err := s.updateViewerSet(ctx, key, "DELETE", []string{email}); err != nil {
		return nil, err
	}
	s.logger.Info("Revoked API key viewer", "key", RedactKey(key))
	return s.GetKeyRecord(ctx, key)
}

func (s *Store) updateViewerSet(ctx context.Context, key, action string, emails []string) error {
	sorted := append([]string(nil), emails...)
	sort.Strings(sorted)
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		UpdateExpression: aws.String(action + " viewer_emails :v SET updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":v":   &types.AttributeValueMemberSS{Value: sorted},
			":now": &types.AttributeValueMemberS{Value: time.Now().Format(time.RFC3339)},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("failed to update API key viewers: %w", err)
	}
	return nil
}
