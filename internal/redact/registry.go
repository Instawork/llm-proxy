package redact

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	maxPlaceholderCarry    = 64
	placeholderTokenPrefix = "PII_"
)

// WirePlaceholderPattern matches canonical upstream MASK/SEAL tokens.
const WirePlaceholderPattern = `<PII_[A-Z][A-Z0-9_]*_\d+>`

var wirePlaceholderRE = regexp.MustCompile(WirePlaceholderPattern)

var placeholderDelimiterPairs = []struct{ open, close byte }{
	{'<', '>'},
	{'[', ']'},
	{'{', '}'},
	{'(', ')'},
}

// piiPlaceholderTailRE matches a complete PII token at the start of a stream
// carry suffix, optionally followed by whitespace or a closing delimiter.
var piiPlaceholderTailRE = regexp.MustCompile(`^PII_[A-Z][A-Z0-9_]*_\d+([\s\]\}>)]|$)`)

type registryEntry struct {
	placeholder string
	original    string
	policy      Policy
	wireForms   []string // MASK only; delimiter-bounded variants, longest-first
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
		ph := fmt.Sprintf("<%s%s_%d>", placeholderTokenPrefix, entityType, r.counters[entityType])
		entry := registryEntry{
			placeholder: ph,
			original:    original,
			policy:      policy,
		}
		if policy == PolicyMask {
			entry.wireForms = buildPlaceholderWireForms(ph)
			r.restoreOrder = append(r.restoreOrder, ph)
		}
		r.byPlaceholder[ph] = entry
		return ph
	default:
		return fmt.Sprintf("[REDACTED:%s]", entityType)
	}
}

func wirePlaceholderInner(ph string) (string, bool) {
	if len(ph) < 3 || ph[0] != '<' || ph[len(ph)-1] != '>' {
		return "", false
	}
	inner := ph[1 : len(ph)-1]
	if !strings.HasPrefix(inner, placeholderTokenPrefix) {
		return "", false
	}
	return inner, true
}

func bracedToken(inner string, open, close byte) string {
	return string([]byte{open}) + inner + string([]byte{close})
}

func spacedBracedToken(inner string, open, close byte) string {
	return string([]byte{open}) + " " + inner + " " + string([]byte{close})
}

func jsonUnicodeEscapedByte(b byte) string {
	return fmt.Sprintf(`\u%04x`, b)
}

func jsonUnicodeEscapedBracedToken(inner string, open, close byte) string {
	return jsonUnicodeEscapedByte(open) + inner + jsonUnicodeEscapedByte(close)
}

// jsonEscapedPlaceholder returns the form encoding/json uses for angle
// brackets inside JSON string values (\u003c / \u003e).
func jsonEscapedPlaceholder(ph string) string {
	s := strings.ReplaceAll(ph, "<", `\u003c`)
	return strings.ReplaceAll(s, ">", `\u003e`)
}

// htmlEscapedPlaceholder returns common HTML-entity escaping for angle brackets.
func htmlEscapedPlaceholder(ph string) string {
	s := strings.ReplaceAll(ph, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

func htmlEntitySquareBracedToken(inner string) string {
	return "&#91;" + inner + "&#93;"
}

func uniqueFormsSortedByLen(forms []string) []string {
	seen := make(map[string]struct{}, len(forms))
	var out []string
	for _, form := range forms {
		if form == "" {
			continue
		}
		if _, ok := seen[form]; ok {
			continue
		}
		seen[form] = struct{}{}
		out = append(out, form)
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i]) > len(out[j])
	})
	return out
}

// buildPlaceholderWireForms returns delimiter-bounded user-facing variants of a
// canonical wire placeholder. Bare inner tokens are intentionally excluded so
// numbered placeholders cannot prefix-collide (PII_PERSON_1 vs PII_PERSON_10).
func buildPlaceholderWireForms(ph string) []string {
	inner, ok := wirePlaceholderInner(ph)
	if !ok {
		return uniqueFormsSortedByLen([]string{ph, jsonEscapedPlaceholder(ph), htmlEscapedPlaceholder(ph)})
	}

	var forms []string
	add := func(s string) {
		if s != "" {
			forms = append(forms, s)
		}
	}

	for _, pair := range placeholderDelimiterPairs {
		lit := bracedToken(inner, pair.open, pair.close)
		add(lit)
		add(spacedBracedToken(inner, pair.open, pair.close))
		if pair.open == '<' {
			add(jsonEscapedPlaceholder(lit))
			add(htmlEscapedPlaceholder(lit))
		}
		if pair.open == '[' {
			add(htmlEntitySquareBracedToken(inner))
		}
		add(jsonUnicodeEscapedBracedToken(inner, pair.open, pair.close))
	}

	return uniqueFormsSortedByLen(forms)
}

func applyWireFormReplacements(text string, forms []string, original string) (string, int) {
	if text == "" || len(forms) == 0 {
		return text, 0
	}
	out := text
	var replaced int
	for {
		bestIdx := -1
		bestLen := 0
		for _, form := range forms {
			idx := strings.Index(out, form)
			if idx < 0 {
				continue
			}
			if bestIdx < 0 || idx < bestIdx || (idx == bestIdx && len(form) > bestLen) {
				bestIdx = idx
				bestLen = len(form)
			}
		}
		if bestIdx < 0 {
			break
		}
		replaced++
		out = out[:bestIdx] + original + out[bestIdx+bestLen:]
	}
	return out, replaced
}

func countPlaceholderForms(text string, forms []string) int {
	_, n := applyWireFormReplacements(text, forms, "")
	return n
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
	var restoredDelta int
	for _, ph := range order {
		r.mu.Lock()
		entry, ok := r.byPlaceholder[ph]
		r.mu.Unlock()
		if !ok || entry.policy != PolicyMask {
			continue
		}
		var delta int
		out, delta = applyWireFormReplacements(out, entry.wireForms, entry.original)
		restoredDelta += delta
	}
	if restoredDelta > 0 {
		r.mu.Lock()
		r.restoredCount += restoredDelta
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

// MaskPlaceholdersRemaining counts MASK-tier placeholder tokens still present
// in text after restore (any known delimiter-bounded variant).
func (r *Registry) MaskPlaceholdersRemaining(text string) int {
	if r == nil || text == "" {
		return 0
	}
	r.mu.Lock()
	order := append([]string(nil), r.restoreOrder...)
	r.mu.Unlock()
	if len(order) == 0 {
		return 0
	}
	var n int
	for _, ph := range order {
		r.mu.Lock()
		entry, ok := r.byPlaceholder[ph]
		r.mu.Unlock()
		if !ok || entry.policy != PolicyMask {
			continue
		}
		n += countPlaceholderForms(text, entry.wireForms)
	}
	return n
}

func looksLikePIIPlaceholderStart(b []byte) bool {
	i := 0
	for i < len(b) && b[i] == ' ' {
		i++
	}
	if i >= len(b) {
		return false
	}
	prefix := []byte(placeholderTokenPrefix)
	if len(b[i:]) < len(prefix) {
		return bytes.HasPrefix(prefix, b[i:])
	}
	return bytes.HasPrefix(b[i:], prefix)
}

func streamSafePrefixLen(combined []byte) int {
	safeLen := len(combined)
	if idx := bytes.LastIndex(combined, []byte(placeholderTokenPrefix)); idx >= 0 {
		tail := combined[idx:]
		if !piiPlaceholderTailRE.Match(tail) && idx < safeLen {
			safeLen = idx
		}
	}
	for _, pair := range placeholderDelimiterPairs {
		idx := bytes.LastIndexByte(combined, pair.open)
		if idx < 0 {
			continue
		}
		if bytes.IndexByte(combined[idx:], pair.close) >= 0 {
			continue
		}
		if looksLikePIIPlaceholderStart(combined[idx+1:]) && idx < safeLen {
			safeLen = idx
		}
	}
	return safeLen
}

// RestoreStreamChunk restores MASK placeholders in a streaming chunk,
// holding back a suffix that might be an incomplete placeholder token.
func (r *Registry) RestoreStreamChunk(chunk []byte, carry []byte) (emit []byte, newCarry []byte) {
	combined := append(append([]byte(nil), carry...), chunk...)
	safeLen := streamSafePrefixLen(combined)
	toProcess := combined[:safeLen]
	newCarry = combined[safeLen:]
	if len(newCarry) > maxPlaceholderCarry {
		toProcess = combined
		newCarry = nil
	}
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

// ExtractWirePlaceholderInner returns the PII-prefixed token inside a
// canonical wire placeholder such as "<PII_PERSON_1>".
func ExtractWirePlaceholderInner(ph string) (string, bool) {
	return wirePlaceholderInner(ph)
}

// ReformatWirePlaceholderDelimiters rewrites canonical wire placeholders using
// alternate surrounding delimiters while preserving the PII_ inner token.
func ReformatWirePlaceholderDelimiters(text string, open, close byte) string {
	return wirePlaceholderRE.ReplaceAllStringFunc(text, func(ph string) string {
		inner, ok := wirePlaceholderInner(ph)
		if !ok {
			return ph
		}
		return bracedToken(inner, open, close)
	})
}

// ReformatSpacedWirePlaceholderDelimiters rewrites wire placeholders with
// spaced delimiter variants, e.g. "[ PII_PERSON_1 ]".
func ReformatSpacedWirePlaceholderDelimiters(text string, open, close byte) string {
	return wirePlaceholderRE.ReplaceAllStringFunc(text, func(ph string) string {
		inner, ok := wirePlaceholderInner(ph)
		if !ok {
			return ph
		}
		return spacedBracedToken(inner, open, close)
	})
}

// ReformatWirePlaceholderList reformats a list of canonical wire placeholders.
func ReformatWirePlaceholderList(matches []string, open, close byte, spaced bool) string {
	out := make([]string, 0, len(matches))
	for _, ph := range matches {
		inner, ok := wirePlaceholderInner(ph)
		if !ok {
			out = append(out, ph)
			continue
		}
		if spaced {
			out = append(out, spacedBracedToken(inner, open, close))
		} else {
			out = append(out, bracedToken(inner, open, close))
		}
	}
	return strings.Join(out, " ")
}
