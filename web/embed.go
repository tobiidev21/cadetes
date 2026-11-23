// Package web embebe el frontend que sirve el binario.
package web

import (
	"embed"
	"io/fs"
)

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
