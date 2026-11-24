package push

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tobiidev21/cadetes/internal/auth"
)

// maxBodyBytes limita el cuerpo de los pedidos JSON de este paquete.
const maxBodyBytes = 8 << 10

// Handler expone el servicio por HTTP. Las rutas van detrás de
// auth.RequireLogin o auth.RequireRole.
type Handler struct {
	svc *Service
}

// NewHandler arma los handlers HTTP sobre el servicio.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// PublicKey atiende GET /api/push/clave: la VAPID pública para suscribirse.
func (h *Handler) PublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"public_key": h.svc.PublicKey()})
}

// Subscribe atiende POST /api/push/suscripciones con PushSubscription.toJSON().
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
		return
	}
	var in SubscriptionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	err := h.svc.Subscribe(r.Context(), u.ID, in, r.UserAgent())
	var inputErr InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
		return
	case err != nil:
		slog.Error("guardando suscripción push", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
