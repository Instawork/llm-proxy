package live

import (
	"fmt"
	"os"
	"strings"
)

func (r *Runner) logf(format string, args ...any) {
	if r == nil || !r.cfg.Verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "… %s\n", fmt.Sprintf(format, args...))
}

func maskProxyKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "(empty)"
	}
	if len(key) <= 16 {
		return key
	}
	return key[:10] + "…" + key[len(key)-6:]
}

func snippetShareLine(share ShareContext) string {
	return fmt.Sprintf("provider=%s base=%s key=%s", share.Provider, share.BaseURL, maskProxyKey(share.Key))
}
