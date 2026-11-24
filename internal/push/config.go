package push

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

// Config son las claves VAPID y el contacto que viaja en cada push.
type Config struct {
	PublicKey  string // VAPID_PUBLIC_KEY: punto P-256 sin comprimir (65 bytes) en base64url
	PrivateKey string // VAPID_PRIVATE_KEY: escalar P-256 (32 bytes) en base64url
	Subject    string // VAPID_SUBJECT: "mailto:alguien@dominio" o una URL https
}

// ConfigFromEnv lee la configuración y la valida. getenv suele ser os.Getenv.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		PublicKey:  strings.TrimSpace(getenv("VAPID_PUBLIC_KEY")),
		PrivateKey: strings.TrimSpace(getenv("VAPID_PRIVATE_KEY")),
		Subject:    strings.TrimSpace(getenv("VAPID_SUBJECT")),
	}
	return c, c.Validate()
}

// Validate controla que estén las tres variables, que las claves tengan el
// formato correcto y que la pública corresponda a la privada (el error típico
// es pegarlas al revés o mezclar dos pares).
func (c Config) Validate() error {
	var missing []string
	for _, v := range []struct{ name, value string }{
		{"VAPID_PUBLIC_KEY", c.PublicKey},
		{"VAPID_PRIVATE_KEY", c.PrivateKey},
		{"VAPID_SUBJECT", c.Subject},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("faltan variables de push: %s (las claves se generan con `go run ./cmd/vapid`)", strings.Join(missing, ", "))
	}

	pub, err := decodeB64URL(c.PublicKey)
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		return errors.New("VAPID_PUBLIC_KEY inválida: tiene que ser un punto P-256 sin comprimir en base64url (87 caracteres)")
	}
	priv, err := decodeB64URL(c.PrivateKey)
	if err != nil || len(priv) != 32 {
		return errors.New("VAPID_PRIVATE_KEY inválida: tienen que ser 32 bytes en base64url (43 caracteres)")
	}
	key, err := ecdh.P256().NewPrivateKey(priv)
	if err != nil {
		return errors.New("VAPID_PRIVATE_KEY inválida: no es una clave P-256")
	}
	if !bytes.Equal(key.PublicKey().Bytes(), pub) {
		return errors.New("VAPID_PUBLIC_KEY no corresponde a VAPID_PRIVATE_KEY")
	}

	switch {
	case strings.HasPrefix(c.Subject, "mailto:"):
		if _, err := mail.ParseAddress(strings.TrimPrefix(c.Subject, "mailto:")); err != nil {
			return fmt.Errorf("VAPID_SUBJECT inválido: %q no es un mailto: con un email válido", c.Subject)
		}
	case strings.HasPrefix(c.Subject, "https://"):
		if u, err := url.Parse(c.Subject); err != nil || u.Host == "" {
			return fmt.Errorf("VAPID_SUBJECT inválido: %q no es una URL https válida", c.Subject)
		}
	default:
		return fmt.Errorf("VAPID_SUBJECT inválido: %q tiene que empezar con mailto: o https://", c.Subject)
	}
	return nil
}

// decodeB64URL acepta base64url con o sin relleno "=".
func decodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
