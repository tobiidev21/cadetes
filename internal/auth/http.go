package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/tobiidev21/cadetes/internal/db"
)

// CookieName es el nombre de la cookie de sesión.
const CookieName = "cadetes_sesion"

// maxBodyBytes limita el cuerpo de los pedidos JSON de este paquete.
const maxBodyBytes = 4 << 10

// Handler expone el servicio por HTTP.
type Handler struct {
	svc *Service
}

// NewHandler arma los handlers HTTP sobre el servicio.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type ctxKey struct{}

// UserFromContext devuelve el usuario que dejó RequireLogin o RequireRole.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// ContextWithUser deja el usuario en el contexto, como lo hace el middleware.
// Sirve para tests de otros paquetes.
func ContextWithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// RequireLogin deja pasar a cualquier usuario con sesión válida (401 si no).
func (h *Handler) RequireLogin(next http.Handler) http.Handler {
	return h.require(next, nil)
}

// RequireRole deja pasar solo a los roles indicados: 401 sin sesión, 403 con
// sesión pero otro rol.
func (h *Handler) RequireRole(roles ...Role) func(http.Handler) http.Handler {
	if len(roles) == 0 {
		panic("auth.RequireRole: falta al menos un rol")
	}
	return func(next http.Handler) http.Handler {
		return h.require(next, roles)
	}
}

func (h *Handler) require(next http.Handler, roles []Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := h.svc.Authenticate(r.Context(), tokenFromRequest(r))
		if errors.Is(err, ErrNoSession) {
			if _, cerr := r.Cookie(CookieName); cerr == nil {
				clearCookie(w)
			}
			writeError(w, http.StatusUnauthorized, "Tenés que iniciar sesión")
			return
		}
		if err != nil {
			slog.Error("autenticando", "err", err)
			writeError(w, http.StatusInternalServerError, "Error interno")
			return
		}
		if roles != nil && !slices.Contains(roles, u.Role) {
			writeError(w, http.StatusForbidden, "No tenés permiso para esto")
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithUser(r.Context(), u)))
	})
}

// Login atiende POST /api/login con {"username", "password"}.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	token, u, err := h.svc.Login(r.Context(), in.Username, in.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "Usuario o contraseña incorrectos")
		return
	}
	if err != nil {
		slog.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}

	http.SetCookie(w, sessionCookie(token, h.svc.ttl))
	writeJSON(w, http.StatusOK, u)
}

// Logout atiende POST /api/logout. Siempre borra la cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Logout(r.Context(), tokenFromRequest(r)); err != nil {
		slog.Error("logout", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Session atiende GET /api/sesion: devuelve el usuario actual.
func (h *Handler) Session(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, u)
}

type userOut struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// ListUsers atiende GET /api/usuarios (solo admin).
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ListUsers(r.Context())
	if err != nil {
		slog.Error("listando usuarios", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	out := make([]userOut, 0, len(rows))
	for _, u := range rows {
		out = append(out, toUserOut(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateUser atiende POST /api/usuarios (solo admin) con
// {"username", "password", "name", "role"}. Si no viene rol, es cadete.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Name     string `json:"name"`
		Role     Role   `json:"role"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = RoleCadete
	}

	u, err := h.svc.CreateUser(r.Context(), in.Username, in.Password, in.Name, in.Role)
	var inputErr InputError
	switch {
	case errors.As(err, &inputErr):
		writeError(w, http.StatusBadRequest, inputErr.Error())
		return
	case errors.Is(err, ErrUsernameTaken):
		writeError(w, http.StatusConflict, "Ese usuario ya existe")
		return
	case err != nil:
		slog.Error("creando usuario", "err", err)
		writeError(w, http.StatusInternalServerError, "Error interno")
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func toUserOut(u db.ListUsersRow) userOut {
	return userOut{
		ID:        u.ID,
		Username:  u.Username,
		Name:      u.Name,
		Role:      Role(u.Role),
		Active:    u.Active,
		CreatedAt: u.CreatedAt,
	}
}

func tokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func sessionCookie(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearCookie(w http.ResponseWriter) {
	c := sessionCookie("", 0)
	c.MaxAge = -1
	http.SetCookie(w, c)
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
