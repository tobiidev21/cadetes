package orders

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/tobiidev21/cadetes/internal/auth"
)

// maxBodyBytes limita el cuerpo de los pedidos JSON de este paquete.
const maxBodyBytes = 4 << 10

// Handler expone el servicio por HTTP. Las rutas van detrás de
// auth.RequireLogin o auth.RequireRole, que dejan el usuario en el contexto.
type Handler struct {
	svc *Service
}

// NewHandler arma los handlers HTTP sobre el servicio.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// List atiende GET /api/pedidos: el admin ve todos, el cadete solo los suyos.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
		return
	}
	out, err := h.svc.ListFor(r.Context(), u)
	if err != nil {
		slog.Error("listando pedidos", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Get atiende GET /api/pedidos/{id}: 404 si no existe o es de otro cadete.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "Pedido no encontrado")
		return
	}
	o, err := h.svc.Get(r.Context(), u, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "Pedido no encontrado")
		return
	}
	if err != nil {
		slog.Error("buscando pedido", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// Create atiende POST /api/pedidos (solo admin) con
// {"pickup_address", "dropoff_address", "price_cents", "courier_id"}.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
		return
	}
	var in NewOrder
	if !decodeJSON(w, r, &in) {
		return
	}

	o, err := h.svc.Create(r.Context(), u, in)
	var inputErr InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
		return
	case errors.Is(err, ErrInvalidCourier):
		writeError(w, http.StatusBadRequest, "El cadete elegido no existe o no está activo")
		return
	case err != nil:
		slog.Error("creando pedido", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// ListCouriers atiende GET /api/cadetes (solo admin): los cadetes activos.
func (h *Handler) ListCouriers(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ActiveCouriers(r.Context())
	if err != nil {
		slog.Error("listando cadetes", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
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
