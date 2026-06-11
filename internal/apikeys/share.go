package apikeys

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ShareKeyPrefix namespaces share-link items in the same DynamoDB table as
// API keys. Share items deliberately do NOT carry a `provider` attribute
// (they use `share_provider` instead) so they stay out of the ProviderIndex
// GSI and the iw:-filtered key scan.
const ShareKeyPrefix = "share:"

// ShareLinkTTL is how long a minted share URL stays valid before a new UUID
// is required. Re-clicking Share within this window returns the same link.
const ShareLinkTTL = 24 * time.Hour

// ShareLink maps an opaque, key-independent UUID to the API key it reveals.
// The UUID is what goes in the shareable URL; it has no cryptographic
// relationship to the key, so leaking the UUID alone (without an admin
// session) discloses nothing.
type ShareLink struct {
	// PK is "share:<uuid>".
	PK string `dynamodbav:"pk"`
	// APIKey is the iw: proxy key this link reveals.
	APIKey string `dynamodbav:"share_api_key"`
	// Provider is denormalised for convenience. Stored under a non-GSI
	// attribute name so share items never appear in ProviderIndex.
	Provider string `dynamodbav:"share_provider"`
	// CreatedBy is the admin email that minted the link.
	CreatedBy string `dynamodbav:"share_created_by,omitempty"`
	// CreatedAt is when the link was minted.
	CreatedAt time.Time `dynamodbav:"created_at"`
	// ExpiresAt optionally expires the link (TTL-friendly).
	ExpiresAt *time.Time `dynamodbav:"expires_at,omitempty"`
}

// ID returns the bare UUID (without the "share:" prefix).
func (s *ShareLink) ID() string {
	return strings.TrimPrefix(s.PK, ShareKeyPrefix)
}

func (s *ShareLink) expiresAt(now time.Time) time.Time {
	if s.ExpiresAt != nil {
		return *s.ExpiresAt
	}
	return s.CreatedAt.Add(ShareLinkTTL)
}

// EffectiveExpiresAt returns when the link stops working (explicit expiry or
// created_at + ShareLinkTTL for older records).
func (s *ShareLink) EffectiveExpiresAt(now time.Time) time.Time {
	return s.expiresAt(now)
}

func (s *ShareLink) isActive(now time.Time) bool {
	return s.expiresAt(now).After(now)
}

// newUUIDv4 returns a random RFC-4122 v4 UUID string using crypto/rand,
// avoiding a third-party dependency.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// CreateShareLink returns a share URL for an existing API key. If a link for
// that key is still within ShareLinkTTL, the same UUID is returned; otherwise
// any expired link is removed and a fresh UUID is minted with a new expiry.
func (s *Store) CreateShareLink(ctx context.Context, apiKey, createdBy string) (*ShareLink, error) {
	rec, err := s.GetKeyRecord(ctx, apiKey)
	if err != nil {
		return nil, fmt.Errorf("cannot share unknown key: %w", err)
	}

	now := time.Now()
	existing, err := s.listShareLinksForKey(ctx, apiKey)
	if err != nil {
		return nil, err
	}

	var active *ShareLink
	for _, link := range existing {
		if link.isActive(now) {
			if active == nil || link.CreatedAt.After(active.CreatedAt) {
				active = link
			}
			continue
		}
		if err := s.DeleteShareLink(ctx, link.ID()); err != nil {
			s.logger.Warn("Failed to delete expired share link",
				"id", link.ID(),
				"key", RedactKey(apiKey),
				"error", err)
		}
	}
	if active != nil {
		s.logger.Info("Reusing active share link",
			"id", active.ID(),
			"key", RedactKey(rec.PK),
			"expires_at", active.expiresAt(now))
		return active, nil
	}

	id, err := newUUIDv4()
	if err != nil {
		return nil, err
	}

	expiresAt := now.Add(ShareLinkTTL)
	link := &ShareLink{
		PK:        ShareKeyPrefix + id,
		APIKey:    rec.PK,
		Provider:  rec.Provider,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: &expiresAt,
	}

	av, err := attributevalue.MarshalMap(link)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal share link: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create share link: %w", err)
	}

	s.logger.Info("Created share link",
		"id", id,
		"key", RedactKey(rec.PK),
		"provider", rec.Provider,
		"created_by", createdBy,
		"expires_at", expiresAt)

	return link, nil
}

func (s *Store) listShareLinksForKey(ctx context.Context, apiKey string) ([]*ShareLink, error) {
	scanInput := &dynamodb.ScanInput{
		TableName: aws.String(s.tableName),
		FilterExpression: aws.String(
			"begins_with(pk, :pfx) AND share_api_key = :key",
		),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pfx": &types.AttributeValueMemberS{Value: ShareKeyPrefix},
			":key": &types.AttributeValueMemberS{Value: apiKey},
		},
	}

	var links []*ShareLink
	for {
		result, err := s.client.Scan(ctx, scanInput)
		if err != nil {
			return nil, fmt.Errorf("failed to list share links for key: %w", err)
		}
		for _, item := range result.Items {
			var link ShareLink
			if err := attributevalue.UnmarshalMap(item, &link); err != nil {
				s.logger.Warn("Failed to unmarshal share link", "error", err)
				continue
			}
			links = append(links, &link)
		}
		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		scanInput.ExclusiveStartKey = result.LastEvaluatedKey
	}
	return links, nil
}

// GetShareLink resolves a share UUID to its link record. Enforces expiry.
func (s *Store) GetShareLink(ctx context.Context, id string) (*ShareLink, error) {
	id = strings.TrimPrefix(id, ShareKeyPrefix)
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: ShareKeyPrefix + id},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get share link: %w", err)
	}
	if result.Item == nil {
		return nil, fmt.Errorf("share link not found")
	}

	var link ShareLink
	if err := attributevalue.UnmarshalMap(result.Item, &link); err != nil {
		return nil, fmt.Errorf("failed to unmarshal share link: %w", err)
	}
	if link.ExpiresAt != nil && link.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("share link has expired")
	}
	if link.ExpiresAt == nil && !link.isActive(time.Now()) {
		return nil, fmt.Errorf("share link has expired")
	}
	return &link, nil
}

// DeleteShareLink revokes a share link by UUID.
func (s *Store) DeleteShareLink(ctx context.Context, id string) error {
	id = strings.TrimPrefix(id, ShareKeyPrefix)
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: ShareKeyPrefix + id},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("failed to delete share link: %w", err)
	}
	s.logger.Info("Deleted share link", "id", id)
	return nil
}
