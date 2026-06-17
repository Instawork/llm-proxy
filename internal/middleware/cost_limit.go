package middleware

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/providers"
)

const (
	costLimitReasonHeader = "X-Cost-Limit-Reason"
	costLimitCentsHeader  = "X-Cost-Limit-Cents"
	costSpendCentsHeader  = "X-Cost-Spend-Cents"
	costLimitExceeded     = "daily_cost_limit_exceeded"
)

// KeySpendReader returns rolling per-key spend for the current UTC day.
type KeySpendReader interface {
	KeySpendUSD(maskedKeyID string) float64
}

// CostLimitMiddleware blocks provider requests when an iw: key's recorded daily
// spend has reached its daily_cost_limit (cents). A limit of zero means unlimited.
func CostLimitMiddleware(pm *providers.ProviderManager, spend KeySpendReader) func(http.Handler) http.Handler {
	if spend == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prov := GetProviderFromRequest(pm, r)
			if prov == nil {
				next.ServeHTTP(w, r)
				return
			}

			rec, ok := apikeys.FromContext(r.Context())
			if !ok || rec == nil || rec.DailyCostLimit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			masked := MaskKeyID(rec.PK)
			spendUSD := spend.KeySpendUSD(masked)
			spendCents := int64(math.Ceil(spendUSD * 100))
			if spendCents < rec.DailyCostLimit {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set(costLimitReasonHeader, costLimitExceeded)
			w.Header().Set(costLimitCentsHeader, strconv.FormatInt(rec.DailyCostLimit, 10))
			w.Header().Set(costSpendCentsHeader, strconv.FormatInt(spendCents, 10))
			log.Printf("costlimit: block provider=%s key_prefix=%s spend_cents=%d limit_cents=%d",
				prov.GetName(), prefix(rec.PK), spendCents, rec.DailyCostLimit)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "daily cost limit exceeded",
			})
		})
	}
}
