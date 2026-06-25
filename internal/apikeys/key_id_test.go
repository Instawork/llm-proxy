package apikeys

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestMaskKeyID_FormatContract(t *testing.T) {
	const pk = "sk-iw-0123456789abcdefghijklmnop"
	got := MaskKeyID(pk)
	want := pk[:12] + maskedIDSplit + CredentialHashSuffix(pk)
	if got != want {
		t.Fatalf("MaskKeyID = %q want %q", got, want)
	}
	if MaskKeyID("iw:short") != "iw:short" {
		t.Fatalf("short key unexpectedly masked: %q", MaskKeyID("iw:short"))
	}
}

func TestStore_GetKeyRecordByID_FullKey(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	created, err := store.CreateKey(ctx, "openai", "sk-upstream", "test", 0, nil, nil, KeyRateLimits{})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	got, err := store.GetKeyRecordByID(ctx, created.PK)
	if err != nil {
		t.Fatalf("GetKeyRecordByID full: %v", err)
	}
	if got.PK != created.PK {
		t.Fatalf("pk = %q want %q", got.PK, created.PK)
	}
}

func TestStore_GetKeyRecordByID_MaskedID(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	created, err := store.CreateKey(ctx, "openai", "sk-upstream", "test", 0, nil, nil, KeyRateLimits{})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	masked := MaskKeyID(created.PK)
	got, err := store.GetKeyRecordByID(ctx, masked)
	if err != nil {
		t.Fatalf("GetKeyRecordByID masked: %v", err)
	}
	if got.PK != created.PK {
		t.Fatalf("pk = %q want %q", got.PK, created.PK)
	}
}

func TestStore_GetKeyRecordByID_LegacyIWMaskedID(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	created := &APIKey{
		PK:             "iw:0123456789abcdefghijklmnop",
		Provider:       "openai",
		ActualKey:      "sk-upstream",
		DailyCostLimit: 100,
		Description:    "legacy",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		Enabled:        true,
	}
	item, err := attributevalue.MarshalMap(created)
	if err != nil {
		t.Fatalf("MarshalMap: %v", err)
	}
	if _, err := store.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(store.tableName),
		Item:      item,
	}); err != nil {
		t.Fatalf("PutItem: %v", err)
	}

	masked := MaskKeyID(created.PK)
	if !IsProxyMaskedKeyID(masked) {
		t.Fatalf("masked legacy key was not recognized: %q", masked)
	}
	got, err := store.GetKeyRecordByID(ctx, masked)
	if err != nil {
		t.Fatalf("GetKeyRecordByID legacy masked: %v", err)
	}
	if got.PK != created.PK {
		t.Fatalf("pk = %q want %q", got.PK, created.PK)
	}
}

func TestStore_GetKeyRecordByID_NotFound(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	_, err := store.GetKeyRecordByID(ctx, "sk-iw-0123456789…00000000")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found, got %v", err)
	}
}
