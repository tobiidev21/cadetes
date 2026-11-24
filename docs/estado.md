# Estado

Prueba técnica: en curso. Pasos 1, 2 y 3 hechos.

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

## Paso 2 — Login y roles (`spike/auth`)

### Qué se hizo

- `db/migrations/00002_usuarios_sesiones.sql`: tablas `users` y `sessions` según el modelo del plan.
  - `users.role` con `CHECK` (`admin` o `cadete`); `username` único, guardado en minúsculas y sin espacios (también con `CHECK`).
  - `sessions.token_hash` es `BYTEA` de 32 bytes (SHA-256) y clave primaria; `ON DELETE CASCADE` con `users`.
- `sqlc.yaml` + `db/queries/{users,sessions}.sql` → código generado en `internal/db` (pgx/v5, `timestamptz` como `time.Time`). El código generado se commitea; para regenerar: `sqlc generate`.
- `internal/auth`:
  - Contraseñas con bcrypt (costo por defecto). Largo entre 8 y 72 bytes (bcrypt ignora lo que pasa de 72, así que se rechaza en vez de truncar).
  - Login: si el usuario no existe se compara igual contra un hash de relleno, para no revelar por demora qué usuarios existen. Usuario inexistente, clave mal o usuario inactivo dan el mismo error.
  - Sesión: token de 32 bytes de `crypto/rand` en base64url; en la base solo su SHA-256. Dura 30 días (sin renovación). La sesión vencida se borra al usarla y en cada login se limpian las vencidas.
  - Cookie `cadetes_sesion`: `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`.
  - Middleware `RequireLogin` (401 sin sesión) y `RequireRole(roles...)` (401 sin sesión, 403 con otro rol). El usuario queda en el contexto (`auth.UserFromContext`).
  - Admin inicial: al arrancar, si no hay ningún admin, crea `admin` con `ADMIN_INITIAL_PASSWORD`. Si ya hay uno no toca nada (cambiar la variable no cambia la clave). Si no hay admin y falta la variable, la app no arranca.
- Rutas (`cmd/server/main.go`):
  - `POST /api/login` `{"username","password"}` → usuario + cookie.
  - `POST /api/logout` → 204 y borra la cookie.
  - `GET /api/sesion` (cualquier usuario logueado) → usuario actual.
  - `GET /api/usuarios` y `POST /api/usuarios` `{"username","password","name","role"}` (solo admin; rol por defecto `cadete`). Sin esto no hay forma de crear un cadete para la prueba.
  - Todo el mux pasa por `http.CrossOriginProtection` (estándar desde Go 1.25): rechaza POST de otro origen además de `SameSite=Lax`.
- `web/static/index.html` (sin diseño):
  - Formulario de login; muestra nombre y rol y permite salir.
  - Solo para el admin: formulario "Nuevo cadete" (usuario, nombre, contraseña) que hace `POST /api/usuarios` con rol `cadete`, y tabla de usuarios con `GET /api/usuarios` que se recarga después de cada alta. Los errores del servidor se muestran tal cual. Ocultar la sección es solo comodidad: la protección real es el 403 del servidor.

### Cómo se probó

Tests (`go test -race ./internal/auth/`, con un store en memoria, sin base):

- Token aleatorio de 43 caracteres, sin repetidos; lo guardado es exactamente SHA-256 del token y nunca el token en claro.
- Login normaliza el usuario; rechaza clave mal, usuario inexistente, usuario inactivo y clave vacía sin crear sesión.
- Sesión válida hasta 1 s antes de vencer; al vencer da `ErrNoSession` y se borra. Usuario desactivado después del login pierde la sesión.
- Logout cierra solo esa sesión. Login limpia las vencidas.
- Validaciones de alta de usuario (vacíos, rol inválido, clave corta/larga, usuario repetido → `ErrUsernameTaken`).
- Admin inicial: falla sin contraseña o con contraseña corta; se crea una sola vez; no cambia la clave en el segundo arranque; no pisa a un usuario `admin` con otro rol.
- HTTP: atributos de la cookie, el token no aparece en el cuerpo, login fallido sin cookie, 401/403/200 según rol, logout invalida la cookie vieja.

De punta a punta, imagen Docker contra Postgres 18 descartable, con curl:

- Sin `ADMIN_INITIAL_PASSWORD`: aplica la migración 2 y sale con código 1 y mensaje claro.
- Con la variable: log `admin inicial creado usuario=admin`.
- Admin entra (200, cookie con `HttpOnly; Secure; SameSite=Lax`), clave mal → 401.
- Admin crea el cadete `juan` → 201; repetido → 409; clave corta → 400; lista usuarios → 200.
- **Cadete entra → 200; cadete en `GET` y `POST /api/usuarios` → 403**; sin sesión → 401.
- POST con la cookie del admin pero `Sec-Fetch-Site: cross-site` → 403.
- En la base: hashes bcrypt (`$2a$10$`), `sessions` con el SHA-256 de la cookie y ninguna fila con el token en claro.
- Logout → 204; la misma cookie después → 401.
- Reinicio con otra `ADMIN_INITIAL_PASSWORD`: no crea nada, la clave original sigue andando y la nueva no.

Pantalla, en Chrome headless manejado por DevTools Protocol contra la imagen y un Postgres descartable:

- Sin sesión, la sección de admin está oculta. Al entrar el admin, se ve y la tabla lista al admin.
- Crear el cadete `Juan` → mensaje "Cadete juan creado.", aparece en la tabla y el formulario se vacía. Repetirlo → "Ese usuario ya existe".
- Al salir, la sección se oculta y la tabla se vacía. El cadete entra y no ve la sección, tampoco después de recargar.

### Antes de mergear

- [ ] Definir `ADMIN_INITIAL_PASSWORD` (8 a 72 caracteres) en el servicio de Railway. Sin ella, el deploy de este paso no arranca.
- [ ] Probar en el dominio de Railway: admin entra, crea un cadete, el cadete entra desde el celular y recibe 403 en `/api/usuarios`.

### Fuera de este paso

Sin límite de intentos de login, sin cambio de contraseña ni baja de usuarios desde la API: no los pide la prueba.

## Paso 3 — Pedidos (`spike/pedidos`)

### Qué se hizo

- `db/migrations/00003_pedidos.sql`: tabla `orders` según el modelo del plan.
  - `price_cents BIGINT` con `CHECK (> 0)`: plata en centavos, siempre entero.
  - `status` con `CHECK` (`pendiente`, `asignado`, `entregado`) y otro `CHECK` que ata estado y cadete: `pendiente` ⇔ sin `courier_id`.
  - Direcciones no vacías (`CHECK`). `courier_id` y `created_by` con FK a `users`. Índices por cadete y por fecha.
- Consultas (`db/queries/orders.sql` y `ListActiveCouriers` en `users.sql`). En `sqlc.yaml` se activó `emit_pointers_for_null_types`: las columnas que aceptan `NULL` salen como `*int64`/`*string` en vez de `pgtype` (no cambia nada de lo anterior, que no tenía columnas nulas).
  - `CreateAssignedOrder` es un `INSERT … SELECT … FROM users WHERE id = $cadete AND role = 'cadete' AND active`: si el elegido no es un cadete activo no inserta nada. El control y el alta van en la misma sentencia, sin carrera entre "verificar" y "crear".
  - El cadete lista con una consulta propia (`ListOrdersByCourier`, filtrada por su id); no hay un filtro opcional que se pueda olvidar.
- `internal/orders`: servicio + handlers HTTP.
  - Validación: direcciones recortadas, no vacías y hasta 200 caracteres; precio > 0; cadete obligatorio. Los errores se devuelven en castellano y se muestran tal cual.
  - El pedido se crea ya `asignado` (el paso pide crearlo y asignarlo; no hay reasignación ni cambio de estado todavía).
- `internal/auth`: se agregó `ContextWithUser`, que ahora usa el middleware y sirve para testear handlers de otros paquetes.
- Rutas nuevas:
  - `GET /api/cadetes` (solo admin) → cadetes activos `{id, username, name}`, ordenados por nombre. Es lo que llena la lista para elegir.
  - `POST /api/pedidos` (solo admin) `{"pickup_address","dropoff_address","price_cents","courier_id"}` → 201 con el pedido. `price_cents` tiene que ser un entero JSON: `1500.5` o `"1500"` dan 400.
  - `GET /api/pedidos` (cualquier usuario logueado) → el admin ve todos; el cadete, solo los suyos. Más nuevos primero.
- `web/static/index.html` (sin diseño):
  - Admin: formulario "Nuevo pedido" con retiro, entrega, precio en pesos y un `<select>` con los cadetes activos ("Nombre (usuario)"). Si no hay ninguno, la lista lo dice y el botón queda deshabilitado. Al dar de alta un cadete, la lista se recarga sola.
  - El precio se escribe en pesos (`1500`, `1500,50` o `1500.50`) y se pasa a centavos con un parseo de texto, sin punto flotante. `1.500` se rechaza (sin separador de miles) para no confundirlo con 1,5.
  - Tabla de pedidos para los dos roles (el cadete la ve como "Mis pedidos", sin la columna Cadete), con botón "Actualizar". Precio con formato `$ 1.500,50`, fecha en hora de Argentina.

### Cómo se probó

Tests (`go test -race ./...`; `internal/orders` con un store en memoria que imita el `INSERT … SELECT`):

- Crear: queda `asignado` al cadete elegido, direcciones recortadas, `created_by` = el admin.
- Validaciones: sin retiro/entrega, solo espacios, 201 caracteres (200 con `ñ` pasa), precio 0 o negativo, sin cadete → error de dato, sin crear nada.
- Cadete inexistente, inactivo o que es admin → `ErrInvalidCourier`, sin crear nada.
- Listado: el cadete ve solo los suyos (más nuevo primero), el admin todos, un cadete sin pedidos recibe `[]` y no `null`.
- Cadetes activos: sin admin ni inactivos, ordenados por nombre.
- HTTP: 201 al crear; 400 con precio decimal, precio como texto, campo desconocido (p. ej. `status`), cadete inactivo o sin precio; 401 sin usuario; 500 sin detalles si falla la base.

De punta a punta, imagen Docker contra Postgres 18 descartable, con curl:

- Arranque: aplica la migración 3 (`base al día version=3`).
- Admin crea `juan`, `ana` y `pedro`; se desactiva `pedro` en la base. `GET /api/cadetes` → solo Ana y Juan.
- Pedido a Juan y a Ana → 201. A Pedro (inactivo), al admin o a un id inexistente → 400 "El cadete elegido no existe o no está activo".
- Precio `10.5` → 400; precio 0 → 400; dirección con solo espacios → 400.
- Admin lista los dos; **Juan lista solo el suyo**.
- Juan en `POST /api/pedidos` y en `GET /api/cadetes` → 403. Sin sesión → 401. POST con `Sec-Fetch-Site: cross-site` → 403.
- En la base: `price_cents` guardado como entero (150050, 90000). Un `INSERT` a mano `asignado` sin cadete, o `pendiente` con cadete, lo rechaza el `CHECK`.

Pantalla, en Chrome headless manejado por DevTools Protocol, admin y cadete en contextos aislados (el cadete con viewport de celular de 390 px):

- La lista de cadetes muestra "Elegí un cadete", Ana y Juan (no Pedro). Sin elegir, el formulario no se envía.
- Precios `1.500`, `abc` y `0` → "Precio inválido…", sin crear nada.
- Admin crea un pedido de `1500,50` a Juan → "Pedido N creado y asignado.", aparece primero con `$ 1.500,50`, Juan y `asignado`; el formulario se vacía. `2000.5` a Ana → `$ 2.000,50`.
- **Juan no lo ve hasta recargar; al recargar aparece primero**, y no ve el pedido de Ana. Sin scroll horizontal a 390 px.
- Alta del cadete Zoe desde la pantalla → aparece en la lista sin recargar.
- Al salir, la tabla de pedidos se oculta y se vacía.

### Antes de mergear

- [x] Probar en el dominio de Railway: el admin crea un pedido desde la compu eligiendo un cadete, y aparece en el celular del cadete al recargar.

### Fuera de este paso

Sin pedidos `pendiente` (sin cadete), reasignación, marcar como entregado ni vista de detalle `/pedidos/{id}` (llega con el push en el paso 4). Los listados no tienen paginación: alcanza para la prueba.
