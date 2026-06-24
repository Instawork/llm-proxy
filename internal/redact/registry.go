package redact

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const maxPlaceholderCarry = 64

type registryEntry struct {
	placeholder string
	original    string
	policy      Policy
}

// Registry maps placeholder tokens to original span values for a single
// proxy request. It is not persisted — lifetime is one HTTP round trip.
type Registry struct {
	mu sync.Mutex

	counters      map[string]int
	byPlaceholder map[string]registryEntry
	restoreOrder  []string // MASK placeholders longest-first for ReplaceAll
	restoredCount int
}

// NewRegistry constructs an empty per-request registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:      make(map[string]int),
		byPlaceholder: make(map[string]registryEntry),
	}
}

// Placeholder returns the replacement token for original at entityType,
// reusing an existing placeholder when the same original was seen before.
func (r *Registry) Placeholder(entityType, original string) string {
	policy := PolicyFor(entityType)
	switch policy {
	case PolicyRedact:
		return fmt.Sprintf("[REDACTED:%s]", entityType)
	case PolicyMask, PolicySeal:
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, entry := range r.byPlaceholder {
			if entry.original == original && entry.policy == policy {
				return entry.placeholder
			}
		}
		r.counters[entityType]++
		ph := fmt.Sprintf("<%s_%d>", entityType, r.counters[entityType])
		r.byPlaceholder[ph] = registryEntry{
			placeholder: ph,
			original:    original,
			policy:      policy,
		}
		if policy == PolicyMask {
			r.restoreOrder = append(r.restoreOrder, ph)
		}
		return ph
	default:
		return fmt.Sprintf("[REDACTED:%s]", entityType)
	}
}

// jsonEscapedPlaceholder returns the form encoding/json uses for angle
// brackets inside JSON string values (\u003c / \u003e).
func jsonEscapedPlaceholder(ph string) string {
	s := strings.ReplaceAll(ph, "<", `\u003c`)
	return strings.ReplaceAll(s, ">", `\u003e`)
}

// RestoreUserFacing replaces MASK-tier placeholders with their original
// values. SEAL placeholders and REDACT markers are left unchanged.
func (r *Registry) RestoreUserFacing(text string) string {
	if r == nil || text == "" {
		return text
	}
	r.mu.Lock()
	order := append([]string(nil), r.restoreOrder...)
	r.mu.Unlock()
	if len(order) == 0 {
		return text
	}
	sort.Slice(order, func(i, j int) bool {
		return len(order[i]) > len(order[j])
	})
	out := text
	for _, ph := range order {
		r.mu.Lock()
		entry, ok := r.byPlaceholder[ph]
		r.mu.Unlock()
		if !ok || entry.policy != PolicyMask {
			continue
		}
		before := out
		out = strings.ReplaceAll(out, ph, entry.original)
		out = strings.ReplaceAll(out, jsonEscapedPlaceholder(ph), entry.original)
		r.mu.Lock()
		r.restoredCount += strings.Count(before, ph) + strings.Count(before, jsonEscapedPlaceholder(ph))
		r.mu.Unlock()
	}
	return out
}

// RestoredCount returns how many MASK placeholder occurrences were replaced
// in responses during this request.
func (r *Registry) RestoredCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restoredCount
}

// RestoreStreamChunk restores MASK placeholders in a streaming chunk,
// holding back a suffix that might be an incomplete placeholder token.
func (r *Registry) RestoreStreamChunk(chunk []byte, carry []byte) (emit []byte, newCarry []byte) {
	combined := append(append([]byte(nil), carry...), chunk...)
	safeLen := len(combined)
	if idx := bytes.LastIndexByte(combined, '<'); idx >= 0 {
		if bytes.IndexByte(combined[idx:], '>') < 0 {
			safeLen = idx
		}
	}
	toProcess := combined[:safeLen]
	newCarry = combined[safeLen:]
	restored := r.RestoreUserFacing(string(toProcess))
	return []byte(restored), newCarry
}

// FlushCarry restores any bytes held back at the end of a stream.
func (r *Registry) FlushCarry(carry []byte) []byte {
	if len(carry) == 0 {
		return nil
	}
	return []byte(r.RestoreUserFacing(string(carry)))
}

// Len returns the number of registered placeholders (MASK + SEAL).
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byPlaceholder)
}
