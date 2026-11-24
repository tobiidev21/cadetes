// Package auth resuelve login, sesiones y roles.
//
// La sesión es un token aleatorio que viaja en una cookie HttpOnly. En la base
// se guarda solo su hash SHA-256, así que una copia de la base no permite
// hacerse pasar por nadie.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/tobiidev21/cadetes/internal/db"
)

// Role es el rol de un usuario. Coincide con el CHECK de la tabla users.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleCadete Role = "cadete"
)

// Valid indica si el rol es uno de los conocidos.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleCadete
}

// SessionTTL es cuánto dura una sesión desde el login.
const SessionTTL = 30 * 24 * time.Hour

// Límites de contraseña. bcrypt solo usa los primeros 72 bytes, así que más
// largo se rechaza en vez de truncarse en silencio.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 72
)

// InitialAdminUsername es el usuario del admin que se crea al arrancar.
const InitialAdminUsername = "admin"

var (
	ErrInvalidCredentials = errors.New("usuario o contraseña incorrectos")
	ErrNoSession          = errors.New("sesión inválida o vencida")
	ErrUsernameTaken      = errors.New("ese usuario ya existe")
)

// InputError es un dato inválido que mandó el usuario; el mensaje se puede
// mostrar tal cual.
type InputError string

func (e InputError) Error() string { return string(e) }

// User es el usuario autenticado, sin datos sensibles.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     Role   `json:"role"`
}

// Store es lo que auth necesita de la base. *db.Queries lo cumple.
type Store interface {
	CountAdmins(ctx context.Context) (int64, error)
	CreateUser(ctx context.Context, arg db.CreateUserParams) (db.CreateUserRow, error)
	GetUserByUsername(ctx context.Context, username string) (db.User, error)
	ListUsers(ctx context.Context) ([]db.ListUsersRow, error)
	CreateSession(ctx context.Context, arg db.CreateSessionParams) error
	GetSession(ctx context.Context, tokenHash []byte) (db.GetSessionRow, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context, before time.Time) error
}

// Service implementa la lógica de usuarios y sesiones.
type Service struct {
	store      Store
	now        func() time.Time
	ttl        time.Duration
	bcryptCost int

	dummyOnce sync.Once
	dummyHash []byte
}

// NewService arma el servicio con los valores de producción.
func NewService(store Store) *Service {
	return &Service{
		store:      store,
		now:        time.Now,
		ttl:        SessionTTL,
		bcryptCost: bcrypt.DefaultCost,
	}
}

// NormalizeUsername deja el usuario en minúsculas y sin espacios alrededor,
// igual que lo exige la base.
func NormalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidatePassword controla el largo de la contraseña.
func ValidatePassword(p string) error {
	if len(p) < MinPasswordLen {
		return InputError(fmt.Sprintf("la contraseña debe tener al menos %d caracteres", MinPasswordLen))
	}
	if len(p) > MaxPasswordLen {
		return InputError(fmt.Sprintf("la contraseña no puede tener más de %d bytes", MaxPasswordLen))
	}
	return nil
}

// Login valida usuario y contraseña y abre una sesión. Devuelve el token en
// claro (para la cookie) y el usuario. Ante cualquier dato inválido devuelve
// ErrInvalidCredentials, sin distinguir si el usuario existe.
func (s *Service) Login(ctx context.Context, username, password string) (string, User, error) {
	u, err := s.store.GetUserByUsername(ctx, NormalizeUsername(username))
	if errors.Is(err, pgx.ErrNoRows) {
		// Comparar igual contra un hash de relleno para que la demora no
		// revele si el usuario existe.
		_ = bcrypt.CompareHashAndPassword(s.dummy(), []byte(password))
		return "", User{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", User{}, fmt.Errorf("buscando usuario: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || !u.Active {
		return "", User{}, ErrInvalidCredentials
	}

	token, hash := newToken()
	now := s.now()
	if err := s.store.CreateSession(ctx, db.CreateSessionParams{
		TokenHash: hash,
		UserID:    u.ID,
		ExpiresAt: now.Add(s.ttl),
	}); err != nil {
		return "", User{}, fmt.Errorf("creando sesión: %w", err)
	}

	// Limpieza oportunista: si falla no afecta el login.
	if err := s.store.DeleteExpiredSessions(ctx, now); err != nil {
		slog.Warn("no se pudieron borrar las sesiones vencidas", "err", err)
	}

	return token, User{ID: u.ID, Username: u.Username, Name: u.Name, Role: Role(u.Role)}, nil
}

// Authenticate devuelve el usuario dueño del token. Si el token no existe, la
// sesión venció o el usuario está inactivo, devuelve ErrNoSession.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	hash := hashToken(token)
	row, err := s.store.GetSession(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("buscando sesión: %w", err)
	}
	if !s.now().Before(row.ExpiresAt) {
		if err := s.store.DeleteSession(ctx, hash); err != nil {
			slog.Warn("no se pudo borrar la sesión vencida", "err", err)
		}
		return User{}, ErrNoSession
	}
	if !row.Active {
		return User{}, ErrNoSession
	}
	return User{ID: row.UserID, Username: row.Username, Name: row.Name, Role: Role(row.Role)}, nil
}

// Logout borra la sesión del token. Un token vacío o desconocido no es error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("borrando sesión: %w", err)
	}
	return nil
}

// CreateUser da de alta un usuario con la contraseña hasheada con bcrypt.
func (s *Service) CreateUser(ctx context.Context, username, password, name string, role Role) (User, error) {
	username = NormalizeUsername(username)
	name = strings.TrimSpace(name)
	switch {
	case username == "":
		return User{}, InputError("falta el usuario")
	case name == "":
		return User{}, InputError("falta el nombre")
	case !role.Valid():
		return User{}, InputError(fmt.Sprintf("rol inválido: %q", role))
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return User{}, fmt.Errorf("hasheando contraseña: %w", err)
	}
	row, err := s.store.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: string(hash),
		Role:         string(role),
		Name:         name,
	})
	if isUniqueViolation(err) {
		return User{}, ErrUsernameTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("creando usuario: %w", err)
	}
	return User{ID: row.ID, Username: row.Username, Name: row.Name, Role: Role(row.Role)}, nil
}

// EnsureInitialAdmin crea el usuario "admin" con la contraseña dada solo si
// no existe ningún admin. Devuelve true si lo creó. Si no hay admin y la
// contraseña está vacía, es un error: la app quedaría sin forma de entrar.
func (s *Service) EnsureInitialAdmin(ctx context.Context, password string) (bool, error) {
	n, err := s.store.CountAdmins(ctx)
	if err != nil {
		return false, fmt.Errorf("contando admins: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if password == "" {
		return false, errors.New("no existe ningún admin y falta la variable ADMIN_INITIAL_PASSWORD")
	}
	if err := ValidatePassword(password); err != nil {
		return false, fmt.Errorf("ADMIN_INITIAL_PASSWORD: %w", err)
	}
	_, err = s.CreateUser(ctx, InitialAdminUsername, password, "Administrador", RoleAdmin)
	if errors.Is(err, ErrUsernameTaken) {
		return false, fmt.Errorf("no hay ningún admin pero el usuario %q ya existe con otro rol", InitialAdminUsername)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListUsers devuelve todos los usuarios, sin hashes.
func (s *Service) ListUsers(ctx context.Context) ([]db.ListUsersRow, error) {
	return s.store.ListUsers(ctx)
}

func (s *Service) dummy() []byte {
	s.dummyOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), s.bcryptCost)
		if err != nil {
			panic(err) // con una contraseña corta y un costo válido no falla
		}
		s.dummyHash = h
	})
	return s.dummyHash
}

// newToken genera un token de 256 bits y su hash.
func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read nunca devuelve error
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

// hashToken es el valor que se guarda en sessions.token_hash.
func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
