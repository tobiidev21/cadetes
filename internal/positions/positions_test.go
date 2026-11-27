package positions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
)

// fakeStore es una base en memoria que imita InsertPositions (con el índice
// único por cadete y hora de captura).
type fakeStore struct {
	mu     sync.Mutex
	nextID int64
	rows   []db.Position
	calls  []db.InsertPositionsParams
	fail   error
}

func (f *fakeStore) InsertPositions(_ context.Context, arg db.InsertPositionsParams) ([]db.Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, arg)
	if f.fail != nil {
		return nil, f.fail
	}
	var in []row
	if err := json.Unmarshal(arg.Rows, &in); err != nil {
		return nil, err
	}
	out := []db.Position{}
	for _, r := range in {
		dup := false
		for _, e := range f.rows {
			if e.CourierID == arg.CourierID && e.CapturedAt.Equal(r.CapturedAt) {
				dup = true
			}
		}
		if dup {
			continue
		}
		f.nextID++
		p := db.Position{
			ID: f.nextID, CourierID: arg.CourierID, Lat: r.Lat, Lng: r.Lng, AccuracyM: r.AccuracyM,
			SpeedMs: r.SpeedMs, HeadingDeg: r.HeadingDeg, CapturedAt: r.CapturedAt,
			ReceivedAt: time.Now(), Source: string(r.Source), Visible: r.Visible,
		}
		f.rows = append(f.rows, p)
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) LatestPositions(context.Context) ([]db.LatestPositionsRow, error) {
	return []db.LatestPositionsRow{}, nil
}

func (f *fakeStore) CourierTrack(context.Context, db.CourierTrackParams) ([]db.Position, error) {
	return []db.Position{}, nil
}

func (f *fakeStore) CourierSummary(context.Context, db.CourierSummaryParams) (db.CourierSummaryRow, error) {
	return db.CourierSummaryRow{}, nil
}

var (
	testNow = time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	admin   = auth.User{ID: 1, Username: "admin", Name: "Administrador", Role: auth.RoleAdmin}
	juan    = auth.User{ID: 2, Username: "juan", Name: "Juan", Role: auth.RoleCadete}
)

func ptr[T any](v T) *T { return &v }

// valid es una posición válida a testNow.
func valid() Input {
	return Input{
		Lat: ptr(-35.5772), Lng: ptr(-58.0094), PrecisionM: ptr(float32(8.5)),
		CapturadaEn: ptr(testNow), Origen: SourcePWA, Visible: ptr(true),
	}
}

func newTestService(store Store) (*Service, *Broker) {
	b := NewBroker()
	svc := NewService(store, b)
	svc.now = func() time.Time { return testNow }
	return svc, b
}

func TestValidate(t *testing.T) {
	many := make([]Input, MaxBatch+1)
	for i := range many {
		many[i] = valid()
	}
	with := func(f func(*Input)) []Input {
		a, b := valid(), valid()
		f(&b)
		return []Input{a, b}
	}
	cases := []struct {
		name string
		in   []Input
		want string // "" = válido
	}{
		{"una válida", []Input{valid()}, ""},
		{"100 pasan", many[:MaxBatch], ""},
		{"opcionales vacíos", with(func(p *Input) { p.Visible, p.VelocidadMS, p.RumboGrados = nil, nil, nil }), ""},
		{"nativa", with(func(p *Input) { p.Origen = SourceNativa }), ""},
		{"bordes", with(func(p *Input) {
			p.Lat, p.Lng, p.PrecisionM, p.VelocidadMS, p.RumboGrados = ptr(-90.0), ptr(180.0), ptr(float32(0)), ptr(float32(0)), ptr(float32(359.9))
		}), ""},
		{"59 s en el futuro", with(func(p *Input) { p.CapturadaEn = ptr(testNow.Add(59 * time.Second)) }), ""},
		{"23 h 59 m atrás", with(func(p *Input) { p.CapturadaEn = ptr(testNow.Add(-24*time.Hour + time.Minute)) }), ""},
		{"vacío", []Input{}, "el lote no tiene posiciones"},
		{"101", many, "el lote tiene 101 posiciones y el máximo es 100"},
		{"sin lat", with(func(p *Input) { p.Lat = nil }), "posición 2: falta la latitud (lat)"},
		{"lat > 90", with(func(p *Input) { p.Lat = ptr(90.0001) }), "posición 2: la latitud tiene que estar entre -90 y 90"},
		{"lat < -90", with(func(p *Input) { p.Lat = ptr(-91.0) }), "posición 2: la latitud tiene que estar entre -90 y 90"},
		{"sin lng", with(func(p *Input) { p.Lng = nil }), "posición 2: falta la longitud (lng)"},
		{"lng < -180", with(func(p *Input) { p.Lng = ptr(-180.5) }), "posición 2: la longitud tiene que estar entre -180 y 180"},
		{"sin precisión", with(func(p *Input) { p.PrecisionM = nil }), "posición 2: falta la precisión (precision_m)"},
		{"precisión negativa", with(func(p *Input) { p.PrecisionM = ptr(float32(-1)) }), "posición 2: la precisión no puede ser negativa"},
		{"velocidad negativa", with(func(p *Input) { p.VelocidadMS = ptr(float32(-0.1)) }), "posición 2: la velocidad no puede ser negativa"},
		{"rumbo 360", with(func(p *Input) { p.RumboGrados = ptr(float32(360)) }), "posición 2: el rumbo tiene que estar entre 0 y 360 (sin incluir 360)"},
		{"rumbo negativo", with(func(p *Input) { p.RumboGrados = ptr(float32(-1)) }), "posición 2: el rumbo tiene que estar entre 0 y 360 (sin incluir 360)"},
		{"sin hora", with(func(p *Input) { p.CapturadaEn = nil }), "posición 2: falta la hora de captura (capturada_en)"},
		{"61 s en el futuro", with(func(p *Input) { p.CapturadaEn = ptr(testNow.Add(61 * time.Second)) }), "posición 2: capturada_en está más de 1 minuto en el futuro (¿la hora del celular está bien?)"},
		{"24 h + 1 s atrás", with(func(p *Input) { p.CapturadaEn = ptr(testNow.Add(-24*time.Hour - time.Second)) }), "posición 2: capturada_en tiene más de 24 h"},
		{"origen inválido", with(func(p *Input) { p.Origen = "ios" }), `posición 2: origen inválido "ios": tiene que ser "pwa" o "nativa"`},
		{"sin origen", with(func(p *Input) { p.Origen = "" }), `posición 2: origen inválido "": tiene que ser "pwa" o "nativa"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.in, testNow)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate = %v, quería nil", err)
				}
				return
			}
			var ie InputError
			if !errors.As(err, &ie) || ie.Error() != tc.want {
				t.Fatalf("Validate = %v, quería %q", err, tc.want)
			}
		})
	}
}

func TestRecordRejectsWholeBatch(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	bad := valid()
	bad.Lat = ptr(100.0)
	_, err := svc.Record(context.Background(), juan, []Input{valid(), bad, valid()})
	var ie InputError
	if !errors.As(err, &ie) {
		t.Fatalf("Record = %v, quería InputError", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("se llamó a la base %d veces con un lote inválido", len(store.calls))
	}
}

func TestRecordUsesSessionCourierAndPublishes(t *testing.T) {
	store := &fakeStore{}
	svc, b := newTestService(store)
	sub := b.Subscribe()
	defer sub.Cancel()

	in := []Input{valid(), valid()}
	in[1].CapturadaEn = ptr(testNow.Add(-5 * time.Second))
	n, err := svc.Record(context.Background(), juan, in)
	if err != nil || n != 2 {
		t.Fatalf("Record = %d, %v", n, err)
	}
	if got := store.calls[0].CourierID; got != juan.ID {
		t.Fatalf("courier_id = %d, quería %d", got, juan.ID)
	}

	// Reenviar el mismo lote no guarda ni publica nada nuevo.
	if n, err := svc.Record(context.Background(), juan, in); err != nil || n != 0 {
		t.Fatalf("reenvío: Record = %d, %v; quería 0", n, err)
	}

	select {
	case msg := <-sub.C:
		ps := parseEvent(t, string(msg))
		if len(ps) != 2 || ps[0].CadeteID != juan.ID || ps[0].CadeteNombre != "Juan" {
			t.Fatalf("evento = %+v", ps)
		}
	default:
		t.Fatal("no se publicó el lote")
	}
	select {
	case msg := <-sub.C:
		t.Fatalf("el reenvío publicó %q", msg)
	default:
	}
}

// parseEvent lee un evento SSE "posiciones".
func parseEvent(t *testing.T, msg string) []Position {
	t.Helper()
	rest, ok := strings.CutPrefix(msg, "event: posiciones\ndata: ")
	if !ok || !strings.HasSuffix(rest, "\n\n") {
		t.Fatalf("evento mal formado: %q", msg)
	}
	var ps []Position
	if err := json.Unmarshal([]byte(strings.TrimSuffix(rest, "\n\n")), &ps); err != nil {
		t.Fatalf("data: %v", err)
	}
	return ps
}

func TestToSummaryNulls(t *testing.T) {
	since, until := testNow.Add(-time.Hour), testNow
	empty := toSummary(2, since, until, db.CourierSummaryRow{FirstAt: since, LastAt: since})
	if empty.Primera != nil || empty.Ultima != nil || empty.IntervaloMedioS != nil || empty.DemoraMediaS != nil ||
		empty.DemoraMaxS != nil || empty.PrecisionMediaM != nil || empty.Huecos60s.MayorS != nil {
		t.Fatalf("sin posiciones quedaron valores: %+v", empty)
	}
	one := toSummary(2, since, until, db.CourierSummaryRow{Total: 1, FirstAt: since, LastAt: since, AvgDelayS: 1.23456, AvgAccuracyM: 5})
	if one.IntervaloMedioS != nil || one.DemoraMediaS == nil || *one.DemoraMediaS != 1.235 || one.Huecos60s.MayorS != nil {
		t.Fatalf("con una posición: %+v", one)
	}
}

func TestLimiter(t *testing.T) {
	now := testNow
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := range limiterBurst {
		if ok, _ := l.Allow(juan.ID); !ok {
			t.Fatalf("envío %d rechazado dentro de la ráfaga", i+1)
		}
	}
	ok, wait := l.Allow(juan.ID)
	if ok || wait != limiterEvery {
		t.Fatalf("después de la ráfaga: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow(99); !ok {
		t.Fatal("otro cadete no tiene que verse afectado")
	}
	now = now.Add(limiterEvery)
	if ok, _ := l.Allow(juan.ID); !ok {
		t.Fatal("pasado el intervalo tiene que volver a pasar")
	}
	if ok, _ := l.Allow(juan.ID); ok {
		t.Fatal("con un solo permiso repuesto, el segundo tiene que rechazarse")
	}
}

func TestCreateRateLimited(t *testing.T) {
	h := NewHandler(func() *Service { s, _ := newTestService(&fakeStore{}); return s }())
	body := func(i int) string {
		return fmt.Sprintf(`{"posiciones":[{"lat":1,"lng":2,"precision_m":3,"capturada_en":%q,"origen":"pwa"}]}`,
			testNow.Add(-time.Duration(i)*time.Second).Format(time.RFC3339Nano))
	}
	for i := range limiterBurst {
		rec := postBatch(h, juan, body(i))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("envío %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	rec := postBatch(h, juan, body(99))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("después de la ráfaga: %d, Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func postBatch(h *Handler, u auth.User, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/posiciones", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec
}

func TestCreateHTTP(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newTestService(store)
	h := NewHandler(svc)
	at := testNow.Format(time.RFC3339Nano)

	t.Run("204 y cadete de la sesión", func(t *testing.T) {
		rec := postBatch(h, juan, `{"posiciones":[{"lat":-35.5,"lng":-58.0,"precision_m":4,"velocidad_ms":null,"capturada_en":"`+at+`","origen":"nativa"}]}`)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if store.calls[len(store.calls)-1].CourierID != juan.ID {
			t.Fatal("no se usó el cadete de la sesión")
		}
	})
	t.Run("cadete_id en el body da 400", func(t *testing.T) {
		before := len(store.calls)
		rec := postBatch(h, juan, `{"cadete_id":99,"posiciones":[{"lat":1,"lng":2,"precision_m":3,"capturada_en":"`+at+`","origen":"pwa"}]}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `campo desconocido \"cadete_id\"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		rec = postBatch(h, juan, `{"posiciones":[{"cadete_id":99,"lat":1,"lng":2,"precision_m":3,"capturada_en":"`+at+`","origen":"pwa"}]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cadete_id en la posición: %d %s", rec.Code, rec.Body)
		}
		if len(store.calls) != before {
			t.Fatal("se guardó algo")
		}
	})
	t.Run("lote inválido da 400 con mensaje", func(t *testing.T) {
		rec := postBatch(h, juan, `{"posiciones":[{"lat":91,"lng":2,"precision_m":3,"capturada_en":"`+at+`","origen":"pwa"}]}`)
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusBadRequest || out["error"] != "posición 1: la latitud tiene que estar entre -90 y 90" {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("JSON roto da 400", func(t *testing.T) {
		if rec := postBatch(h, juan, `{"posiciones":[{"lat":"x"}]}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("falla la base da 500 sin detalles", func(t *testing.T) {
		store.fail = errors.New("conexión rota")
		defer func() { store.fail = nil }()
		rec := postBatch(h, juan, `{"posiciones":[{"lat":1,"lng":2,"precision_m":3,"capturada_en":"`+testNow.Add(-time.Minute).Format(time.RFC3339Nano)+`","origen":"pwa"}]}`)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "conexión") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
}

func TestQueryParams(t *testing.T) {
	svc, _ := newTestService(&fakeStore{})
	h := NewHandler(svc)
	cases := []struct {
		target string
		want   int
	}{
		{"/api/posiciones/recorrido?cadete=2&desde=2026-11-25T14:00:00-03:00", http.StatusOK},
		{"/api/posiciones/recorrido?desde=2026-11-25T14:00:00Z", http.StatusBadRequest},
		{"/api/posiciones/recorrido?cadete=abc&desde=2026-11-25T14:00:00Z", http.StatusBadRequest},
		{"/api/posiciones/recorrido?cadete=2", http.StatusBadRequest},
		{"/api/posiciones/recorrido?cadete=2&desde=ayer", http.StatusBadRequest},
		{"/api/posiciones/resumen?cadete=2&desde=2026-11-25T14:00:00Z", http.StatusOK},
		{"/api/posiciones/resumen?cadete=2&desde=2026-11-25T14:00:00Z&hasta=2026-11-25T14:30:00Z", http.StatusOK},
		{"/api/posiciones/resumen?cadete=2&desde=2026-11-25T14:00:00Z&hasta=2026-11-25T13:00:00Z", http.StatusBadRequest},
		{"/api/posiciones/resumen?cadete=2&desde=2026-11-01T00:00:00Z&hasta=2026-11-25T00:00:00Z", http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		rec := httptest.NewRecorder()
		if strings.Contains(tc.target, "recorrido") {
			h.Track(rec, req)
		} else {
			h.Summary(rec, req)
		}
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s, quería %d", tc.target, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestBrokerSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe() // nunca lee
	fast := b.Subscribe() // lee cada mensaje

	done := make(chan int)
	go func() {
		n := 0
		for range subscriberBuffer + 10 {
			b.Publish([]byte("x"))
			<-fast.C
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != subscriberBuffer+10 {
			t.Fatalf("el suscriptor rápido recibió %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish se bloqueó con un suscriptor que no lee")
	}

	if dropped := slow.Cancel(); dropped != 10 {
		t.Fatalf("descartados = %d, quería 10", dropped)
	}
	if fast.Cancel() != 0 {
		t.Fatal("al suscriptor rápido no se le tenía que descartar nada")
	}
	n := 0
	for range slow.C {
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("el lento conservaba %d mensajes, quería %d", n, subscriberBuffer)
	}
	slow.Cancel() // dos veces no rompe
	if b.Subscribers() != 0 {
		t.Fatal("quedaron suscriptores")
	}
	b.Close()
	b.Close()
	if _, ok := <-b.Subscribe().C; ok {
		t.Fatal("suscribirse a un broker cerrado tiene que dar un canal cerrado")
	}
}

// sseReader lee el stream línea por línea en otra goroutine.
func sseReader(t *testing.T, resp *http.Response) <-chan string {
	t.Helper()
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return lines
}

func waitLine(t *testing.T, lines <-chan string, prefix string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("el stream se cerró esperando %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-deadline:
			t.Fatalf("no llegó %q", prefix)
		}
	}
}

// TestStream levanta un servidor real con timeouts cortos y comprueba que el
// stream sobrevive a ellos, recibe heartbeats y los lotes que se insertan.
func TestStream(t *testing.T) {
	svc, broker := newTestService(&fakeStore{})
	h := NewHandler(svc)
	h.heartbeat = 50 * time.Millisecond

	mux := http.NewServeMux()
	as := func(u auth.User, f http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f(w, r.WithContext(auth.ContextWithUser(r.Context(), u)))
		})
	}
	mux.Handle("GET /stream", as(admin, h.Stream))
	mux.Handle("POST /posiciones", as(juan, h.Create))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := sseReader(t, resp)
	waitLine(t, lines, ": conectado", time.Second)

	// Pasados los timeouts del servidor, el stream sigue vivo.
	time.Sleep(500 * time.Millisecond)
	waitLine(t, lines, ": ping", time.Second)

	at := testNow.Add(-time.Second).Format(time.RFC3339Nano)
	post, err := srv.Client().Post(srv.URL+"/posiciones", "application/json",
		strings.NewReader(`{"posiciones":[{"lat":-35.57,"lng":-58.01,"precision_m":6,"capturada_en":"`+at+`","origen":"pwa","visible":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = post.Body.Close()
	if post.StatusCode != http.StatusNoContent {
		t.Fatalf("POST = %d", post.StatusCode)
	}

	waitLine(t, lines, "event: posiciones", time.Second)
	data := waitLine(t, lines, "data: ", time.Second)
	var ps []Position
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &ps); err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].CadeteID != juan.ID || ps[0].Lat != -35.57 || ps[0].Visible == nil || *ps[0].Visible {
		t.Fatalf("posiciones = %+v", ps)
	}

	// Al cortar el cliente, el handler se va y libera la suscripción.
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for broker.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("la suscripción no se liberó al desconectarse el cliente")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamEndsOnBrokerClose(t *testing.T) {
	svc, broker := newTestService(&fakeStore{})
	h := NewHandler(svc)
	srv := httptest.NewServer(http.HandlerFunc(h.Stream))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := sseReader(t, resp)
	waitLine(t, lines, ": conectado", time.Second)

	broker.Close()
	select {
	case <-func() chan struct{} {
		c := make(chan struct{})
		go func() {
			for range lines {
			}
			close(c)
		}()
		return c
	}():
	case <-time.After(2 * time.Second):
		t.Fatal("el stream no terminó al cerrar el broker")
	}
}
