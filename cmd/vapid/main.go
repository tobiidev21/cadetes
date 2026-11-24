// Comando vapid: genera un par de claves VAPID para Web Push.
//
//	go run ./cmd/vapid
//
// Imprime las líneas listas para pegar como variables de entorno. Se genera
// una sola vez por ambiente: si se cambian, todas las suscripciones existentes
// dejan de servir y cada cadete tiene que volver a activar las notificaciones.
package main

import (
	"fmt"
	"os"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func main() {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "no se pudieron generar las claves:", err)
		os.Exit(1)
	}
	fmt.Println("VAPID_PUBLIC_KEY=" + pub)
	fmt.Println("VAPID_PRIVATE_KEY=" + priv)
	fmt.Fprintln(os.Stderr, "\nFalta VAPID_SUBJECT: un mailto: con un email real tuyo, p. ej. VAPID_SUBJECT=mailto:vos@tudominio.com")
	fmt.Fprintln(os.Stderr, "La clave privada es secreta: no la commitees.")
}
