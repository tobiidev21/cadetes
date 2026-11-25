package positions

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/tobiidev21/cadetes/db/migrations"
	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
)

// testDB abre una base de prueba en un esquema descartable con las
// migraciones aplicadas. Sin TEST_DATABASE_URL, el test se saltea.
func testDB(t *testing.T) (*pgxpool.Pool, *db.Queries) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("sin TEST_DATABASE_URL: se saltean los tests contra Postgres (ver docs/spike-gps.md)")
	}
	ctx := context.Background()

	schema := "test_" + strings.ToLower(rand.Text()[:12])
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("conectando a TEST_DATABASE_URL: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migraciones: %v", err)
	}
	return pool, db.New(pool)
}

func createCourier(t *testing.T, q *db.Queries, username string) auth.User {
	t.Helper()
	r, err := q.CreateUser(context.Background(), db.CreateUserParams{
		Username: username, PasswordHash: "x", Role: string(auth.RoleCadete), Name: "Cadete " + username,
	})
	if err != nil {
		t.Fatal(err)
	}
	return auth.User{ID: r.ID, Username: r.Username, Name: r.Name, Role: auth.RoleCadete}
}

func TestIntegrationInsertTrackLatest(t *testing.T) {
	_, q := testDB(t)
	ctx := context.Background()
	juan, ana := createCourier(t, q, "juan"), createCourier(t, q, "ana")
	svc := NewService(q, NewBroker())

	now := time.Now()
	t0 := now.Add(-10 * time.Minute).Truncate(time.Millisecond)
	in := []Input{
		{Lat: ptr(-35.5772), Lng: ptr(-58.0094), PrecisionM: ptr(float32(12.3)), VelocidadMS: ptr(float32(4.5)), RumboGrados: ptr(float32(270)),
			CapturadaEn: ptr(t0), Origen: SourcePWA, Visible: ptr(false)},
		// Opcionales en null y dos capturas en el mismo segundo, distintas por milisegundos.
		{Lat: ptr(-35.578), Lng: ptr(-58.01), PrecisionM: ptr(float32(5)), CapturadaEn: ptr(t0.Add(250 * time.Millisecond)), Origen: SourceNativa},
	}
	n, err := svc.Record(ctx, juan, in)
	if err != nil || n != 2 {
		t.Fatalf("Record = %d, %v", n, err)
	}
	if n, err := svc.Record(ctx, juan, in); err != nil || n != 0 {
		t.Fatalf("reenvío: Record = %d, %v; quería 0 (duplicados ignorados)", n, err)
	}
	later := []Input{{Lat: ptr(-35.6), Lng: ptr(-58.02), PrecisionM: ptr(float32(7)), CapturadaEn: ptr(t0.Add(time.Minute)), Origen: SourcePWA}}
	if _, err := svc.Record(ctx, juan, later); err != nil {
		t.Fatal(err)
	}
	// El mismo instante para otro cadete no es duplicado.
	if n, err := svc.Record(ctx, ana, in[:1]); err != nil || n != 1 {
		t.Fatalf("Record ana = %d, %v", n, err)
	}

	track, err := svc.Track(ctx, juan.ID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(track) != 3 {
		t.Fatalf("recorrido con %d posiciones, quería 3", len(track))
	}
	a, b := track[0], track[1]
	if !a.CapturadaEn.Equal(t0) || a.Lat != -35.5772 || a.PrecisionM != 12.3 || *a.VelocidadMS != 4.5 ||
		*a.RumboGrados != 270 || *a.Visible || a.Origen != SourcePWA || a.CadeteID != juan.ID {
		t.Fatalf("primera = %+v", a)
	}
	if !b.CapturadaEn.Equal(t0.Add(250*time.Millisecond)) || b.VelocidadMS != nil || b.RumboGrados != nil || b.Visible != nil || b.Origen != SourceNativa {
		t.Fatalf("segunda = %+v", b)
	}
	if d := a.RecibidaEn.Sub(now); d < -time.Minute || d > time.Minute {
		t.Fatalf("recibida_en = %v, quería la hora del servidor", a.RecibidaEn)
	}
	if got, _ := svc.Track(ctx, juan.ID, t0.Add(time.Second)); len(got) != 1 {
		t.Fatalf("recorrido desde t0+1s: %d posiciones, quería 1", len(got))
	}

	latest, err := svc.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]Position{}
	for _, p := range latest {
		byID[p.CadeteID] = p
	}
	if len(latest) != 2 || !byID[juan.ID].CapturadaEn.Equal(t0.Add(time.Minute)) || byID[juan.ID].CadeteNombre != "Cadete juan" ||
		!byID[ana.ID].CapturadaEn.Equal(t0) {
		t.Fatalf("últimas = %+v", latest)
	}

	// El CHECK de la base respalda la validación.
	if _, err := q.InsertPositions(ctx, db.InsertPositionsParams{CourierID: juan.ID,
		Rows: []byte(`[{"lat":1,"lng":2,"accuracy_m":1,"captured_at":"2026-11-25T12:00:00Z","source":"ios"}]`)}); err == nil {
		t.Fatal("la base aceptó un origen inválido")
	}
}

// TestIntegrationSummary usa datos fijos: capturas a 0, 10, 20, 100, 110 y
// 300 s, demoras de 1, 2, 3, 4, 5 y 9 s, precisiones de 5 a 30 m y dos
// posiciones con visible = false. Hay una antes del rango y otra justo en
// "hasta", que no cuentan.
func TestIntegrationSummary(t *testing.T) {
	pool, q := testDB(t)
	ctx := context.Background()
	juan := createCourier(t, q, "juan")
	svc := NewService(q, nil)

	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	offsets := []int{0, 10, 20, 100, 110, 300}
	delays := []int{1, 2, 3, 4, 5, 9}
	visible := []*bool{ptr(true), ptr(false), nil, ptr(true), ptr(false), ptr(true)}
	var in []Input
	for i, off := range offsets {
		in = append(in, Input{
			Lat: ptr(-35.57), Lng: ptr(-58.01), PrecisionM: ptr(float32(5 * (i + 1))),
			CapturadaEn: ptr(t0.Add(time.Duration(off) * time.Second)), Origen: SourcePWA, Visible: visible[i],
		})
	}
	until := t0.Add(400 * time.Second)
	outside := []Input{
		{Lat: ptr(0.0), Lng: ptr(0.0), PrecisionM: ptr(float32(999)), CapturadaEn: ptr(t0.Add(-5 * time.Second)), Origen: SourcePWA, Visible: ptr(false)},
		{Lat: ptr(0.0), Lng: ptr(0.0), PrecisionM: ptr(float32(999)), CapturadaEn: ptr(until), Origen: SourcePWA, Visible: ptr(false)},
	}
	if _, err := svc.Record(ctx, juan, append(in, outside...)); err != nil {
		t.Fatal(err)
	}
	// recibida_en la pone el servidor; para tener demoras conocidas se fija a mano.
	for i, off := range offsets {
		at := t0.Add(time.Duration(off) * time.Second)
		if _, err := pool.Exec(ctx, "UPDATE positions SET received_at = captured_at + make_interval(secs => $1) WHERE courier_id = $2 AND captured_at = $3",
			delays[i], juan.ID, at); err != nil {
			t.Fatal(err)
		}
	}

	s, err := svc.Summary(ctx, juan.ID, t0, until)
	if err != nil {
		t.Fatal(err)
	}
	eq := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || math.Abs(*got-want) > 1e-9 {
			t.Errorf("%s = %v, quería %v", name, fmtPtr(got), want)
		}
	}
	if s.Posiciones != 6 {
		t.Errorf("posiciones = %d, quería 6", s.Posiciones)
	}
	if s.Primera == nil || !s.Primera.Equal(t0) || s.Ultima == nil || !s.Ultima.Equal(t0.Add(300*time.Second)) {
		t.Errorf("primera/última = %v / %v", s.Primera, s.Ultima)
	}
	eq("intervalo_medio_s", s.IntervaloMedioS, 60) // (10+10+80+10+190)/5
	if s.Huecos60s.Cantidad != 2 {                 // 80 y 190
		t.Errorf("huecos = %d, quería 2", s.Huecos60s.Cantidad)
	}
	eq("huecos.mayor_s", s.Huecos60s.MayorS, 190)
	eq("demora_media_s", s.DemoraMediaS, 4) // 24/6
	eq("demora_max_s", s.DemoraMaxS, 9)
	eq("precision_media_m", s.PrecisionMediaM, 17.5) // (5+10+…+30)/6
	if s.NoVisibles != 2 {
		t.Errorf("no_visibles = %d, quería 2", s.NoVisibles)
	}

	// Un rango sin huecos ni datos.
	s, err = svc.Summary(ctx, juan.ID, t0, t0.Add(25*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.Posiciones != 3 || s.Huecos60s.Cantidad != 0 || s.Huecos60s.MayorS != nil {
		t.Errorf("rango corto = %+v", s)
	}
	eq("intervalo_medio_s (corto)", s.IntervaloMedioS, 10)
	s, err = svc.Summary(ctx, juan.ID, t0.Add(-time.Hour), t0.Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if s.Posiciones != 0 || s.Primera != nil || s.IntervaloMedioS != nil || s.DemoraMediaS != nil || s.PrecisionMediaM != nil {
		t.Errorf("rango vacío = %+v", s)
	}
}

func fmtPtr(p *float64) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprint(*p)
}
