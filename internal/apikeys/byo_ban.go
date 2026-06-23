package apikeys

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const BYOBanKeyPrefix = "byo-ban:"

// BYOBan records a blocked bring-your-own provider credential. Only the
// provider + FNV hash of the raw credential is stored — never the secret.
type BYOBan struct {
	PK        string    `dynamodbav:"pk"`
	Provider  string    `dynamodbav:"byo_ban_provider"`
	MaskedID  string    `dynamodbav:"masked_id"`
	Hash      string    `dynamodbav:"credential_hash"`
	BannedBy  string    `dynamodbav:"banned_by,omitempty"`
	Reason    string    `dynamodbav:"reason,omitempty"`
	CreatedAt time.Time `dynamodbav:"created_at"`
}

func byoBanPK(provider, hash string) string {
	return BYOBanKeyPrefix + normalizeProvider(provider) + ":" + hash
}

// BanBYOCredential blocks a BYO credential identified by its masked id.
func (s *Store) BanBYOCredential(ctx context.Context, provider, maskedID, bannedBy, reason string) (*BYOBan, error) {
	var err error
	provider, err = ValidateBYOBanRequest(provider, maskedID)
	if err != nil {
		return nil, err
	}
	hash, err := ParseCredentialHashFromMaskedID(maskedID)
	if err != nil {
		return nil, err
	}
	maskedID = strings.TrimSpace(maskedID)

	now := time.Now()
	rec := &BYOBan{
		PK:        byoBanPK(provider, hash),
		Provider:  provider,
		MaskedID:  maskedID,
		Hash:      hash,
		BannedBy:  strings.TrimSpace(bannedBy),
		Reason:    strings.TrimSpace(reason),
		CreatedAt: now,
	}

	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal BYO ban: %w", err)
	}
	if _, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      item,
	}); err != nil {
		return nil, fmt.Errorf("failed to store BYO ban: %w", err)
	}

	s.logger.Info("Banned BYO credential",
		"provider", provider,
		"masked_id", maskedID,
		"banned_by", rec.BannedBy)
	return rec, nil
}

// UnbanBYOCredential removes a BYO ban for provider + credential hash.
func (s *Store) UnbanBYOCredential(ctx context.Context, provider, hash string) error {
	provider = normalizeProvider(provider)
	hash = strings.TrimSpace(hash)
	if provider == "" || hash == "" {
		return fmt.Errorf("provider and credential hash are required")
	}

	pk := byoBanPK(provider, hash)
	if _, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
		},
	}); err != nil {
		return fmt.Errorf("failed to delete BYO ban: %w", err)
	}

	s.logger.Info("Unbanned BYO credential", "provider", provider, "hash", hash)
	return nil
}

// IsBYOCredentialBanned reports whether provider + hash is blocked.
func (s *Store) IsBYOCredentialBanned(ctx context.Context, provider, hash string) (bool, error) {
	provider = normalizeProvider(provider)
	hash = strings.TrimSpace(hash)
	if provider == "" || hash == "" {
		return false, fmt.Errorf("provider and credential hash are required")
	}

	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: byoBanPK(provider, hash)},
		},
	})
	if err != nil {
		return false, fmt.Errorf("failed to lookup BYO ban: %w", err)
	}
	return len(out.Item) > 0, nil
}

// ListBYOBans returns all BYO bans, optionally filtered by provider.
func (s *Store) ListBYOBans(ctx context.Context, provider string) ([]*BYOBan, error) {
	provider = normalizeProvider(provider)

	scanInput := &dynamodb.ScanInput{
		TableName:        aws.String(s.tableName),
		FilterExpression: aws.String("begins_with(pk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pfx": &types.AttributeValueMemberS{Value: BYOBanKeyPrefix},
		},
	}
	if provider != "" {
		scanInput.FilterExpression = aws.String("begins_with(pk, :pfx) AND byo_ban_provider = :provider")
		scanInput.ExpressionAttributeValues[":provider"] = &types.AttributeValueMemberS{Value: provider}
	}

	var bans []*BYOBan
	for {
		result, err := s.client.Scan(ctx, scanInput)
		if err != nil {
			return nil, fmt.Errorf("failed to scan BYO bans: %w", err)
		}
		for _, item := range result.Items {
			var ban BYOBan
			if err := attributevalue.UnmarshalMap(item, &ban); err != nil {
				s.logger.Warn("Failed to unmarshal BYO ban", "error", err)
				continue
			}
			bans = append(bans, &ban)
		}
		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		scanInput.ExclusiveStartKey = result.LastEvaluatedKey
	}
	return bans, nil
}
