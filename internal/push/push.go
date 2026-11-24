// Package push guarda las suscripciones de los dispositivos y les manda
// notificaciones Web Push (VAPID) cuando se asigna un pedido.
package push

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/tobiidev21/cadetes/internal/db"
	"github.com/tobiidev21/cadetes/internal/orders"
)

const (
	// TTL es cuánto guarda el servicio push el mensaje si el teléfono no está
	// conectado. webpush-go manda TTL 0 si no se indica, y con 0 un teléfono
	// sin señal en ese momento no lo recibe nunca.
	TTL = time.Hour

	// sendTimeout limita cada envío completo (todos los dispositivos).
	sendTimeout = 30 * time.Second

	maxEndpointLen  = 2048
	maxUserAgentLen = 512
)

// allowedPushHosts son los servicios push de los navegadores. El servidor
// hace un POST al endpoint que manda el navegador; limitarlo a estos dominios
// evita que alguien registre una URL interna y use el servidor de proxy.
var allowedPushHosts = []string{
	"fcm.googleapis.com",        // Chrome, Android, Samsung Internet, Opera
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari en iPhone y Mac (web.push.apple.com)
	"notify.windows.com",        // Edge en Windows
}

// InputError es un dato inválido que mandó el navegador; el mensaje se puede
// mostrar tal cual.
type InputError string

func (e InputError) Error() string { return string(e) }

// Message es lo que muestra la notificación. Lo lee el service worker.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"` // ruta que abre al tocarla
	Tag   string `json:"tag"` // el mismo tag reemplaza en vez de apilar
}

// SubscriptionInput es PushSubscription.toJSON() del navegador.
type SubscriptionInput struct {
	Endpoint       string   `json:"endpoint"`
	ExpirationTime *float64 `json:"expirationTime"` // lo manda el navegador; no se usa
	Keys           struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Store es lo que push necesita de la base. *db.Queries lo cumple.
type Store interface {
	UpsertPushSubscription(ctx context.Context, arg db.UpsertPushSubscriptionParams) error
	ListPushSubscriptionsByUser(ctx context.Context, userID int64) ([]db.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, id int64) error
}

// Sender manda un mensaje ya serializado a un dispositivo y devuelve el
// código HTTP del servicio push.
type Sender interface {
	Send(ctx context.Context, sub db.PushSubscription, payload []byte) (status int, err error)
}

// Service implementa suscripción y envío.
type Service struct {
	store     Store
	sender    Sender
	publicKey string
	wg        sync.WaitGroup
}

// NewService arma el servicio. publicKey es la VAPID pública que necesita el
// navegador para suscribirse.
func NewService(store Store, sender Sender, publicKey string) *Service {
	return &Service{store: store, sender: sender, publicKey: publicKey}
}

// PublicKey devuelve la clave VAPID pública.
func (s *Service) PublicKey() string { return s.publicKey }

// Subscribe guarda (o actualiza) la suscripción del dispositivo para el
// usuario. Si el endpoint ya estaba con otro usuario, pasa a este.
func (s *Service) Subscribe(ctx context.Context, userID int64, in SubscriptionInput, userAgent string) error {
	if err := validateEndpoint(in.Endpoint); err != nil {
		return err
	}
	p256dh, err := decodeB64URL(in.Keys.P256dh)
	if err != nil {
		return InputError("clave p256dh inválida")
	}
	if _, err := ecdh.P256().NewPublicKey(p256dh); err != nil {
		return InputError("clave p256dh inválida")
	}
	auth, err := decodeB64URL(in.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return InputError("clave auth inválida")
	}

	if err := s.store.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{
		UserID:    userID,
		Endpoint:  in.Endpoint,
		P256dh:    in.Keys.P256dh,
		Auth:      in.Keys.Auth,
		UserAgent: truncate(userAgent, maxUserAgentLen),
	}); err != nil {
		return fmt.Errorf("guardando suscripción: %w", err)
	}
	return nil
}

// SendResult resume un envío a todos los dispositivos de un usuario.
type SendResult struct {
	Devices int // suscripciones encontradas
	Sent    int // aceptadas por el servicio push
	Removed int // borradas porque el servicio respondió 404 o 410
}

// SendToUser manda el mensaje a todos los dispositivos del usuario. Las
// suscripciones que el servicio da por muertas (404/410) se borran; cualquier
// otro error se registra y se sigue con el resto.
func (s *Service) SendToUser(ctx context.Context, userID int64, msg Message) (SendResult, error) {
	var res SendResult
	payload, err := json.Marshal(msg)
	if err != nil {
		return res, fmt.Errorf("armando el mensaje: %w", err)
	}
	subs, err := s.store.ListPushSubscriptionsByUser(ctx, userID)
	if err != nil {
		return res, fmt.Errorf("buscando suscripciones: %w", err)
	}
	res.Devices = len(subs)

	for _, sub := range subs {
		status, err := s.sender.Send(ctx, sub, payload)
		switch {
		case err != nil:
			slog.Warn("push: no se pudo enviar", "suscripcion", sub.ID, "usuario", userID, "err", err)
		case status >= 200 && status < 300:
			res.Sent++
		case status == http.StatusNotFound || status == http.StatusGone:
			if err := s.store.DeletePushSubscription(ctx, sub.ID); err != nil {
				slog.Warn("push: no se pudo borrar la suscripción vencida", "suscripcion", sub.ID, "err", err)
				continue
			}
			res.Removed++
			slog.Info("push: suscripción vencida borrada", "suscripcion", sub.ID, "usuario", userID, "status", status)
		default:
			slog.Warn("push: el servicio rechazó el envío", "suscripcion", sub.ID, "usuario", userID, "status", status, "host", endpointHost(sub.Endpoint))
		}
	}
	return res, nil
}

// OrderAssigned avisa al cadete del pedido. Vuelve enseguida: el envío corre
// en otra goroutine para no demorar la respuesta al admin. Cumple
// orders.Notifier.
func (s *Service) OrderAssigned(o orders.Order) {
	if o.CourierID == nil {
		return
	}
	courierID := *o.CourierID
	msg := Message{
		Title: fmt.Sprintf("Nuevo pedido N° %d", o.ID),
		Body:  "Retiro: " + o.PickupAddress + "\nEntrega: " + o.DropoffAddress,
		URL:   fmt.Sprintf("/pedidos/%d", o.ID),
		Tag:   fmt.Sprintf("pedido-%d", o.ID),
	}
	s.wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		start := time.Now()
		res, err := s.SendToUser(ctx, courierID, msg)
		if err != nil {
			slog.Error("push: pedido asignado", "pedido", o.ID, "cadete", courierID, "err", err)
			return
		}
		slog.Info("push: pedido asignado", "pedido", o.ID, "cadete", courierID,
			"dispositivos", res.Devices, "enviados", res.Sent, "borrados", res.Removed,
			"duracion", time.Since(start).Round(time.Millisecond))
	})
}

// Shutdown espera a que terminen los envíos en curso, o a que venza ctx.
func (s *Service) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("quedaron envíos push sin terminar")
	}
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" || len(endpoint) > maxEndpointLen {
		return InputError("endpoint inválido")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return InputError("endpoint inválido: tiene que ser https")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowedPushHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return InputError("servicio push no soportado: " + host)
}

func endpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// WebPushSender manda con webpush-go: cifrado aes128gcm, firma VAPID y
// urgencia alta (el teléfono lo entrega aunque esté en ahorro de batería).
type WebPushSender struct {
	cfg    Config
	client *http.Client
}

// NewWebPushSender arma el sender. client puede ser nil.
func NewWebPushSender(cfg Config, client *http.Client) *WebPushSender {
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			// Los servicios push no redirigen; seguir redirecciones solo
			// serviría para mandar el POST a otro lado.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &WebPushSender{cfg: cfg, client: client}
}

// Send cumple Sender.
func (w *WebPushSender) Send(ctx context.Context, sub db.PushSubscription, payload []byte) (int, error) {
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      w.client,
		Subscriber:      librarySubscriber(w.cfg.Subject),
		TTL:             int(TTL / time.Second),
		Urgency:         webpush.UrgencyHigh,
		VAPIDPublicKey:  w.cfg.PublicKey,
		VAPIDPrivateKey: w.cfg.PrivateKey,
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// librarySubscriber adapta VAPID_SUBJECT a webpush-go v1.4, que le antepone
// "mailto:" a todo lo que no empiece con "https:". Sin esto el JWT llevaría
// "mailto:mailto:..." y Apple lo rechaza.
func librarySubscriber(subject string) string {
	return strings.TrimPrefix(subject, "mailto:")
}
