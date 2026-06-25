package apikeys

import (
	"context"
	"fmt"
	"strings"
)

// MaskKeyID returns a short dashboard identifier for a proxy key: a 12-char
// prefix plus an FNV-1a/32 hash of the whole key. Safe for URLs and logs.
func MaskKeyID(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 12 {
		return key
	}
	return key[:12] + maskedIDSplit + CredentialHashSuffix(key)
}

// GetKeyRecordByID resolves a full proxy key or masked dashboard id to a record.
func (s *Store) GetKeyRecordByID(ctx context.Context, id string) (*APIKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("empty key identifier")
	}
	if IsProxyMaskedKeyID(id) && (strings.Contains(id, maskedIDSplit) || strings.Contains(id, "...")) {
		return s.getKeyRecordByMaskedID(ctx, id)
	}
	if HasKeyPrefix(id) {
		return s.GetKeyRecord(ctx, id)
	}
	if !IsProxyMaskedKeyID(id) {
		return nil, fmt.Errorf("invalid key identifier")
	}
	return s.getKeyRecordByMaskedID(ctx, id)
}

func (s *Store) getKeyRecordByMaskedID(ctx context.Context, id string) (*APIKey, error) {
	hash, err := ParseCredentialHashFromMaskedID(id)
	if err != nil {
		return nil, err
	}
	head := MaskedIDHead(id)
	keys, err := s.ListKeys(ctx, "")
	if err != nil {
		return nil, err
	}
	var matches []*APIKey
	for _, k := range keys {
		if CredentialHashSuffix(k.PK) != hash {
			continue
		}
		if head != "" && !strings.HasPrefix(k.PK, head) {
			continue
		}
		matches = append(matches, k)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("API key not found")
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("ambiguous key identifier")
	}
	return matches[0], nil
}
