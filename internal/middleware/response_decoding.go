package middleware

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// maxDecodedResponseBytes bounds what the metering path inflates from one
// captured response, so a hostile or corrupt upstream cannot exhaust memory.
const maxDecodedResponseBytes = 64 << 20

// decodeResponseBody undoes a response's Content-Encoding so the metering
// path can parse it. Clients choose their own Accept-Encoding; the proxy
// forwards it untouched and decodes its own copy here. Multi-valued headers
// ("gzip, br") are applied in reverse order, as RFC 9110 specifies.
//
// On a read error the bytes decoded so far are returned alongside the error,
// because a captured stream can end mid-frame when the client hangs up.
func decodeResponseBody(contentEncoding string, body []byte) ([]byte, error) {
	codings := strings.Split(contentEncoding, ",")
	for i := len(codings) - 1; i >= 0; i-- {
		coding := strings.ToLower(strings.TrimSpace(codings[i]))
		if coding == "" || coding == "identity" {
			continue
		}
		decoded, err := decodeOne(coding, body)
		if err != nil {
			return decoded, err
		}
		body = decoded
	}
	return body, nil
}

func decodeOne(coding string, body []byte) ([]byte, error) {
	var reader io.Reader
	switch coding {
	case "gzip", "x-gzip":
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gr.Close()
		reader = gr
	case "br":
		reader = brotli.NewReader(bytes.NewReader(body))
	case "zstd":
		zr, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(maxDecodedResponseBytes))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		defer zr.Close()
		reader = zr
	case "deflate":
		// HTTP "deflate" is zlib-wrapped, but some servers send raw DEFLATE.
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			defer zr.Close()
			reader = zr
		} else {
			reader = flate.NewReader(bytes.NewReader(body))
		}
	default:
		return nil, fmt.Errorf("unsupported content-encoding %q", coding)
	}

	out, err := io.ReadAll(io.LimitReader(reader, maxDecodedResponseBytes+1))
	if err != nil {
		return out, fmt.Errorf("%s: %w", coding, err)
	}
	if len(out) > maxDecodedResponseBytes {
		return nil, fmt.Errorf("%s: decoded body exceeds %d bytes", coding, maxDecodedResponseBytes)
	}
	return out, nil
}
