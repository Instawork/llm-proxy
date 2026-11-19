package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── extractJSONType ─────────────────────────────────────────────────────

func TestExtractJSONType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"compact", `{"type":"thinking_delta","content":"hi"}`, "thinking_delta"},
		{"with_space", `{"type": "text_delta","content":"hi"}`, "text_delta"},
		{"missing", `{"foo":"bar"}`, ""},
		{"empty", ``, ""},
		{"non_json", `data: not_json`, ""},
		{"unterminated", `{"type":"unterminated`, ""},
		{
			// Beyond the 256-byte prefix scan, should return "".
			name: "outside_prefix",
			in:   strings.Repeat(" ", 300) + `"type":"late"`,
			want: "",
		},
		{
			// Type-name longer than the 64-char sanity cap should reject.
			name: "type_too_long",
			in:   `{"type":"` + strings.Repeat("a", 70) + `"}`,
			want: "",
		},
		{
			// Realistic OpenAI Responses API chunk.
			name: "openai_responses_event",
			in:   `{"type":"response.output_text.delta","delta":"Hi"}`,
			want: "response.output_text.delta",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractJSONType([]byte(tc.in)); got != tc.want {
				t.Errorf("extractJSONType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ─── chunkPreview ────────────────────────────────────────────────────────

func TestChunkPreview(t *testing.T) {
	cases := []struct {
		name   string
		in     []byte
		maxLen int
		want   string
	}{
		{"plain", []byte("hello"), 100, "hello"},
		{"newline_escaped", []byte("a\nb"), 100, `a\nb`},
		{"crlf_escaped", []byte("a\r\nb"), 100, `a\r\nb`},
		{"tab_escaped", []byte("a\tb"), 100, `a\tb`},
		{"non_printable_dotted", []byte{'h', 0x01, 0x02, 0xff, 'i'}, 100, "h...i"},
		{"truncated_with_ellipsis", []byte("abcdefghij"), 4, "abcd..."},
		{"empty", []byte{}, 100, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chunkPreview(tc.in, tc.maxLen); got != tc.want {
				t.Errorf("chunkPreview(%q,%d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
		})
	}
}

// ─── formatPerChunkEvents ────────────────────────────────────────────────

func TestFormatPerChunkEvents(t *testing.T) {
	t.Run("empty_returns_dash", func(t *testing.T) {
		if got := formatPerChunkEvents(map[string]int64{}); got != "-" {
			t.Errorf("want \"-\" for empty map, got %q", got)
		}
	})
	t.Run("singletons_omit_count", func(t *testing.T) {
		got := formatPerChunkEvents(map[string]int64{
			"event:ping": 1,
		})
		if got != "event:ping" {
			t.Errorf("singleton should omit ×N, got %q", got)
		}
	})
	t.Run("multi_count_uses_x", func(t *testing.T) {
		got := formatPerChunkEvents(map[string]int64{
			"event:ping":                2,
			"event:content_block_delta": 12,
		})
		// Sorted alphabetically.
		want := "event:content_block_delta×12,event:ping×2"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("stable_sort", func(t *testing.T) {
		// Same input twice must produce identical output despite map ordering.
		m := map[string]int64{"a": 2, "b": 3, "c": 1}
		first := formatPerChunkEvents(m)
		for range 50 {
			if got := formatPerChunkEvents(m); got != first {
				t.Fatalf("formatPerChunkEvents must be deterministic; got %q vs %q", got, first)
			}
		}
	})
}

// ─── responseCapture.formatEventCounts ───────────────────────────────────

func TestResponseCapture_FormatEventCounts(t *testing.T) {
	t.Run("empty_returns_dash", func(t *testing.T) {
		rc := &responseCapture{}
		if got := rc.formatEventCounts(); got != "-" {
			t.Errorf("want \"-\", got %q", got)
		}
	})
	t.Run("renders_events_and_data_types", func(t *testing.T) {
		rc := &responseCapture{
			sseEventCounts: map[string]int64{"ping": 2, "content_block_delta": 12},
			sseDataTypes:   map[string]int64{"text_delta": 10, "thinking_delta": 2},
		}
		got := rc.formatEventCounts()
		// Map iteration order is non-deterministic, so just verify all four
		// expected substrings are present and the format is comma-joined.
		expectedSubstrings := []string{
			"event:ping=2",
			"event:content_block_delta=12",
			"type:text_delta=10",
			"type:thinking_delta=2",
		}
		for _, sub := range expectedSubstrings {
			if !strings.Contains(got, sub) {
				t.Errorf("formatEventCounts() = %q, missing %q", got, sub)
			}
		}
		if strings.Count(got, ",") != len(expectedSubstrings)-1 {
			t.Errorf("expected %d commas, got %q", len(expectedSubstrings)-1, got)
		}
	})
}

// ─── sniffSSEEvents partial / split-line behaviour ───────────────────────

func TestSniffSSEEvents_LeftoverAcrossChunks(t *testing.T) {
	rc := &responseCapture{}

	// First chunk ends mid-line (no trailing \n).  Should NOT count anything
	// yet and should stash the partial line.
	first := []byte("event: ping\ndata: {\"type\":\"text_d")
	got1 := rc.sniffSSEEvents(first)
	if got1["event:ping"] != 1 {
		t.Fatalf("first chunk should count event:ping, got %v", got1)
	}
	if _, ok := got1["type:text_delta"]; ok {
		t.Fatalf("partial data line must not be counted yet, got %v", got1)
	}
	if len(rc.sseLeftover) == 0 {
		t.Fatal("expected sseLeftover to hold the partial trailing line")
	}

	// Second chunk completes the partial line.
	second := []byte("elta\",\"text\":\"hi\"}\n")
	got2 := rc.sniffSSEEvents(second)
	if got2["type:text_delta"] != 1 {
		t.Fatalf("second chunk should complete the data line and count type:text_delta, got %v", got2)
	}
	if rc.sseDataTypes["text_delta"] != 1 {
		t.Fatalf("cumulative sseDataTypes should reflect the completed line; got %v", rc.sseDataTypes)
	}
	if len(rc.sseLeftover) != 0 {
		t.Fatalf("sseLeftover must be cleared after completion; got %q", rc.sseLeftover)
	}
}

func TestSniffSSEEvents_HandlesCRLF(t *testing.T) {
	rc := &responseCapture{}
	// CRLF line endings (some upstreams emit them).
	in := []byte("event: ping\r\ndata: {\"type\":\"text_delta\"}\r\n")
	got := rc.sniffSSEEvents(in)
	if got["event:ping"] != 1 {
		t.Errorf("event:ping should be counted under CRLF, got %v", got)
	}
	if got["type:text_delta"] != 1 {
		t.Errorf("type:text_delta should be counted under CRLF, got %v", got)
	}
}

// ─── responseCapture.Hijack ──────────────────────────────────────────────

// nonHijackableWriter implements only http.ResponseWriter.
type nonHijackableWriter struct{ httptest.ResponseRecorder }

func TestResponseCapture_Hijack_UnsupportedReturnsErr(t *testing.T) {
	rc := &responseCapture{ResponseWriter: &nonHijackableWriter{}}
	conn, brw, err := rc.Hijack()
	if conn != nil || brw != nil {
		t.Fatalf("Hijack must return nils on non-hijackable writer; got conn=%v brw=%v", conn, brw)
	}
	// The middleware delegates to errors.ErrUnsupported (stdlib), not the
	// http-package's ErrNotSupported.  Both are acceptable signals to a caller
	// — pin to whichever the implementation actually uses.
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Hijack must return errors.ErrUnsupported on non-hijackable writer; got %v", err)
	}
}

// hijackableWriter implements both http.ResponseWriter and http.Hijacker.
type hijackableWriter struct {
	httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func TestResponseCapture_Hijack_DelegatesWhenSupported(t *testing.T) {
	hw := &hijackableWriter{}
	rc := &responseCapture{ResponseWriter: hw}
	if _, _, err := rc.Hijack(); err != nil {
		t.Fatalf("Hijack: unexpected err=%v", err)
	}
	if !hw.hijacked {
		t.Fatal("Hijack must delegate to underlying writer when supported")
	}
}

// ─── responseCapture: first-chunk gzip detection ──────────────────────

// TestResponseCapture_CompressedFromContentEncoding ensures the compressed
// flag (which disables SSE sniffing) follows the response Content-Encoding
// header rather than the body bytes.
func TestResponseCapture_CompressedFromContentEncoding(t *testing.T) {
	for _, tc := range []struct {
		encoding string
		want     bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"br", true},
		{"zstd", true},
	} {
		t.Run("encoding="+tc.encoding, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if tc.encoding != "" {
				rec.Header().Set("Content-Encoding", tc.encoding)
			}
			rc := &responseCapture{
				ResponseWriter: rec,
				body:           &bytes.Buffer{},
				requestStart:   time.Now(),
			}
			_, _ = rc.Write([]byte("payload"))
			if rc.compressed != tc.want {
				t.Errorf("compressed = %v, want %v", rc.compressed, tc.want)
			}
		})
	}
}
