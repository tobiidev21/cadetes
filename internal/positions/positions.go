// Package positions recibe, guarda, transmite y resume las posiciones GPS de
// los cadetes. Es parte de la prueba técnica de rastreo: el resumen es la
// herramienta para medir cada prueba (huecos, demoras, precisión).
package positions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
)

// Source es de dónde viene la posición. Coincide con el CHECK de la tabla.
type Source string

const (
	SourcePWA    Source = "pwa"
	SourceNativa Source = "nativa"
)

// Límites del lote y de las consultas.
const (
	MaxBatch        = 100
	MaxFuture       = time.Minute
	MaxPast         = 24 * time.Hour
	MaxTrack        = 10000 // tope de CourierTrack (está en la consulta)
	MaxSummaryRange = 7 * 24 * time.Hour
)

// InputError es un dato inválido que mandó el cliente; el mensaje se puede
// mostrar tal cual.
type InputError string

func (e InputError) Error() string { return string(e) }

// Input es una posición tal como la manda el celular. Los punteros distinguen
// "no vino" de cero (latitud 0 es válida).
type Input struct {
	Lat         *float64   `json:"lat"`
	Lng         *float64   `json:"lng"`
	PrecisionM  *float32   `json:"precision_m"`
	VelocidadMS *float32   `json:"velocidad_ms"`
	RumboGrados *float32   `json:"rumbo_grados"`
	CapturadaEn *time.Time `json:"capturada_en"`
	Origen      Source     `json:"origen"`
	Visible     *bool      `json:"visible"`
}

// Batch es el cuerpo de POST /api/posiciones.
type Batch struct {
	Posiciones []Input `json:"posiciones"`
}

// Position es una posición guardada, como la ven el admin y el stream.
type Position struct {
	ID           int64     `json:"id"`
	CadeteID     int64     `json:"cadete_id"`
	CadeteNombre string    `json:"cadete_nombre,omitempty"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	PrecisionM   float32   `json:"precision_m"`
	VelocidadMS  *float32  `json:"velocidad_ms"`
	RumboGrados  *float32  `json:"rumbo_grados"`
	CapturadaEn  time.Time `json:"capturada_en"`
	RecibidaEn   time.Time `json:"recibida_en"`
	Origen       Source    `json:"origen"`
	Visible      *bool     `json:"visible"`
}

// Gaps son los huecos de más de 60 s entre posiciones consecutivas.
type Gaps struct {
	Cantidad int64    `json:"cantidad"`
	MayorS   *float64 `json:"mayor_s"`
}

// Summary es el resumen de un cadete en un rango. Los tiempos van en
// segundos; lo que no se puede calcular (sin datos) sale null.
type Summary struct {
	CadeteID        int64      `json:"cadete_id"`
	Desde           time.Time  `json:"desde"`
	Hasta           time.Time  `json:"hasta"`
	Posiciones      int64      `json:"posiciones"`
	Primera         *time.Time `json:"primera"`
	Ultima          *time.Time `json:"ultima"`
	IntervaloMedioS *float64   `json:"intervalo_medio_s"`
	Huecos60s       Gaps       `json:"huecos_60s"`
	DemoraMediaS    *float64   `json:"demora_media_s"`
	DemoraMaxS      *float64   `json:"demora_max_s"`
	PrecisionMediaM *float64   `json:"precision_media_m"`
	NoVisibles      int64      `json:"no_visibles"`
}

// Store es lo que positions necesita de la base. *db.Queries lo cumple.
type Store interface {
	InsertPositions(ctx context.Context, arg db.InsertPositionsParams) ([]db.Position, error)
	LatestPositions(ctx context.Context) ([]db.LatestPositionsRow, error)
	CourierTrack(ctx context.Context, arg db.CourierTrackParams) ([]db.Position, error)
	CourierSummary(ctx context.Context, arg db.CourierSummaryParams) (db.CourierSummaryRow, error)
}

// Service implementa la lógica de posiciones.
type Service struct {
	store  Store
	broker *Broker
	now    func() time.Time
}

// NewService arma el servicio. Cada lote guardado se publica en broker.
func NewService(store Store, broker *Broker) *Service {
	return &Service{store: store, broker: broker, now: time.Now}
}

// Validate controla el lote entero; ante el primer error lo rechaza todo.
func Validate(in []Input, now time.Time) error {
	if len(in) == 0 {
		return InputError("el lote no tiene posiciones")
	}
	if len(in) > MaxBatch {
		return InputError(fmt.Sprintf("el lote tiene %d posiciones y el máximo es %d", len(in), MaxBatch))
	}
	for i, p := range in {
		if err := validateOne(p, now); err != "" {
			return InputError(fmt.Sprintf("posición %d: %s", i+1, err))
		}
	}
	return nil
}

func validateOne(p Input, now time.Time) string {
	switch {
	case p.Lat == nil:
		return "falta la latitud (lat)"
	case *p.Lat < -90 || *p.Lat > 90:
		return "la latitud tiene que estar entre -90 y 90"
	case p.Lng == nil:
		return "falta la longitud (lng)"
	case *p.Lng < -180 || *p.Lng > 180:
		return "la longitud tiene que estar entre -180 y 180"
	case p.PrecisionM == nil:
		return "falta la precisión (precision_m)"
	case *p.PrecisionM < 0:
		return "la precisión no puede ser negativa"
	case p.VelocidadMS != nil && *p.VelocidadMS < 0:
		return "la velocidad no puede ser negativa"
	case p.RumboGrados != nil && (*p.RumboGrados < 0 || *p.RumboGrados >= 360):
		return "el rumbo tiene que estar entre 0 y 360 (sin incluir 360)"
	case p.CapturadaEn == nil:
		return "falta la hora de captura (capturada_en)"
	case p.CapturadaEn.After(now.Add(MaxFuture)):
		return "capturada_en está más de 1 minuto en el futuro (¿la hora del celular está bien?)"
	case p.CapturadaEn.Before(now.Add(-MaxPast)):
		return "capturada_en tiene más de 24 h"
	case p.Origen != SourcePWA && p.Origen != SourceNativa:
		return fmt.Sprintf("origen inválido %q: tiene que ser \"pwa\" o \"nativa\"", p.Origen)
	}
	return ""
}

// row es una posición como la lee jsonb_to_recordset en InsertPositions: las
// claves tienen que coincidir con las columnas de la consulta.
type row struct {
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	AccuracyM  float32   `json:"accuracy_m"`
	SpeedMs    *float32  `json:"speed_ms"`
	HeadingDeg *float32  `json:"heading_deg"`
	CapturedAt time.Time `json:"captured_at"`
	Source     Source    `json:"source"`
	Visible    *bool     `json:"visible"`
}

// Record valida y guarda un lote del cadete u, y publica en el stream las
// posiciones nuevas. Devuelve cuántas se guardaron (las repetidas no cuentan).
func (s *Service) Record(ctx context.Context, u auth.User, in []Input) (int, error) {
	if err := Validate(in, s.now()); err != nil {
		return 0, err
	}
	rows := make([]row, len(in))
	for i, p := range in {
		rows[i] = row{
			Lat: *p.Lat, Lng: *p.Lng, AccuracyM: *p.PrecisionM,
			SpeedMs: p.VelocidadMS, HeadingDeg: p.RumboGrados,
			CapturedAt: *p.CapturadaEn, Source: p.Origen, Visible: p.Visible,
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return 0, fmt.Errorf("armando el lote: %w", err)
	}
	inserted, err := s.store.InsertPositions(ctx, db.InsertPositionsParams{CourierID: u.ID, Rows: raw})
	if err != nil {
		return 0, fmt.Errorf("guardando posiciones: %w", err)
	}
	if len(inserted) > 0 && s.broker != nil {
		out := make([]Position, len(inserted))
		for i, p := range inserted {
			out[i] = toPosition(p)
			out[i].CadeteNombre = u.Name
		}
		s.broker.PublishPositions(out)
	}
	return len(inserted), nil
}

// Latest devuelve la última posición de cada cadete.
func (s *Service) Latest(ctx context.Context) ([]Position, error) {
	rows, err := s.store.LatestPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("buscando últimas posiciones: %w", err)
	}
	out := make([]Position, len(rows))
	for i, r := range rows {
		out[i] = Position{
			ID: r.ID, CadeteID: r.CourierID, CadeteNombre: r.CourierName,
			Lat: r.Lat, Lng: r.Lng, PrecisionM: r.AccuracyM,
			VelocidadMS: r.SpeedMs, RumboGrados: r.HeadingDeg,
			CapturadaEn: r.CapturedAt, RecibidaEn: r.ReceivedAt,
			Origen: Source(r.Source), Visible: r.Visible,
		}
	}
	return out, nil
}

// Track devuelve el recorrido de un cadete desde since, del más viejo al más
// nuevo, con un tope de MaxTrack posiciones.
func (s *Service) Track(ctx context.Context, courierID int64, since time.Time) ([]Position, error) {
	rows, err := s.store.CourierTrack(ctx, db.CourierTrackParams{CourierID: courierID, Since: since})
	if err != nil {
		return nil, fmt.Errorf("buscando recorrido: %w", err)
	}
	out := make([]Position, len(rows))
	for i, r := range rows {
		out[i] = toPosition(r)
	}
	return out, nil
}

// Summary resume las posiciones de un cadete en [since, until).
func (s *Service) Summary(ctx context.Context, courierID int64, since, until time.Time) (Summary, error) {
	if !until.After(since) {
		return Summary{}, InputError("hasta tiene que ser posterior a desde")
	}
	if until.Sub(since) > MaxSummaryRange {
		return Summary{}, InputError("el rango no puede ser mayor a 7 días")
	}
	r, err := s.store.CourierSummary(ctx, db.CourierSummaryParams{CourierID: courierID, Since: since, Until: until})
	if err != nil {
		return Summary{}, fmt.Errorf("calculando resumen: %w", err)
	}
	return toSummary(courierID, since, until, r), nil
}

// toSummary pasa a null lo que la consulta devuelve con COALESCE cuando no hay
// datos: sin posiciones, todo; con una sola, el intervalo; sin huecos, el
// mayor hueco.
func toSummary(courierID int64, since, until time.Time, r db.CourierSummaryRow) Summary {
	out := Summary{
		CadeteID:   courierID,
		Desde:      since,
		Hasta:      until,
		Posiciones: r.Total,
		Huecos60s:  Gaps{Cantidad: r.GapsOver60s},
		NoVisibles: r.NotVisible,
	}
	if r.Total > 0 {
		first, last := r.FirstAt, r.LastAt
		out.Primera, out.Ultima = &first, &last
		out.DemoraMediaS = round3(r.AvgDelayS)
		out.DemoraMaxS = round3(r.MaxDelayS)
		out.PrecisionMediaM = round3(r.AvgAccuracyM)
	}
	if r.Total > 1 {
		out.IntervaloMedioS = round3(r.AvgIntervalS)
	}
	if r.GapsOver60s > 0 {
		out.Huecos60s.MayorS = round3(r.LongestGapS)
	}
	return out
}

// round3 redondea a milésimas (milisegundos, en los tiempos).
func round3(v float64) *float64 {
	r := math.Round(v*1000) / 1000
	return &r
}

func toPosition(p db.Position) Position {
	return Position{
		ID: p.ID, CadeteID: p.CourierID,
		Lat: p.Lat, Lng: p.Lng, PrecisionM: p.AccuracyM,
		VelocidadMS: p.SpeedMs, RumboGrados: p.HeadingDeg,
		CapturadaEn: p.CapturedAt, RecibidaEn: p.ReceivedAt,
		Origen: Source(p.Source), Visible: p.Visible,
	}
}
