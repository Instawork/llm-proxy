package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
)

// decompressPIIResponseIfGzip returns plain bytes when b is a complete gzip
// blob (magic 0x1f 0x8b). wasGzip is true when decompression ran successfully.
func decompressPIIResponseIfGzip(b []byte) (plain []byte, wasGzip bool, err error) {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, false, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b, false, err
	}
	defer gr.Close()
	out, err := io.ReadAll(gr)
	if err != nil {
		return b, false, err
	}
	return out, true, nil
}
