package providers

import (
	"net/http"
	"strings"
	"testing"
)

func TestAnthropicUpstreamSkipReason(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantOK     bool
		wantSubstr string
	}{
		{
			name:   "ok",
			status: http.StatusOK,
			body:   `{}`,
			wantOK: false,
		},
		{
			name:       "api_error_500",
			status:     http.StatusInternalServerError,
			body:       `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
			wantOK:     true,
			wantSubstr: "api_error",
		},
		{
			name:       "overloaded_529",
			status:     529,
			body:       `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantOK:     true,
			wantSubstr: "overloaded",
		},
		{
			name:   "client_error_400",
			status: http.StatusBadRequest,
			body:   `{"type":"error","error":{"type":"invalid_request_error"}}`,
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := anthropicUpstreamSkipReason(tc.status, []byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if tc.wantOK && tc.wantSubstr != "" && !strings.Contains(reason, tc.wantSubstr) {
				t.Fatalf("reason %q missing %q", reason, tc.wantSubstr)
			}
		})
	}
}
