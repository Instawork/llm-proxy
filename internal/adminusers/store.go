package adminusers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	profileSK = "PROFILE"
	shareSKP  = "SHARE#"
	userPKP   = "USER#"
)

// User is an admin dashboard user record.
type User struct {
	Email       string    `json:"email"`
	Name        string    `json:"name,omitempty"`
	Picture     string    `json:"picture,omitempty"`
	Role        Role      `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastLoginAt time.Time `json:"last_login_at,omitempty"`
}

type userItem struct {
	PK          string    `dynamodbav:"pk"`
	SK          string    `dynamodbav:"sk"`
	Email       string    `dynamodbav:"email"`
	Name        string    `dynamodbav:"name,omitempty"`
	Picture     string    `dynamodbav:"picture,omitempty"`
	Role        string    `dynamodbav:"role"`
	CreatedAt   time.Time `dynamodbav:"created_at"`
	UpdatedAt   time.Time `dynamodbav:"updated_at"`
	LastLoginAt time.Time `dynamodbav:"last_login_at,omitempty"`
}

type shareAwarenessItem struct {
	PK          string    `dynamodbav:"pk"`
	SK          string    `dynamodbav:"sk"`
	FirstSeenAt time.Time `dynamodbav:"first_seen_at"`
}

// Store handles admin user persistence in DynamoDB.
type Store struct {
	client    *dynamodb.Client
	tableName string
	logger    *slog.Logger
}

// StoreConfig holds configuration for the admin users store.
type StoreConfig struct {
	TableName       string
	Region          string
	EndpointURL     string
	Logger          *slog.Logger
	AutoCreateTable bool
}

// NewStore creates a new admin users store.
func NewStore(cfg StoreConfig) (*Store, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(
		startupCtx,
		awsconfig.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	endpointURL := cfg.EndpointURL
	if endpointURL == "" {
		endpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	var client *dynamodb.Client
	if endpointURL != "" {
		client = dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
	} else {
		client = dynamodb.NewFromConfig(awsCfg)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	store := &Store{
		client:    client,
		tableName: cfg.TableName,
		logger:    logger,
	}

	if cfg.AutoCreateTable {
		if err := store.ensureTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("failed to ensure table exists: %w", err)
		}
	} else {
		if err := store.verifyTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("admin users table %q is not accessible (pass AutoCreateTable: true in dev only): %w", cfg.TableName, err)
		}
	}

	return store, nil
}

func (s *Store) verifyTableExists(ctx context.Context) error {
	_, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})
	return err
}

func (s *Store) ensureTableExists(ctx context.Context) error {
	_, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})
	if err == nil {
		s.logger.Debug("admin users table already exists", "table", s.tableName)
		return nil
	}

	s.logger.Info("Creating DynamoDB table for admin users", "table", s.tableName)

	_, err = s.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(s.tableName),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	waiter := dynamodb.NewTableExistsWaiter(s.client)
	return waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	}, 2*time.Minute)
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", fmt.Errorf("invalid email")
	}
	return email, nil
}

func userPK(email string) string {
	return userPKP + email
}

func (s *Store) getProfileItem(ctx context.Context, email string) (*userItem, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(email)},
			"sk": &types.AttributeValueMemberS{Value: profileSK},
		},
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, fmt.Errorf("user not found")
	}
	var item userItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func itemToUser(item *userItem) User {
	role, _ := ParseRole(item.Role)
	return User{
		Email:       item.Email,
		Name:        item.Name,
		Picture:     item.Picture,
		Role:        role,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
		LastLoginAt: item.LastLoginAt,
	}
}

// EnsureUser creates a viewer if missing and updates profile + last login.
func (s *Store) EnsureUser(ctx context.Context, email, name, picture string) (User, bool, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, false, err
	}

	now := time.Now().UTC()
	existing, getErr := s.getProfileItem(ctx, email)
	if getErr == nil {
		item := userItem{
			PK:          userPK(email),
			SK:          profileSK,
			Email:       email,
			Name:        name,
			Picture:     picture,
			Role:        existing.Role,
			CreatedAt:   existing.CreatedAt,
			UpdatedAt:   now,
			LastLoginAt: now,
		}
		av, err := attributevalue.MarshalMap(item)
		if err != nil {
			return User{}, false, err
		}
		if _, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(s.tableName),
			Item:      av,
		}); err != nil {
			return User{}, false, err
		}
		return itemToUser(&item), false, nil
	}

	item := userItem{
		PK:          userPK(email),
		SK:          profileSK,
		Email:       email,
		Name:        name,
		Picture:     picture,
		Role:        string(RoleViewer),
		CreatedAt:   now,
		UpdatedAt:   now,
		LastLoginAt: now,
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return User{}, false, err
	}
	if _, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}); err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return s.EnsureUser(ctx, email, name, picture)
		}
		return User{}, false, err
	}
	return itemToUser(&item), true, nil
}

// GetUser returns a user by email.
func (s *Store) GetUser(ctx context.Context, email string) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	item, err := s.getProfileItem(ctx, email)
	if err != nil {
		return User{}, err
	}
	return itemToUser(item), nil
}

// CreateUser pre-provisions a user with the given role.
func (s *Store) CreateUser(ctx context.Context, email string, role Role) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if _, err := ParseRole(string(role)); err != nil {
		return User{}, err
	}

	now := time.Now().UTC()
	item := userItem{
		PK:        userPK(email),
		SK:        profileSK,
		Email:     email,
		Role:      string(role),
		CreatedAt: now,
		UpdatedAt: now,
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return User{}, err
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return User{}, fmt.Errorf("user already exists")
		}
		return User{}, err
	}
	return itemToUser(&item), nil
}

// SetRole updates a user's role.
func (s *Store) SetRole(ctx context.Context, email string, role Role) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if _, err := ParseRole(string(role)); err != nil {
		return err
	}

	now := time.Now().UTC()
	_, err = s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(email)},
			"sk": &types.AttributeValueMemberS{Value: profileSK},
		},
		UpdateExpression: aws.String("SET #role = :role, updated_at = :updated_at"),
		ExpressionAttributeNames: map[string]string{
			"#role": "role",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":role":       &types.AttributeValueMemberS{Value: string(role)},
			":updated_at": &types.AttributeValueMemberS{Value: now.Format(time.RFC3339Nano)},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return fmt.Errorf("user not found")
		}
		return err
	}
	return nil
}

// DeleteUser removes a user's profile row (share awareness rows are left orphaned).
func (s *Store) DeleteUser(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(email)},
			"sk": &types.AttributeValueMemberS{Value: profileSK},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return fmt.Errorf("user not found")
		}
		return err
	}
	return nil
}

// ListUsers returns all user profiles sorted by email.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	var startKey map[string]types.AttributeValue

	for {
		out, err := s.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(s.tableName),
			FilterExpression: aws.String("sk = :profile"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":profile": &types.AttributeValueMemberS{Value: profileSK},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range out.Items {
			var item userItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				return nil, err
			}
			users = append(users, itemToUser(&item))
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Email < users[j].Email
	})
	return users, nil
}

// CountAdmins returns the number of users with the admin role.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n, nil
}

// RecordShareAwareness records that a user opened a share link while authenticated.
func (s *Store) RecordShareAwareness(ctx context.Context, email, shareID string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return fmt.Errorf("share id required")
	}

	now := time.Now().UTC()
	item := shareAwarenessItem{
		PK:          userPK(email),
		SK:          shareSKP + shareID,
		FirstSeenAt: now,
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return nil
		}
		return err
	}
	return nil
}
