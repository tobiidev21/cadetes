package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/tobiidev21/cadetes/internal/db"
)

// fakeStore es una base en memoria que imita las consultas de sqlc.
type fakeStore struct {
	mu       sync.Mutex
	nextID   int64
	users    map[string]db.User // por username
	sessions map[string]db.Session
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]db.User{}, sessions: map[string]db.Session{}}
}

func (f *fakeStore) CountAdmins(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, u := range f.users {
		if u.Role == string(RoleAdmin) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) CreateUser(_ context.Context, arg db.CreateUserParams) (db.CreateUserRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[arg.Username]; ok {
		return db.CreateUserRow{}, &pgconn.PgError{Code: "23505"}
	}
	f.nextID++
	u := db.User{
		ID: f.nextID, Username: arg.Username, PasswordHash: arg.PasswordHash,
		Role: arg.Role, Name: arg.Name, Active: true, CreatedAt: time.Now(),
	}
	f.users[u.Username] = u
	return db.CreateUserRow{ID: u.ID, Username: u.Username, Role: u.Role, Name: u.Name, Active: u.Active, CreatedAt: u.CreatedAt}, nil
}

func (f *fakeStore) GetUserByUsername(_ context.Context, username string) (db.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[username]
	if !ok {
		return db.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeStore) ListUsers(context.Context) ([]db.ListUsersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []db.ListUsersRow{}
	for _, u := range f.users {
		out = append(out, db.ListUsersRow{ID: u.ID, Username: u.Username, Role: u.Role, Name: u.Name, Active: u.Active, CreatedAt: u.CreatedAt})
	}
	return out, nil
}

func (f *fakeStore) CreateSession(_ context.Context, arg db.CreateSessionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[string(arg.TokenHash)]; ok {
		return &pgconn.PgError{Code: "23505"}
	}
	f.sessions[string(arg.TokenHash)] = db.Session(arg)
	return nil
}

func (f *fakeStore) GetSession(_ context.Context, tokenHash []byte) (db.GetSessionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[string(tokenHash)]
	if !ok {
		return db.GetSessionRow{}, pgx.ErrNoRows
	}
	for _, u := range f.users {
		if u.ID == s.UserID {
			return db.GetSessionRow{ExpiresAt: s.ExpiresAt, UserID: u.ID, Username: u.Username, Role: u.Role, Name: u.Name, Active: u.Active}, nil
		}
	}
	return db.GetSessionRow{}, pgx.ErrNoRows
}

func (f *fakeStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, string(tokenHash))
	return nil
}

func (f *fakeStore) DeleteExpiredSessions(_ context.Context, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.sessions {
		if !s.ExpiresAt.After(before) {
			delete(f.sessions, k)
		}
	}
	return nil
}

func (f *fakeStore) setActive(username string, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[username]
	u.Active = active
	f.users[username] = u
}

// testClock es un reloj que el test avanza a mano.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T) (*Service, *fakeStore, *testClock) {
	t.Helper()
	store := newFakeStore()
	clock := &testClock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	svc := NewService(store)
	svc.now = clock.now
	svc.bcryptCost = bcrypt.MinCost
	return svc, store, clock
}

func mustCreate(t *testing.T, svc *Service, username, password string, role Role) User {
	t.Helper()
	u, err := svc.CreateUser(context.Background(), username, password, "Nombre "+username, role)
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", username, err)
	}
	return u
}

func TestNewTokenIsRandomAndHashIsSHA256(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		token, hash := newToken()
		if len(token) != 43 { // 32 bytes en base64 URL sin relleno
			t.Fatalf("largo de token = %d, quiero 43", len(token))
		}
		if seen[token] {
			t.Fatalf("token repetido: %s", token)
		}
		seen[token] = true

		want := sha256.Sum256([]byte(token))
		if !bytes.Equal(hash, want[:]) {
			t.Fatalf("el hash no es SHA-256 del token")
		}
	}
}

func TestLoginStoresOnlyTokenHash(t *testing.T) {
	svc, store, clock := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)

	token, u, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u.Username != "juan" || u.Role != RoleCadete {
		t.Fatalf("usuario = %+v", u)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sesiones guardadas = %d, quiero 1", len(store.sessions))
	}
	for key, s := range store.sessions {
		if strings.Contains(key, token) || bytes.Contains(s.TokenHash, []byte(token)) {
			t.Fatal("el token en claro quedó guardado en la base")
		}
		want := sha256.Sum256([]byte(token))
		if !bytes.Equal(s.TokenHash, want[:]) {
			t.Fatal("se guardó algo distinto al SHA-256 del token")
		}
		if !s.ExpiresAt.Equal(clock.now().Add(SessionTTL)) {
			t.Fatalf("vence %v, quiero %v", s.ExpiresAt, clock.now().Add(SessionTTL))
		}
	}
}

func TestLoginNormalizesUsername(t *testing.T) {
	svc, _, _ := newTestService(t)
	mustCreate(t, svc, "  Juan ", "clave-segura", RoleCadete)

	if _, _, err := svc.Login(context.Background(), " JUAN", "clave-segura"); err != nil {
		t.Fatalf("Login con mayúsculas y espacios: %v", err)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	svc, store, _ := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	mustCreate(t, svc, "pedro", "clave-segura", RoleCadete)
	store.setActive("pedro", false)

	cases := []struct{ name, user, pass string }{
		{"contraseña incorrecta", "juan", "otra-clave"},
		{"usuario inexistente", "nadie", "clave-segura"},
		{"usuario inactivo", "pedro", "clave-segura"},
		{"contraseña vacía", "juan", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.Login(context.Background(), tc.user, tc.pass)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("err = %v, quiero ErrInvalidCredentials", err)
			}
		})
	}
	if len(store.sessions) != 0 {
		t.Fatalf("se crearon %d sesiones con credenciales inválidas", len(store.sessions))
	}
}

func TestAuthenticate(t *testing.T) {
	svc, _, _ := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	token, _, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatal(err)
	}

	u, err := svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Username != "juan" || u.Role != RoleCadete {
		t.Fatalf("usuario = %+v", u)
	}

	for _, bad := range []string{"", "token-inventado", token + "x"} {
		if _, err := svc.Authenticate(context.Background(), bad); !errors.Is(err, ErrNoSession) {
			t.Errorf("Authenticate(%q) err = %v, quiero ErrNoSession", bad, err)
		}
	}
}

func TestAuthenticateExpiredSession(t *testing.T) {
	svc, store, clock := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	token, _, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatal(err)
	}

	clock.advance(SessionTTL - time.Second)
	if _, err := svc.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("un segundo antes de vencer: %v", err)
	}

	clock.advance(time.Second)
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("al vencer: err = %v, quiero ErrNoSession", err)
	}
	if len(store.sessions) != 0 {
		t.Fatal("la sesión vencida no se borró")
	}
}

func TestAuthenticateInactiveUser(t *testing.T) {
	svc, store, _ := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	token, _, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatal(err)
	}

	store.setActive("juan", false)
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("usuario desactivado: err = %v, quiero ErrNoSession", err)
	}
}

func TestLogout(t *testing.T) {
	svc, _, _ := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	token, _, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := svc.Login(context.Background(), "juan", "clave-segura")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("después del logout: err = %v, quiero ErrNoSession", err)
	}
	if _, err := svc.Authenticate(context.Background(), other); err != nil {
		t.Fatalf("el logout cerró también la otra sesión: %v", err)
	}
}

func TestLoginCleansExpiredSessions(t *testing.T) {
	svc, store, clock := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	if _, _, err := svc.Login(context.Background(), "juan", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	clock.advance(SessionTTL)
	if _, _, err := svc.Login(context.Background(), "juan", "clave-segura"); err != nil {
		t.Fatal(err)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sesiones = %d, quiero 1 (la vencida se borra)", len(store.sessions))
	}
}

func TestCreateUserValidation(t *testing.T) {
	svc, _, _ := newTestService(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)

	cases := []struct {
		name, user, pass, fullName string
		role                       Role
		wantTaken                  bool
	}{
		{name: "sin usuario", user: "  ", pass: "clave-segura", fullName: "X", role: RoleCadete},
		{name: "sin nombre", user: "ana", pass: "clave-segura", fullName: " ", role: RoleCadete},
		{name: "rol inválido", user: "ana", pass: "clave-segura", fullName: "Ana", role: "comercio"},
		{name: "contraseña corta", user: "ana", pass: "1234567", fullName: "Ana", role: RoleCadete},
		{name: "contraseña larga", user: "ana", pass: strings.Repeat("a", 73), fullName: "Ana", role: RoleCadete},
		{name: "usuario repetido", user: "JUAN", pass: "clave-segura", fullName: "Juan", role: RoleCadete, wantTaken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateUser(context.Background(), tc.user, tc.pass, tc.fullName, tc.role)
			if tc.wantTaken {
				if !errors.Is(err, ErrUsernameTaken) {
					t.Fatalf("err = %v, quiero ErrUsernameTaken", err)
				}
				return
			}
			var inputErr InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("err = %v, quiero InputError", err)
			}
		})
	}
}

func TestEnsureInitialAdmin(t *testing.T) {
	ctx := context.Background()

	t.Run("sin admin y sin contraseña falla", func(t *testing.T) {
		svc, _, _ := newTestService(t)
		if _, err := svc.EnsureInitialAdmin(ctx, ""); err == nil {
			t.Fatal("quiero error")
		}
	})

	t.Run("contraseña corta falla", func(t *testing.T) {
		svc, _, _ := newTestService(t)
		if _, err := svc.EnsureInitialAdmin(ctx, "corta"); err == nil {
			t.Fatal("quiero error")
		}
	})

	t.Run("crea el admin una sola vez", func(t *testing.T) {
		svc, store, _ := newTestService(t)
		created, err := svc.EnsureInitialAdmin(ctx, "clave-del-admin")
		if err != nil || !created {
			t.Fatalf("primer arranque: created=%v err=%v", created, err)
		}
		if _, u, err := svc.Login(ctx, InitialAdminUsername, "clave-del-admin"); err != nil || u.Role != RoleAdmin {
			t.Fatalf("login del admin: user=%+v err=%v", u, err)
		}

		// Segundo arranque, aunque cambie o falte la variable: no toca nada.
		for _, pass := range []string{"otra-clave-distinta", ""} {
			created, err = svc.EnsureInitialAdmin(ctx, pass)
			if err != nil || created {
				t.Fatalf("con admin existente (pass=%q): created=%v err=%v", pass, created, err)
			}
		}
		if len(store.users) != 1 {
			t.Fatalf("usuarios = %d, quiero 1", len(store.users))
		}
		if _, _, err := svc.Login(ctx, InitialAdminUsername, "clave-del-admin"); err != nil {
			t.Fatal("la contraseña del admin cambió en el segundo arranque")
		}
	})

	t.Run("no pisa un usuario admin que no es admin", func(t *testing.T) {
		svc, _, _ := newTestService(t)
		mustCreate(t, svc, InitialAdminUsername, "clave-segura", RoleCadete)
		if _, err := svc.EnsureInitialAdmin(ctx, "clave-del-admin"); err == nil {
			t.Fatal("quiero error")
		}
	})
}

// --- HTTP ---

func newTestMux(t *testing.T) (*http.ServeMux, *Service) {
	t.Helper()
	svc, _, _ := newTestService(t)
	h := NewHandler(svc)
	admin := h.RequireRole(RoleAdmin)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.Login)
	mux.HandleFunc("POST /api/logout", h.Logout)
	mux.Handle("GET /api/sesion", h.RequireLogin(http.HandlerFunc(h.Session)))
	mux.Handle("GET /api/usuarios", admin(http.HandlerFunc(h.ListUsers)))
	return mux, svc
}

func login(t *testing.T, mux http.Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatal("la respuesta no trae la cookie de sesión")
	return nil
}

func get(mux http.Handler, path string, c *http.Cookie) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoginCookieAttributes(t *testing.T) {
	mux, svc := newTestMux(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)

	rec := login(t, mux, "juan", "clave-segura")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	c := sessionCookieFrom(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie con atributos incorrectos: %+v", c)
	}
	if c.MaxAge != int(SessionTTL/time.Second) {
		t.Fatalf("MaxAge = %d", c.MaxAge)
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		t.Fatal("el token aparece en el cuerpo de la respuesta")
	}
}

func TestLoginWrongPasswordHTTP(t *testing.T) {
	mux, svc := newTestMux(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)

	rec := login(t, mux, "juan", "mal")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, quiero 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("un login fallido no debe setear cookie")
	}
}

func TestRoleMiddleware(t *testing.T) {
	mux, svc := newTestMux(t)
	mustCreate(t, svc, "admin", "clave-del-admin", RoleAdmin)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)

	adminCookie := sessionCookieFrom(t, login(t, mux, "admin", "clave-del-admin"))
	cadeteCookie := sessionCookieFrom(t, login(t, mux, "juan", "clave-segura"))
	fake := &http.Cookie{Name: CookieName, Value: "inventado"}

	cases := []struct {
		name   string
		path   string
		cookie *http.Cookie
		want   int
	}{
		{"sin sesión en ruta de admin", "/api/usuarios", nil, http.StatusUnauthorized},
		{"token inventado", "/api/usuarios", fake, http.StatusUnauthorized},
		{"cadete en ruta de admin", "/api/usuarios", cadeteCookie, http.StatusForbidden},
		{"admin en ruta de admin", "/api/usuarios", adminCookie, http.StatusOK},
		{"sin sesión en /api/sesion", "/api/sesion", nil, http.StatusUnauthorized},
		{"cadete en /api/sesion", "/api/sesion", cadeteCookie, http.StatusOK},
		{"admin en /api/sesion", "/api/sesion", adminCookie, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(mux, tc.path, tc.cookie); got != tc.want {
				t.Fatalf("status = %d, quiero %d", got, tc.want)
			}
		})
	}
}

func TestLogoutHTTP(t *testing.T) {
	mux, svc := newTestMux(t)
	mustCreate(t, svc, "juan", "clave-segura", RoleCadete)
	c := sessionCookieFrom(t, login(t, mux, "juan", "clave-segura"))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, quiero 204", rec.Code)
	}
	if cleared := sessionCookieFrom(t, rec); cleared.MaxAge >= 0 {
		t.Fatalf("la cookie no se borró: %+v", cleared)
	}
	if got := get(mux, "/api/sesion", c); got != http.StatusUnauthorized {
		t.Fatalf("con la cookie vieja: status = %d, quiero 401", got)
	}
}
