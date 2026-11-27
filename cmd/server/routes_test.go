package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
	"github.com/tobiidev21/cadetes/internal/positions"
)

// authStore es una base en memoria para auth: usuarios fijos y sesiones.
type authStore struct {
	mu       sync.Mutex
	users    map[string]db.User
	sessions map[string]db.Session
}

func (s *authStore) CountAdmins(context.Context) (int64, error) { return 1, nil }
func (s *authStore) CreateUser(context.Context, db.CreateUserParams) (db.CreateUserRow, error) {
	panic("no se usa")
}
func (s *authStore) ListUsers(context.Context) ([]db.ListUsersRow, error)   { panic("no se usa") }
func (s *authStore) DeleteExpiredSessions(context.Context, time.Time) error { return nil }
func (s *authStore) DeleteSession(_ context.Context, h []byte) error        { return nil }
func (s *authStore) GetUserByUsername(_ context.Context, u string) (db.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user, ok := s.users[u]; ok {
		return user, nil
	}
	return db.User{}, pgx.ErrNoRows
}
func (s *authStore) CreateSession(_ context.Context, arg db.CreateSessionParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[string(arg.TokenHash)] = db.Session(arg)
	return nil
}
func (s *authStore) GetSession(_ context.Context, h []byte) (db.GetSessionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[string(h)]
	if !ok {
		return db.GetSessionRow{}, pgx.ErrNoRows
	}
	for _, u := range s.users {
		if u.ID == sess.UserID {
			return db.GetSessionRow{ExpiresAt: sess.ExpiresAt, UserID: u.ID, Username: u.Username, Role: u.Role, Name: u.Name, Active: u.Active}, nil
		}
	}
	return db.GetSessionRow{}, pgx.ErrNoRows
}

// positionsStore guarda lo mínimo para que el POST dé 204.
type positionsStore struct{}

func (positionsStore) InsertPositions(context.Context, db.InsertPositionsParams) ([]db.Position, error) {
	return []db.Position{}, nil
}
func (positionsStore) LatestPositions(context.Context) ([]db.LatestPositionsRow, error) {
	return []db.LatestPositionsRow{}, nil
}
func (positionsStore) CourierTrack(context.Context, db.CourierTrackParams) ([]db.Position, error) {
	return []db.Position{}, nil
}
func (positionsStore) CourierSummary(context.Context, db.CourierSummaryParams) (db.CourierSummaryRow, error) {
	return db.CourierSummaryRow{}, nil
}

// TestPositionsRoutesRoles arma el mux real de routes() y revisa los permisos
// de las rutas de posiciones: 401 sin sesión, 403 con el rol equivocado.
func TestPositionsRoutesRoles(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("clave-de-prueba"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &authStore{
		users: map[string]db.User{
			"admin": {ID: 1, Username: "admin", PasswordHash: string(hash), Role: "admin", Name: "Admin", Active: true},
			"juan":  {ID: 2, Username: "juan", PasswordHash: string(hash), Role: "cadete", Name: "Juan", Active: true},
		},
		sessions: map[string]db.Session{},
	}
	authH := auth.NewHandler(auth.NewService(store))
	broker := positions.NewBroker()
	defer broker.Close()
	posH := positions.NewHandler(positions.NewService(positionsStore{}, broker))
	h := routes(nil, authH, nil, nil, posH)

	login := func(username string) *http.Cookie {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"`+username+`","password":"clave-de-prueba"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == auth.CookieName {
				return c
			}
		}
		t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body)
		return nil
	}
	adminCookie, cadeteCookie := login("admin"), login("juan")

	body := `{"posiciones":[{"lat":-35.57,"lng":-58.01,"precision_m":5,"capturada_en":"` +
		time.Now().UTC().Format(time.RFC3339Nano) + `","origen":"pwa"}]}`
	reads := []string{
		"/api/posiciones/ultimas",
		"/api/posiciones/recorrido?cadete=2&desde=2026-11-25T00:00:00Z",
		"/api/posiciones/resumen?cadete=2&desde=2026-11-25T00:00:00Z",
		"/api/posiciones/stream",
	}

	do := func(method, target string, c *http.Cookie) int {
		var req *http.Request
		if method == http.MethodPost {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		if c != nil {
			req.AddCookie(c)
		}
		if strings.HasSuffix(target, "/stream") {
			// El stream no termina solo: se corta con el contexto.
			ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
			defer cancel()
			req = req.WithContext(ctx)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do(http.MethodPost, "/api/posiciones", nil); got != http.StatusUnauthorized {
		t.Errorf("POST sin sesión = %d, quería 401", got)
	}
	if got := do(http.MethodPost, "/api/posiciones", adminCookie); got != http.StatusForbidden {
		t.Errorf("POST como admin = %d, quería 403", got)
	}
	if got := do(http.MethodPost, "/api/posiciones", cadeteCookie); got != http.StatusNoContent {
		t.Errorf("POST como cadete = %d, quería 204", got)
	}
	for _, target := range reads {
		if got := do(http.MethodGet, target, nil); got != http.StatusUnauthorized {
			t.Errorf("GET %s sin sesión = %d, quería 401", target, got)
		}
		if got := do(http.MethodGet, target, cadeteCookie); got != http.StatusForbidden {
			t.Errorf("GET %s como cadete = %d, quería 403", target, got)
		}
		if got := do(http.MethodGet, target, adminCookie); got != http.StatusOK {
			t.Errorf("GET %s como admin = %d, quería 200", target, got)
		}
	}
}
