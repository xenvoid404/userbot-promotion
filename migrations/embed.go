// Package migrations meng-embed semua file SQL ke dalam binary
// sehingga tidak perlu menyertakan folder migrations saat deploy.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
