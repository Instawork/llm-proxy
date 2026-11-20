package apikeys

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Instawork/llm-proxy/internal/config"
)

// AgentLimitPrefix is the pk prefix of per-agent spend-limit overrides. It
// shares no prefix with key records, key requests, share links, or BYO bans,
// so their scans never return these items.
const AgentLimitPrefix = "AGENTLIMIT#"

// maxAgentLabelLen bounds labels so a caller cannot write oversized keys.
const maxAgentLabelLen = 128

var (
	ErrAgentLimitNotFound = errors.New("agent limit not found")
	ErrInvalidAgentLabel  = errors.New("invalid agent label")
)

// AgentLimit is a per-agent override of the agent_spend_limits defaults. A
// nil field means "use the default". UpdatedBy is the editor's email: store
// it, never log it.
type AgentLimit struct {
	PK               string    `dynamodbav:"pk"`
	SoftMonthlyCents *int64    `dynamodbav:"soft_monthly_cents,omitempty"`
	HardMonthlyCents *int64    `dynamodbav:"hard_monthly_cents,omitempty"`
	Exempt           *bool     `dynamodbav:"exempt,omitempty"`
	UpdatedBy        string    `dynamodbav:"updated_by,omitempty"`
	UpdatedAt        time.Time `dynamodbav:"updated_at"`
}

// Label returns the agent user label, e.g. "finch-agent-123".
func (a *AgentLimit) Label() string {
	return strings.TrimPrefix(a.PK, AgentLimitPrefix)
}

func (a *AgentLimit) clone() *AgentLimit {
	if a == nil {
		return nil
	}
	c := *a
	if a.SoftMonthlyCents != nil {
		v := *a.SoftMonthlyCents
		c.SoftMonthlyCents = &v
	}
	if a.HardMonthlyCents != nil {
		v := *a.HardMonthlyCents
		c.HardMonthlyCents = &v
	}
	if a.Exempt != nil {
		v := *a.Exempt
		c.Exempt = &v
	}
	return &c
}

// FieldUpdate is a tri-state patch for one AgentLimit attribute: the zero
// value leaves it unchanged, SetField stores a value, ClearField removes the attribute
// so the default applies again.
type FieldUpdate[T any] struct {
	present bool
	value   *T
}

// SetField returns an update that stores v.
func SetField[T any](v T) FieldUpdate[T] { return FieldUpdate[T]{present: true, value: &v} }

// ClearField returns an update that removes the attribute.
func ClearField[T any]() FieldUpdate[T] { return FieldUpdate[T]{present: true} }

// Present reports whether the update changes the attribute.
func (f FieldUpdate[T]) Present() bool { return f.present }

// Value returns the new value, or nil when the update clears the attribute.
func (f FieldUpdate[T]) Value() *T { return f.value }

// AgentLimitUpdate patches an AGENTLIMIT item. UpdatedBy is required.
type AgentLimitUpdate struct {
	SoftMonthlyCents FieldUpdate[int64]
	HardMonthlyCents FieldUpdate[int64]
	Exempt           FieldUpdate[bool]
	UpdatedBy        string
}

// ValidateAgentLabel rejects labels that cannot be a /meta/<label>/ path
// segment or a "<label>|spend_usd" Redis hash field.
func ValidateAgentLabel(label string) error {
	if label == "" || len(label) > maxAgentLabelLen {
		return fmt.Errorf("%w: must be 1-%d characters", ErrInvalidAgentLabel, maxAgentLabelLen)
	}
	if strings.ContainsAny(label, "/|# \t\r\n") {
		return fmt.Errorf("%w: must not contain '/', '|', '#', or whitespace", ErrInvalidAgentLabel)
	}
	return nil
}

func agentLimitPK(label string) string { return AgentLimitPrefix + label }

// GetAgentLimit loads the override for label, or ErrAgentLimitNotFound.
func (s *Store) GetAgentLimit(ctx context.Context, label string) (*AgentLimit, error) {
	if err := ValidateAgentLabel(label); err != nil {
		return nil, err
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: agentLimitPK(label)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get agent limit: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrAgentLimitNotFound
	}
	var rec AgentLimit
	if err := attributevalue.UnmarshalMap(out.Item, &rec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal agent limit: %w", err)
	}
	rec.PK = agentLimitPK(label)
	return &rec, nil
}

// PutAgentLimit applies upd to the override for label, creating the item if
// needed, and returns the stored result. It checks only that set values are
// non-negative; cross-field bounds against the defaults are the caller's job
// (see EffectiveAgentLimits.Check).
func (s *Store) PutAgentLimit(ctx context.Context, label string, upd AgentLimitUpdate) (*AgentLimit, error) {
	if err := ValidateAgentLabel(label); err != nil {
		return nil, err
	}
	updatedBy := strings.TrimSpace(strings.ToLower(upd.UpdatedBy))
	if updatedBy == "" {
		return nil, fmt.Errorf("updated_by is required")
	}
	for name, f := range map[string]FieldUpdate[int64]{
		"soft_monthly_cents": upd.SoftMonthlyCents,
		"hard_monthly_cents": upd.HardMonthlyCents,
	} {
		if v := f.Value(); v != nil && *v < 0 {
			return nil, fmt.Errorf("%s cannot be negative", name)
		}
	}

	sets := []string{"updated_by = :updated_by", "updated_at = :updated_at"}
	var removes []string
	values := map[string]types.AttributeValue{
		":updated_by": &types.AttributeValueMemberS{Value: updatedBy},
		":updated_at": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	addInt := func(attr string, f FieldUpdate[int64]) {
		if !f.Present() {
			return
		}
		if v := f.Value(); v != nil {
			sets = append(sets, attr+" = :"+attr)
			values[":"+attr] = &types.AttributeValueMemberN{Value: strconv.FormatInt(*v, 10)}
			return
		}
		removes = append(removes, attr)
	}
	addInt("soft_monthly_cents", upd.SoftMonthlyCents)
	addInt("hard_monthly_cents", upd.HardMonthlyCents)
	if upd.Exempt.Present() {
		if v := upd.Exempt.Value(); v != nil {
			sets = append(sets, "exempt = :exempt")
			values[":exempt"] = &types.AttributeValueMemberBOOL{Value: *v}
		} else {
			removes = append(removes, "exempt")
		}
	}

	expr := "SET " + strings.Join(sets, ", ")
	if len(removes) > 0 {
		expr += " REMOVE " + strings.Join(removes, ", ")
	}
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: agentLimitPK(label)},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update agent limit: %w", err)
	}
	var rec AgentLimit
	if err := attributevalue.UnmarshalMap(out.Attributes, &rec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal agent limit: %w", err)
	}
	rec.PK = agentLimitPK(label)
	s.logger.Info("Updated agent limit", "label", label)
	return &rec, nil
}

// DeleteAgentLimit removes the override for label so every default applies.
// Deleting a missing item is not an error.
func (s *Store) DeleteAgentLimit(ctx context.Context, label string) error {
	if err := ValidateAgentLabel(label); err != nil {
		return err
	}
	if _, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: agentLimitPK(label)},
		},
	}); err != nil {
		return fmt.Errorf("failed to delete agent limit: %w", err)
	}
	s.logger.Info("Deleted agent limit", "label", label)
	return nil
}

// ListAgentLimits returns every stored override, keyed by label, in one
// paginated scan so a caller listing many agents avoids a read per label.
func (s *Store) ListAgentLimits(ctx context.Context) (map[string]*AgentLimit, error) {
	in := &dynamodb.ScanInput{
		TableName:        aws.String(s.tableName),
		FilterExpression: aws.String("begins_with(pk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pfx": &types.AttributeValueMemberS{Value: AgentLimitPrefix},
		},
	}
	out := make(map[string]*AgentLimit)
	for {
		res, err := s.client.Scan(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("failed to scan agent limits: %w", err)
		}
		for _, item := range res.Items {
			var rec AgentLimit
			if err := attributevalue.UnmarshalMap(item, &rec); err != nil {
				s.logger.Warn("Failed to unmarshal agent limit", "error", err)
				continue
			}
			out[rec.Label()] = &rec
		}
		if len(res.LastEvaluatedKey) == 0 {
			return out, nil
		}
		in.ExclusiveStartKey = res.LastEvaluatedKey
	}
}

// EffectiveAgentLimits is a label's override merged over the defaults.
type EffectiveAgentLimits struct {
	Label            string
	SoftMonthlyCents int64
	HardMonthlyCents int64
	Exempt           bool
	// Override is a copy of the stored item, or nil when none exists.
	Override *AgentLimit
}

// Check enforces 0 <= soft <= hard <= maxHard on the effective values.
func (e EffectiveAgentLimits) Check(maxHardMonthlyCents int64) error {
	if e.SoftMonthlyCents < 0 {
		return fmt.Errorf("soft_monthly_cents cannot be negative")
	}
	if e.HardMonthlyCents < e.SoftMonthlyCents {
		return fmt.Errorf("hard_monthly_cents (%d) must be at least soft_monthly_cents (%d)", e.HardMonthlyCents, e.SoftMonthlyCents)
	}
	if e.HardMonthlyCents > maxHardMonthlyCents {
		return fmt.Errorf("hard_monthly_cents (%d) cannot exceed %d", e.HardMonthlyCents, maxHardMonthlyCents)
	}
	return nil
}

// MergeAgentLimit returns override merged over the defaults in cfg.
func MergeAgentLimit(cfg config.AgentSpendLimitsConfig, label string, override *AgentLimit) EffectiveAgentLimits {
	e := EffectiveAgentLimits{
		Label:            label,
		SoftMonthlyCents: cfg.DefaultSoftMonthlyCents,
		HardMonthlyCents: cfg.DefaultHardMonthlyCents,
		Override:         override,
	}
	if override == nil {
		return e
	}
	if override.SoftMonthlyCents != nil {
		e.SoftMonthlyCents = *override.SoftMonthlyCents
	}
	if override.HardMonthlyCents != nil {
		e.HardMonthlyCents = *override.HardMonthlyCents
	}
	if override.Exempt != nil {
		e.Exempt = *override.Exempt
	}
	return e
}

// AgentLimitGetter is the read side of Store used by AgentLimitResolver.
type AgentLimitGetter interface {
	GetAgentLimit(ctx context.Context, label string) (*AgentLimit, error)
}

// agentLimitCacheMaxEntries triggers a sweep of expired entries; labels are
// one per agent, so the cache stays small in practice.
const agentLimitCacheMaxEntries = 10000

type agentLimitCacheEntry struct {
	override  *AgentLimit
	expiresAt time.Time
}

// AgentLimitResolver returns effective limits per label through an
// in-process cache. Hits and misses are cached for cfg.LimitCacheTTL(), so a
// change reaches every process within one TTL. Lookup errors are not cached.
type AgentLimitResolver struct {
	getter AgentLimitGetter
	cfg    config.AgentSpendLimitsConfig
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]agentLimitCacheEntry
}

// NewAgentLimitResolver builds a resolver over getter with cfg's defaults.
func NewAgentLimitResolver(getter AgentLimitGetter, cfg config.AgentSpendLimitsConfig) *AgentLimitResolver {
	return &AgentLimitResolver{
		getter:  getter,
		cfg:     cfg,
		ttl:     cfg.LimitCacheTTL(),
		now:     time.Now,
		entries: make(map[string]agentLimitCacheEntry),
	}
}

// Resolve returns the effective limits for label. On a lookup error it
// still returns usable limits, from the last cached override if there is one
// and otherwise the defaults, together with the error so the caller can
// report degraded enforcement.
func (r *AgentLimitResolver) Resolve(ctx context.Context, label string) (EffectiveAgentLimits, error) {
	now := r.now()
	r.mu.Lock()
	entry, ok := r.entries[label]
	r.mu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return MergeAgentLimit(r.cfg, label, entry.override.clone()), nil
	}

	override, err := r.getter.GetAgentLimit(ctx, label)
	switch {
	case errors.Is(err, ErrAgentLimitNotFound):
		override = nil
	case err != nil:
		var stale *AgentLimit
		if ok {
			stale = entry.override.clone()
		}
		return MergeAgentLimit(r.cfg, label, stale), err
	}

	r.mu.Lock()
	if len(r.entries) >= agentLimitCacheMaxEntries {
		for k, e := range r.entries {
			if !now.Before(e.expiresAt) {
				delete(r.entries, k)
			}
		}
	}
	r.entries[label] = agentLimitCacheEntry{override: override.clone(), expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return MergeAgentLimit(r.cfg, label, override), nil
}

// Invalidate drops label from the cache, for the process that wrote it.
func (r *AgentLimitResolver) Invalidate(label string) {
	r.mu.Lock()
	delete(r.entries, label)
	r.mu.Unlock()
}

// Config returns the agent_spend_limits block the resolver was built with.
func (r *AgentLimitResolver) Config() config.AgentSpendLimitsConfig {
	return r.cfg
}
