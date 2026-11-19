package providers

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// ErrUnsupportedContentEncoding is returned by DecodeContentEncoding for a
// coding the proxy has no decoder for.
var ErrUnsupportedContentEncoding = errors.New("unsupported content encoding")

// ErrDecodedBodyTooLarge is returned by DecodeContentEncoding when the
// decoded body would exceed the caller's limit.
var ErrDecodedBodyTooLarge = errors.New("decoded body exceeds limit")

// supportedContentCodings are the codings DecodeContentEncoding can reverse.
// NarrowAcceptEncoding offers upstream nothing outside this set, so a
// response the proxy cannot meter is never negotiated.
var supportedContentCodings = map[string]bool{
	"identity": true,
	"gzip":     true,
	"x-gzip":   true,
	"deflate":  true,
	"br":       true,
}

// NarrowAcceptEncoding drops the codings the proxy cannot decode from an
// Accept-Encoding value, keeping the remaining elements verbatim (order,
// q-values and spacing of each element). A "*" element is kept only with
// q=0, where it forbids unlisted codings rather than inviting them. Returns
// "" when nothing usable remains.
func NarrowAcceptEncoding(acceptEncoding string) string {
	kept := make([]string, 0, 4)
	for part := range strings.SplitSeq(acceptEncoding, ",") {
		element := strings.TrimSpace(part)
		if element == "" {
			continue
		}
		coding, params, _ := strings.Cut(element, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		switch {
		case supportedContentCodings[coding]:
			kept = append(kept, element)
		case coding == "*" && qualityIsZero(params):
			kept = append(kept, element)
		}
	}
	return strings.Join(kept, ", ")
}

// qualityIsZero reports whether an Accept-Encoding element's parameters
// carry q=0. A malformed q is treated as non-zero.
func qualityIsZero(params string) bool {
	for param := range strings.SplitSeq(params, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return err == nil && q == 0
	}
	return false
}

// DecodeContentEncoding reverses the codings in a Content-Encoding header
// value and returns the decoded body. An empty or identity value returns body
// itself without copying. Decoding stops at maxDecoded bytes with
// ErrDecodedBodyTooLarge.
//
// A decode error after some output (for example a body cut short by the
// capture cap) returns the partial output together with the error, so
// streaming callers can still parse the events that did arrive.
func DecodeContentEncoding(body []byte, contentEncoding string, maxDecoded int) ([]byte, error) {
	codings := parseContentCodings(contentEncoding)
	for _, coding := range codings {
		if !supportedContentCodings[coding] {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedContentEncoding, coding)
		}
	}
	out := body
	// Codings are listed in the order they were applied.
	for i := len(codings) - 1; i >= 0; i-- {
		if codings[i] == "identity" {
			continue
		}
		decoded, err := decodeOne(out, codings[i], maxDecoded)
		if err != nil {
			return decoded, err
		}
		out = decoded
	}
	return out, nil
}

// parseContentCodings splits a Content-Encoding value into lower-cased
// codings, ignoring empty elements.
func parseContentCodings(contentEncoding string) []string {
	var codings []string
	for part := range strings.SplitSeq(contentEncoding, ",") {
		if coding := strings.ToLower(strings.TrimSpace(part)); coding != "" {
			codings = append(codings, coding)
		}
	}
	return codings
}

func decodeOne(body []byte, coding string, maxDecoded int) ([]byte, error) {
	var r io.Reader
	switch coding {
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close() //nolint:errcheck
		r = gz
	case "deflate":
		// RFC 9110 "deflate" is zlib-wrapped, but some servers send raw
		// DEFLATE; fall back when the zlib header does not validate.
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer zr.Close() //nolint:errcheck
			r = zr
		} else {
			fr := flate.NewReader(bytes.NewReader(body))
			defer fr.Close() //nolint:errcheck
			r = fr
		}
	case "br":
		r = brotli.NewReader(bytes.NewReader(body))
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedContentEncoding, coding)
	}

	var buf bytes.Buffer
	n, err := buf.ReadFrom(io.LimitReader(r, int64(maxDecoded)+1))
	if n > int64(maxDecoded) {
		return nil, fmt.Errorf("%s: %w (%d bytes)", coding, ErrDecodedBodyTooLarge, maxDecoded)
	}
	if err != nil {
		return buf.Bytes(), fmt.Errorf("%s: %w", coding, err)
	}
	return buf.Bytes(), nil
}
