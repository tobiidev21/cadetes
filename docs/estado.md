# Estado

Prueba técnica: en curso.

## Paso 1 — Esqueleto y deploy (`spike/esqueleto`)

### Qué se hizo

- Módulo `github.com/tobiidev21/cadetes` con Go 1.27 (última estable al 2026-11-23).
- `cmd/server/main.go`: servidor `net/http` estándar.
  - Escucha en `PORT` (8080 si no está definida).
  - Exige `DATABASE_URL`; conecta con `pgxpool` y hace ping (10 s) antes de seguir.
  - Aplica las migraciones de goose al arrancar (Provider API, archivos embebidos desde `db/migrations`).
  - `GET /health`: hace ping a la base (2 s) y responde `ok` (200) o `db no disponible` (503).
  - `GET /`: sirve el frontend embebido con `go:embed` (`web/static/index.html`).
  - Apagado limpio con SIGTERM/SIGINT (timeout 10 s) y timeouts en el servidor HTTP.
- `db/migrations/00001_inicio.sql`: migración base sin tablas (`SELECT 1`), solo para comprobar que goose corre y crea `goose_db_version`. Las tablas llegan en el paso 2.
- `Dockerfile` multi-etapa: build con `golang:1.27-alpine` (binario estático, `CGO_ENABLED=0`), final con `gcr.io/distroless/static-debian12:nonroot`. Sin configuración específica de Railway.

El frontend por ahora es un `index.html` estático en `web/static/`. Cuando entre Vite (pasos 3–4) el embed pasa a apuntar a `web/dist`.

### Cómo se probó (local)

Imagen construida con `docker build` y corrida contra un Postgres 18 descartable:

- `GET /health` → `200 ok`; `POST /health` → `405`.
- `GET /` → devuelve el `index.html` embebido.
- Primer arranque: log `migración aplicada version=1`; `goose_db_version` con versiones 0 y 1.
- Reinicio: no reaplica nada, log `base al día version=1`.
- Base caída con la app corriendo: `/health` → `503 db no disponible`.
- `docker stop` (SIGTERM): log `apagando el servidor` y salida limpia.
- Sin `DATABASE_URL`: sale con código 1 y mensaje claro.

### Pendiente para cerrar el paso (Railway)

- [ ] Crear proyecto en Railway, agregar Postgres y conectar el repo de GitHub (rama `main`).
- [ ] En el servicio de la app, definir `DATABASE_URL` como referencia a la del Postgres (`${{Postgres.DATABASE_URL}}`).
- [ ] Generar dominio público y comprobar que `https://<dominio>/health` responde `ok`.
- [ ] Opcional: configurar `/health` como healthcheck del servicio en Railway.
- [ ] Hacer un cambio en `main` y verificar que se despliega solo.
