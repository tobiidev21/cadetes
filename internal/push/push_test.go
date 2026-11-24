package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
	"github.com/tobiidev21/cadetes/internal/orders"
)

func newKeys(t *testing.T) Config {
	t.Helper()
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return Config{PublicKey: pub, PrivateKey: priv, Subject: "mailto:soporte@example.com"}
}

func TestConfigValidate(t *testing.T) {
	good := newKeys(t)
	other := newKeys(t)
	if err := good.Validate(); err != nil {
		t.Fatalf("claves recién generadas: %v", err)
	}

	padded := good
	padded.PublicKey += "="
	padded.PrivateKey += "="
	if err := padded.Validate(); err != nil {
		t.Errorf("con relleno '=': %v", err)
	}
	https := good
	https.Subject = "https://cadetes.example.com"
	if err := https.Validate(); err != nil {
		t.Errorf("subject https: %v", err)
	}

	cases := map[string]struct {
		mod  func(*Config)
		want string
	}{
		"todo vacío":            {func(c *Config) { *c = Config{} }, "VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, VAPID_SUBJECT"},
		"sin subject":           {func(c *Config) { c.Subject = "" }, "faltan variables de push: VAPID_SUBJECT"},
		"pública rota":          {func(c *Config) { c.PublicKey = "abc" }, "VAPID_PUBLIC_KEY inválida"},
		"privada rota":          {func(c *Config) { c.PrivateKey = "abc" }, "VAPID_PRIVATE_KEY inválida"},
		"al revés":              {func(c *Config) { c.PublicKey, c.PrivateKey = c.PrivateKey, c.PublicKey }, "inválida"},
		"de pares distintos":    {func(c *Config) { c.PublicKey = other.PublicKey }, "no corresponde"},
		"subject sin esquema":   {func(c *Config) { c.Subject = "soporte@example.com" }, "mailto: o https://"},
		"subject http":          {func(c *Config) { c.Subject = "http://example.com" }, "mailto: o https://"},
		"mailto sin email":      {func(c *Config) { c.Subject = "mailto:" }, "email válido"},
		"mailto email inválido": {func(c *Config) { c.Subject = "mailto:no es un mail" }, "email válido"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := good
			c.mod(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, quería que dijera %q", err, c.want)
			}
		})
	}
}

func TestConfigFromEnvTrims(t *testing.T) {
	good := newKeys(t)
	env := map[string]string{
		"VAPID_PUBLIC_KEY":  " " + good.PublicKey + "\n",
		"VAPID_PRIVATE_KEY": good.PrivateKey + " ",
		"VAPID_SUBJECT":     good.Subject,
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil || cfg.PublicKey != good.PublicKey {
		t.Fatalf("cfg = %+v, err %v", cfg, err)
	}
}

func TestLibrarySubscriber(t *testing.T) {
	if got := librarySubscriber("mailto:a@example.com"); got != "a@example.com" {
		t.Errorf("mailto: %q", got)
	}
	if got := librarySubscriber("https://example.com"); got != "https://example.com" {
		t.Errorf("https: %q", got)
	}
}

// --- suscripción ---

type fakeStore struct {
	mu      sync.Mutex
	nextID  int64
	subs    map[string]db.PushSubscription // por endpoint
	deleted []int64
}

func newFakeStore() *fakeStore { return &fakeStore{subs: map[string]db.PushSubscription{}} }

func (f *fakeStore) UpsertPushSubscription(_ context.Context, arg db.UpsertPushSubscriptionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[arg.Endpoint]
	if !ok {
		f.nextID++
		s = db.PushSubscription{ID: f.nextID, Endpoint: arg.Endpoint, CreatedAt: time.Now()}
	}
	s.UserID, s.P256dh, s.Auth, s.UserAgent = arg.UserID, arg.P256dh, arg.Auth, arg.UserAgent
	f.subs[arg.Endpoint] = s
	return nil
}

func (f *fakeStore) ListPushSubscriptionsByUser(_ context.Context, userID int64) ([]db.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []db.PushSubscription{}
	for _, s := range f.subs {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b db.PushSubscription) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeStore) DeletePushSubscription(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.subs {
		if s.ID == id {
			delete(f.subs, k)
		}
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// device es un navegador de mentira: su par de claves y el secreto auth.
type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T) device {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return device{priv: priv, auth: a}
}

func (d device) input(endpoint string) SubscriptionInput {
	var in SubscriptionInput
	in.Endpoint = endpoint
	in.Keys.P256dh = base64.RawURLEncoding.EncodeToString(d.priv.PublicKey().Bytes())
	in.Keys.Auth = base64.RawURLEncoding.EncodeToString(d.auth)
	return in
}

func TestSubscribeEndpoints(t *testing.T) {
	dev := newDevice(t)
	ok := []string{
		"https://fcm.googleapis.com/fcm/send/abc:APA91b",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAAA",
		"https://web.push.apple.com/QGuQyavXutnMH",
		"https://wns2-par02p.notify.windows.com/w/?token=BQYAAA",
		"https://FCM.googleapis.com/fcm/send/mayusculas",
	}
	for _, e := range ok {
		svc := NewService(newFakeStore(), nil, "")
		if err := svc.Subscribe(context.Background(), 2, dev.input(e), "UA"); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	bad := []string{
		"",
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://evil.example.com/push",
		"https://fcm.googleapis.com.evil.example.com/push",
		"https://evilfcm.googleapis.com/push",
		"https://user:pass@fcm.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com:8443/fcm/send/abc",
		"https://127.0.0.1/push",
		"https://localhost/push",
		"https://fcm.googleapis.com/" + strings.Repeat("a", maxEndpointLen),
	}
	for _, e := range bad {
		store := newFakeStore()
		err := NewService(store, nil, "").Subscribe(context.Background(), 2, dev.input(e), "UA")
		var inputErr InputError
		if !errors.As(err, &inputErr) || len(store.subs) != 0 {
			t.Errorf("%.60q: err = %v, guardadas %d; quería InputError y nada guardado", e, err, len(store.subs))
		}
	}
}

func TestSubscribeKeys(t *testing.T) {
	const e = "https://fcm.googleapis.com/fcm/send/abc"
	dev := newDevice(t)
	cases := map[string]func(*SubscriptionInput){
		"p256dh vacía": func(in *SubscriptionInput) { in.Keys.P256dh = "" },
		"p256dh corta": func(in *SubscriptionInput) { in.Keys.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 33)) },
		"p256dh no punto": func(in *SubscriptionInput) {
			b := make([]byte, 65)
			b[0] = 4
			in.Keys.P256dh = base64.RawURLEncoding.EncodeToString(b)
		},
		"p256dh no base64": func(in *SubscriptionInput) { in.Keys.P256dh = "%%%" },
		"auth vacía":       func(in *SubscriptionInput) { in.Keys.Auth = "" },
		"auth de 15 bytes": func(in *SubscriptionInput) { in.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 15)) },
	}
	for name, mod := range cases {
		in := dev.input(e)
		mod(&in)
		err := NewService(newFakeStore(), nil, "").Subscribe(context.Background(), 2, in, "UA")
		var inputErr InputError
		if !errors.As(err, &inputErr) {
			t.Errorf("%s: err = %v, quería InputError", name, err)
		}
	}
}

func TestSubscribeUpsertMovesDevice(t *testing.T) {
	const e = "https://fcm.googleapis.com/fcm/send/abc"
	store := newFakeStore()
	svc := NewService(store, nil, "")
	dev := newDevice(t)
	ctx := context.Background()

	if err := svc.Subscribe(ctx, 2, dev.input(e), strings.Repeat("ñ", 400)); err != nil {
		t.Fatal(err)
	}
	if ua := store.subs[e].UserAgent; len(ua) > maxUserAgentLen || !strings.HasPrefix(ua, "ñ") || !utf8Valid(ua) {
		t.Errorf("user agent no recortado bien: %d bytes", len(ua))
	}
	// El mismo navegador, otro cadete: la fila pasa a ser del nuevo.
	if err := svc.Subscribe(ctx, 3, dev.input(e), "UA"); err != nil {
		t.Fatal(err)
	}
	if len(store.subs) != 1 || store.subs[e].UserID != 3 {
		t.Errorf("subs = %+v, quería una sola del usuario 3", store.subs)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// --- envío ---

type fakeSender struct {
	mu       sync.Mutex
	status   map[string]int    // por endpoint; 0 = error de red
	payloads map[string][]byte // lo último enviado a cada endpoint
}

func (f *fakeSender) Send(_ context.Context, sub db.PushSubscription, payload []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payloads == nil {
		f.payloads = map[string][]byte{}
	}
	f.payloads[sub.Endpoint] = payload
	st, ok := f.status[sub.Endpoint]
	if !ok {
		st = http.StatusCreated
	}
	if st == 0 {
		return 0, errors.New("timeout")
	}
	return st, nil
}

func subscribeAll(t *testing.T, svc *Service, userID int64, endpoints ...string) {
	t.Helper()
	for _, e := range endpoints {
		if err := svc.Subscribe(context.Background(), userID, newDevice(t).input(e), "UA"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSendToUserRemovesGoneSubscriptions(t *testing.T) {
	const base = "https://fcm.googleapis.com/fcm/send/"
	store := newFakeStore()
	sender := &fakeSender{status: map[string]int{
		base + "gone": http.StatusGone, base + "notfound": http.StatusNotFound,
		base + "error500": 500, base + "ratelimit": 429, base + "red": 0,
	}}
	svc := NewService(store, sender, "")
	subscribeAll(t, svc, 2, base+"ok", base+"gone", base+"notfound", base+"error500", base+"ratelimit", base+"red")
	subscribeAll(t, svc, 3, base+"otro-usuario")

	res, err := svc.SendToUser(context.Background(), 2, Message{Title: "t", URL: "/pedidos/1"})
	if err != nil {
		t.Fatal(err)
	}
	if res != (SendResult{Devices: 6, Sent: 1, Removed: 2}) {
		t.Errorf("resultado = %+v", res)
	}
	var left []string
	for e := range store.subs {
		left = append(left, strings.TrimPrefix(e, base))
	}
	slices.Sort(left)
	if want := []string{"error500", "ok", "otro-usuario", "ratelimit", "red"}; !slices.Equal(left, want) {
		t.Errorf("quedaron %v, quería %v (solo se borran 404 y 410)", left, want)
	}
	if _, ok := sender.payloads[base+"otro-usuario"]; ok {
		t.Error("se le mandó a un dispositivo de otro usuario")
	}
}

func TestOrderAssigned(t *testing.T) {
	const e = "https://fcm.googleapis.com/fcm/send/juan"
	sender := &fakeSender{}
	svc := NewService(newFakeStore(), sender, "")
	subscribeAll(t, svc, 2, e)

	courier := int64(2)
	svc.OrderAssigned(orders.Order{ID: 7, PickupAddress: "Lavalle 123", DropoffAddress: "Belgrano 456", CourierID: &courier})
	svc.OrderAssigned(orders.Order{ID: 8}) // sin cadete: no hace nada
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	var msg Message
	if err := json.Unmarshal(sender.payloads[e], &msg); err != nil {
		t.Fatalf("payload: %v (%q)", err, sender.payloads[e])
	}
	want := Message{Title: "Nuevo pedido N° 7", Body: "Retiro: Lavalle 123\nEntrega: Belgrano 456", URL: "/pedidos/7", Tag: "pedido-7"}
	if msg != want {
		t.Errorf("mensaje = %+v, quería %+v", msg, want)
	}
	if len(sender.payloads) != 1 {
		t.Errorf("envíos = %d, quería 1", len(sender.payloads))
	}
}

// TestWebPushSenderEndToEnd manda con webpush-go a un servicio push de
// prueba, descifra el cuerpo como lo haría el navegador (RFC 8291) y revisa
// las cabeceras y el JWT VAPID.
func TestWebPushSenderEndToEnd(t *testing.T) {
	cfg := newKeys(t)
	dev := newDevice(t)

	var (
		gotHeader http.Header
		gotBody   []byte
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirige" {
			http.Redirect(w, r, "/otro", http.StatusFound)
			return
		}
		if r.URL.Path == "/otro" {
			t.Error("se siguió una redirección")
		}
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sender := NewWebPushSender(cfg, nil)
	sender.client.Transport = srv.Client().Transport // confiar en el certificado de prueba

	payload := []byte(`{"title":"Nuevo pedido N° 1","url":"/pedidos/1"}`)
	in := dev.input(srv.URL + "/push/abc")
	sub := db.PushSubscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}

	status, err := sender.Send(context.Background(), sub, payload)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("Send: status %d, err %v", status, err)
	}

	for h, want := range map[string]string{
		"Urgency":          "high",
		"Ttl":              "3600",
		"Content-Encoding": "aes128gcm",
	} {
		if got := gotHeader.Get(h); got != want {
			t.Errorf("%s = %q, quería %q", h, got, want)
		}
	}

	authz := gotHeader.Get("Authorization")
	if !strings.HasPrefix(authz, "vapid t=") || !strings.HasSuffix(authz, ", k="+cfg.PublicKey) {
		t.Fatalf("Authorization = %q", authz)
	}
	jwt := strings.TrimSuffix(strings.TrimPrefix(authz, "vapid t="), ", k="+cfg.PublicKey)
	claimsJSON, err := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(claimsJSON, &claims)
	if claims.Sub != "mailto:soporte@example.com" {
		t.Errorf("sub del JWT = %q, quería mailto:soporte@example.com (sin mailto: repetido)", claims.Sub)
	}
	if claims.Aud != srv.URL {
		t.Errorf("aud = %q, quería %q", claims.Aud, srv.URL)
	}

	if got := decryptAES128GCM(t, dev, gotBody); !bytes.Equal(got, payload) {
		t.Errorf("descifrado = %q, quería %q", got, payload)
	}

	sub.Endpoint = srv.URL + "/redirige"
	if status, err := sender.Send(context.Background(), sub, payload); err != nil || status != http.StatusFound {
		t.Errorf("redirección: status %d, err %v; quería 302 sin seguirla", status, err)
	}
}

// decryptAES128GCM descifra un mensaje Web Push (RFC 8291 + RFC 8188).
func decryptAES128GCM(t *testing.T, dev device, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("cuerpo muy corto: %d", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idLen := int(body[20])
	asPublic := body[21 : 21+idLen]
	ciphertext := body[21+idLen:]
	if uint32(len(body)) > rs {
		t.Fatalf("más de un registro (%d > %d)", len(body), rs)
	}

	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := dev.priv.ECDH(asKey)
	if err != nil {
		t.Fatal(err)
	}
	keyInfo := "WebPush: info\x00" + string(dev.priv.PublicKey().Bytes()) + string(asPublic)
	ikm, _ := hkdf.Key(sha256.New, secret, dev.auth, keyInfo, 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)

	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("no se pudo descifrar: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatal("falta el delimitador de último registro")
	}
	return plain[:len(plain)-1]
}

// --- HTTP ---

func TestHTTPSubscribe(t *testing.T) {
	store := newFakeStore()
	h := NewHandler(NewService(store, nil, "CLAVE"))
	cadete := auth.User{ID: 2, Role: auth.RoleCadete}
	dev := newDevice(t)

	post := func(u *auth.User, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/push/suscripciones", strings.NewReader(body))
		req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14)")
		if u != nil {
			req = req.WithContext(auth.ContextWithUser(req.Context(), *u))
		}
		rec := httptest.NewRecorder()
		h.Subscribe(rec, req)
		return rec
	}

	// Tal cual lo arma PushSubscription.toJSON(), con expirationTime null.
	in := dev.input("https://fcm.googleapis.com/fcm/send/abc")
	body := `{"endpoint":"` + in.Endpoint + `","expirationTime":null,"keys":{"p256dh":"` + in.Keys.P256dh + `","auth":"` + in.Keys.Auth + `"}}`
	if rec := post(&cadete, body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, cuerpo %s", rec.Code, rec.Body)
	}
	if s := store.subs[in.Endpoint]; s.UserID != 2 || s.UserAgent != "Mozilla/5.0 (Linux; Android 14)" {
		t.Errorf("guardado = %+v", s)
	}

	if rec := post(&cadete, `{"endpoint":"https://evil.example.com/x","keys":{}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("endpoint ajeno: status = %d", rec.Code)
	}
	if rec := post(&cadete, `{"endpoint":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("JSON inválido: status = %d", rec.Code)
	}
	if rec := post(nil, body); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin usuario: status = %d", rec.Code)
	}

	rec := httptest.NewRecorder()
	h.PublicKey(rec, httptest.NewRequest(http.MethodGet, "/api/push/clave", nil))
	if !strings.Contains(rec.Body.String(), `"public_key":"CLAVE"`) {
		t.Errorf("clave: %s", rec.Body)
	}
}
