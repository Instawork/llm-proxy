package admin

import (
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
)

const scopeSuffixLen = 4

// RedactScopeKey returns a display-safe rate-limit scope string. Secrets and
// client IPs are collapsed to a last-N suffix so operators can correlate rows
// without exposing full credentials in JSON or the admin UI.
func RedactScopeKey(scope string) string {
	if scope == "" || scope == "global" {
		return scope
	}
	idx := strings.Index(scope, ":")
	if idx < 0 {
		return scope
	}
	kind := scope[:idx]
	rest := scope[idx+1:]
	switch kind {
	case "key":
		return "key:" + redactScopeSecret(rest)
	case "user":
		if strings.HasPrefix(rest, "ip:") {
			return redactUserIPScope(rest)
		}
		return "user:" + redactScopeSecret(rest)
	default:
		return scope
	}
}

func redactScopeSecret(value string) string {
	body := apikeys.TrimKeyPrefix(value)
	if len(body) <= scopeSuffixLen {
		return "••••"
	}
	return "••••" + body[len(body)-scopeSuffixLen:]
}

func redactUserIPScope(rest string) string {
	ipPart := strings.TrimPrefix(rest, "ip:")
	lastColon := strings.LastIndex(ipPart, ":")
	if lastColon >= 0 {
		port := ipPart[lastColon+1:]
		return "user:•••.•••.•••.•:" + port
	}
	return "user:••••"
}

func sanitizeLimitsSnapshot(snap *ratelimit.LimitsSnapshot) {
	if snap == nil {
		return
	}
	if snap.Minute != nil {
		snap.Minute.Counters = mergeRedactedCounters(snap.Minute.Counters)
	}
	if snap.Day != nil {
		snap.Day.Counters = mergeRedactedCounters(snap.Day.Counters)
	}
}

func mergeRedactedCounters(in map[string]ratelimit.CounterSnapshot) map[string]ratelimit.CounterSnapshot {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]ratelimit.CounterSnapshot, len(in))
	for scope, c := range in {
		key := RedactScopeKey(scope)
		existing := out[key]
		existing.Requests += c.Requests
		existing.Tokens += c.Tokens
		out[key] = existing
	}
	return out
}

func sanitizeRateLimitOverrides(o config.RateLimitOverrides) config.RateLimitOverrides {
	if len(o.PerKey) == 0 && len(o.PerUser) == 0 && len(o.PerModel) == 0 {
		return o
	}
	out := o
	if len(o.PerKey) > 0 {
		out.PerKey = make(map[string]config.LimitsConfig, len(o.PerKey))
		for k, v := range o.PerKey {
			out.PerKey[RedactScopeKey("key:"+k)] = v
		}
	}
	if len(o.PerUser) > 0 {
		out.PerUser = make(map[string]config.LimitsConfig, len(o.PerUser))
		for k, v := range o.PerUser {
			out.PerUser[RedactScopeKey("user:"+k)] = v
		}
	}
	return out
}
