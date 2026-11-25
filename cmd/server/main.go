// Comando server: arranca la app de cadetes (API + frontend embebido).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/tobiidev21/cadetes/db/migrations"
	"github.com/tobiidev21/cadetes/internal/auth"
	"github.com/tobiidev21/cadetes/internal/db"
	"github.com/tobiidev21/cadetes/internal/orders"
	"github.com/tobiidev21/cadetes/internal/push"
	"github.com/tobiidev21/cadetes/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("el servidor terminó con error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("falta la variable DATABASE_URL")
	}

	// Se valida antes de tocar la base: si faltan las claves VAPID la app no
	// arranca, en vez de arrancar y no mandar ningún push.
	pushCfg, err := push.ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	pool, err := connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := migrate(ctx, pool); err != nil {
		return err
	}

	queries := db.New(pool)
	authSvc := auth.NewService(queries)
	created, err := authSvc.EnsureInitialAdmin(ctx, os.Getenv("ADMIN_INITIAL_PASSWORD"))
	if err != nil {
		return fmt.Errorf("admin inicial: %w", err)
	}
	if created {
		slog.Info("admin inicial creado", "usuario", auth.InitialAdminUsername)
	}

	pushSvc := push.NewService(queries, push.NewWebPushSender(pushCfg, nil), pushCfg.PublicKey)
	ordersSvc := orders.NewService(queries, pushSvc)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           routes(pool, auth.NewHandler(authSvc), orders.NewHandler(ordersSvc), push.NewHandler(pushSvc)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("servidor escuchando", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("apagando el servidor")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	// Con el servidor cerrado no entran pedidos nuevos; se esperan los push
	// que estaban saliendo.
	return pushSvc.Shutdown(shutdownCtx)
}

// connect abre el pool de pgx y verifica que la base responda.
func connect(ctx context.Context, dbURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("configurando la conexión a Postgres: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("conectando a Postgres: %w", err)
	}
	return pool, nil
}

// migrate aplica las migraciones de goose embebidas en el binario.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("preparando migraciones: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("aplicando migraciones: %w", err)
	}
	for _, r := range results {
		slog.Info("migración aplicada", "version", r.Source.Version, "archivo", r.Source.Path, "duracion", r.Duration)
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("leyendo la versión de la base: %w", err)
	}
	slog.Info("base al día", "version", version)
	return nil
}

func routes(pool *pgxpool.Pool, authH *auth.Handler, ordersH *orders.Handler, pushH *push.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := pool.Ping(ctx); err != nil {
			slog.Warn("health: la base no responde", "err", err)
			http.Error(w, "db no disponible", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, "ok")
	})

	admin := authH.RequireRole(auth.RoleAdmin)

	mux.HandleFunc("POST /api/login", authH.Login)
	mux.HandleFunc("POST /api/logout", authH.Logout)
	mux.Handle("GET /api/sesion", authH.RequireLogin(http.HandlerFunc(authH.Session)))
	mux.Handle("GET /api/usuarios", admin(http.HandlerFunc(authH.ListUsers)))
	mux.Handle("POST /api/usuarios", admin(http.HandlerFunc(authH.CreateUser)))
	mux.Handle("GET /api/cadetes", admin(http.HandlerFunc(ordersH.ListCouriers)))
	mux.Handle("GET /api/pedidos", authH.RequireLogin(http.HandlerFunc(ordersH.List)))
	mux.Handle("POST /api/pedidos", admin(http.HandlerFunc(ordersH.Create)))
	mux.Handle("GET /api/pedidos/{id}", authH.RequireLogin(http.HandlerFunc(ordersH.Get)))
	mux.Handle("GET /api/push/clave", authH.RequireLogin(http.HandlerFunc(pushH.PublicKey)))
	mux.Handle("POST /api/push/suscripciones", authH.RequireRole(auth.RoleCadete)(http.HandlerFunc(pushH.Subscribe)))

	// La notificación abre /pedidos/{id}: es la misma página, que lee la ruta.
	mux.HandleFunc("GET /pedidos/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, web.FS(), "index.html")
	})
	static := http.FileServerFS(web.FS())
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sw.js" {
			// Que el navegador vea enseguida una versión nueva del service worker.
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})

	// Rechaza POST/PUT/DELETE que vienen de otro origen (CSRF), además de
	// SameSite=Lax en la cookie.
	return http.NewCrossOriginProtection().Handler(mux)
}
