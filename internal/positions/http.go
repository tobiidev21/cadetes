package positions

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tobiidev21/cadetes/internal/auth"
)

// maxBodyBytes limita el cuerpo de POST /api/posiciones: 100 posiciones
// ocupan unos 20 KB.
const maxBodyBytes = 64 << 10

// heartbeatEvery es cada cuánto el stream manda un comentario para que los
// proxies no corten la conexión por inactividad.
const heartbeatEvery = 15 * time.Second

// Handler expone el servicio por HTTP. Las rutas van detrás de
// auth.RequireRole, que deja el usuario en el contexto.
type Handler struct {
	svc       *Service
	limiter   *Limiter
	heartbeat time.Duration
}

// NewHandler arma los handlers HTTP sobre el servicio.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, limiter: NewLimiter(), heartbeat: heartbeatEvery}
}

// Create atiende POST /api/posiciones (solo cadete) con {"posiciones": [...]}.
// El cadete sale de la sesión; el cuerpo no puede traer otro (campo
// desconocido → 400).
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
		return
	}
	if ok, wait := h.limiter.Allow(u.ID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "Demasiados envíos seguidos; reintentá en unos segundos")
		return
	}
	var in Batch
	if !decodeJSON(w, r, &in) {
		return
	}
	_, err := h.svc.Record(r.Context(), u, in.Posiciones)
	var inputErr InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
		return
	case err != nil:
		slog.Error("guardando posiciones", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Stream atiende GET /api/posiciones/stream (solo admin): Server-Sent Events
// con un evento "posiciones" por cada lote guardado.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// El WriteTimeout del servidor (30 s) cortaría el stream: se saca solo
	// para esta conexión. El ReadTimeout no lo afecta (lo cubre TestStream).
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Error("stream: quitando el timeout de escritura", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}

	sub := h.svc.broker.Subscribe()
	defer func() {
		if dropped := sub.Cancel(); dropped > 0 {
			slog.Warn("stream: se descartaron mensajes para un admin lento", "descartados", dropped)
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // que un proxy no lo junte en un buffer
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("retry: 3000\n: conectado\n\n")); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		slog.Error("stream: el ResponseWriter no permite flush", "err", err)
		return
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		var msg []byte
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-sub.C:
			if !ok { // el servidor se está apagando
				return
			}
			msg = m
		case <-ticker.C:
			msg = []byte(": ping\n\n")
		}
		if _, err := w.Write(msg); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// Latest atiende GET /api/posiciones/ultimas (solo admin).
func (h *Handler) Latest(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Latest(r.Context())
	if err != nil {
		slog.Error("últimas posiciones", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Track atiende GET /api/posiciones/recorrido?cadete=ID&desde=RFC3339 (solo
// admin).
func (h *Handler) Track(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	courierID, ok := courierParam(w, q.Get("cadete"))
	if !ok {
		return
	}
	since, ok := timeParam(w, "desde", q.Get("desde"))
	if !ok {
		return
	}
	out, err := h.svc.Track(r.Context(), courierID, since)
	if err != nil {
		slog.Error("recorrido", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Summary atiende GET /api/posiciones/resumen?cadete=ID&desde=…&hasta=… (solo
// admin). Si falta hasta, es ahora.
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	courierID, ok := courierParam(w, q.Get("cadete"))
	if !ok {
		return
	}
	since, ok := timeParam(w, "desde", q.Get("desde"))
	if !ok {
		return
	}
	until := h.svc.now()
	if q.Get("hasta") != "" {
		if until, ok = timeParam(w, "hasta", q.Get("hasta")); !ok {
			return
		}
	}
	out, err := h.svc.Summary(r.Context(), courierID, since, until)
	var inputErr InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
		return
	case err != nil:
		slog.Error("resumen de posiciones", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func courierParam(w http.ResponseWriter, s string) (int64, bool) {
	if s == "" {
		writeError(w, http.StatusBadRequest, "Falta el parámetro cadete (id del cadete)")
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "El parámetro cadete tiene que ser un id numérico")
		return 0, false
	}
	return id, true
}

func timeParam(w http.ResponseWriter, name, s string) (time.Time, bool) {
	if s == "" {
		writeError(w, http.StatusBadRequest, "Falta el parámetro "+name+" (fecha RFC 3339, p. ej. 2026-11-25T14:00:00-03:00)")
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		writeError(w, http.StatusBadRequest, "El parámetro "+name+" tiene que ser una fecha RFC 3339, p. ej. 2026-11-25T14:00:00-03:00 (en la URL, el + de una zona horaria va como %2B)")
		return time.Time{}, false
	}
	return t, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// El error de campo desconocido sí se muestra: es el caso de un body
		// que trae cadete_id, que siempre sale de la sesión.
		if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			writeError(w, http.StatusBadRequest, "JSON inválido: campo desconocido "+field)
			return false
		}
		writeError(w, http.StatusBadRequest, "JSON inválido")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("escribiendo respuesta JSON", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
