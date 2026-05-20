package middleware

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
			"event:ping":                 2,
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
		for i := 0; i < 50; i++ {
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

// ─── decompressForPreview ────────────────────────────────────────────────

func TestDecompressForPreview(t *testing.T) {
	t.Run("not_gzip_returns_error", func(t *testing.T) {
		_, err := decompressForPreview([]byte("plain text payload"))
		if err == nil {
			t.Fatal("expected error for non-gzip data")
		}
	})

	t.Run("too_short_returns_error", func(t *testing.T) {
		_, err := decompressForPreview([]byte{0x1f})
		if err == nil {
			t.Fatal("expected error for too-short data")
		}
	})

	t.Run("valid_gzip_roundtrips", func(t *testing.T) {
		original := []byte(`{"hello":"world","nested":{"x":1}}`)
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(original); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
		got, err := decompressForPreview(buf.Bytes())
		if err != nil {
			t.Fatalf("decompressForPreview: %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("roundtrip mismatch: got %q, want %q", got, original)
		}
	})

	t.Run("gzip_magic_but_corrupt_returns_error", func(t *testing.T) {
		// Magic bytes match (0x1f, 0x8b) but the rest is garbage — gzip.NewReader
		// should fail.
		_, err := decompressForPreview([]byte{0x1f, 0x8b, 0xff, 0xff, 0xff})
		if err == nil {
			t.Fatal("expected error for corrupt gzip data")
		}
	})

	t.Run("valid_header_but_truncated_body_returns_error", func(t *testing.T) {
		// A real gzip header followed by a truncated body drives the
		// "failed to decompress data" branch (gzip.NewReader succeeds, but
		// io.ReadAll on the inner reader fails).
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write([]byte("payload-that-will-be-truncated")); err != nil {
			t.Fatalf("gzip.Write: %v", err)
		}
		if err := gz.Close(); err != nil {
			t.Fatalf("gzip.Close: %v", err)
		}
		good := buf.Bytes()
		if len(good) < 18 {
			t.Skip("gzip output too small to truncate meaningfully")
		}
		// Drop the trailing CRC + length so the inner reader can't validate.
		if _, err := decompressForPreview(good[:len(good)-2]); err == nil {
			t.Fatal("expected error for truncated gzip body")
		}
	})
}

// ─── responseCapture: first-chunk gzip detection ──────────────────────

// TestResponseCapture_FirstChunkGzip_LogsCompressedWarning ensures the
// upstream-gzip warning fires the first time we see the gzip magic bytes,
// since the proxy strips Accept-Encoding by default and unexpected gzip
// upstream responses break SSE/event sniffing.
func TestResponseCapture_FirstChunkGzip_LogsCompressedWarning(t *testing.T) {
	rc := &responseCapture{
		ResponseWriter: httptest.NewRecorder(),
		body:           &bytes.Buffer{},
		isStreaming:    false,
		requestStart:   time.Now(),
	}
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	if _, err := gz.Write([]byte("gzipped-payload")); err != nil {
		t.Fatalf("gzip.Write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip.Close: %v", err)
	}

	logOut := captureLogOutput(func() {
		_, _ = rc.Write(gzBuf.Bytes())
	})
	if !rc.compressed {
		t.Error("compressed flag must be set on gzip first chunk")
	}
	if !strings.Contains(logOut, "WARNING upstream still sent gzip") {
		t.Errorf("expected gzip warning; got: %s", logOut)
	}
}
