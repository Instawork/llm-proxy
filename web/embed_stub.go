//go:build !embed_ui

package web

import "io/fs"

// FS returns an empty filesystem when the UI has not been embedded.
func FS() fs.FS {
	return nil
}
