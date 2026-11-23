// Package migrations embebe los archivos SQL de goose para aplicarlos al arrancar.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
