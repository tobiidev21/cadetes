// Package web embebe el frontend que sirve el binario.
package web

import (
	"embed"
	"io/fs"
	"mime"
)

func init() {
	// La imagen final (distroless) no trae /etc/mime.types y Go no conoce
	// esta extensión: sin esto el manifest saldría como text/plain.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

//go:embed static
var embedded embed.FS

// FS devuelve los archivos del frontend con static/ como raíz.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err) // solo falla si el directorio embebido no existe: error de compilación en la práctica
	}
	return sub
}
