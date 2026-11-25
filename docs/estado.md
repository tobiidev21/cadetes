# Estado

Prueba técnica: hecha. Stack confirmado.

Pasos 1 a 5 hechos.

## Resultados de la prueba de push

| Dispositivo | Escenario | Resultado | Demora | Notas |
| --- | --- | --- | --- | --- |
| iPhone (PWA agregada desde Safari) | Desbloqueado | Llegó | < 1 s | Al tocar abre el pedido |
| iPhone (PWA agregada desde Safari) | Bloqueado 20 min, app cerrada | Llegó | Instantáneo | |
| Samsung Galaxy A22+ (Chrome) | Uso normal | Llegó | Inmediato | Al tocar abre el pedido |
| Samsung Galaxy A22+ (Chrome) | Ahorro de batería activado, sin excepción | No llegó hasta desbloquear | Al desbloquear (+2 a 3 s) | Pasa aunque el teléfono esté bloqueado poco tiempo: lo causa el ahorro de batería, no la espera |
| Samsung Galaxy A22+ (Chrome) | Ahorro activado, app "Cadetes" en "Aplicaciones que nunca se suspenden" | Llegó | 4 a 5 s | Chrome no aparece en esas listas de Samsung; la PWA instalada sí, como app aparte |

## Conclusión

El stack Go + Postgres + PWA en Railway queda confirmado: login, pedidos, push y deploy automático funcionan en producción y en celulares reales.

Web Push en PWA alcanza; no hace falta Capacitor. La demora con ahorro de batería en Samsung se debe al sistema y no a la app. Capacitor no la resolvería, porque Chrome en Android ya recibe el push por Firebase Cloud Messaging, igual que una app nativa. Se resuelve con la configuración del teléfono.

En iPhone, el push solo funciona con la app agregada a la pantalla de inicio desde Safari y abierta desde ese ícono.

En Samsung con ahorro de batería, la solución que funcionó fue agregar la app "Cadetes" a "Aplicaciones que nunca se suspenden" (Ajustes → Batería → Límites de uso en segundo plano). Es el paso que tiene que incluir la guía de configuración para cadetes.

## Pendientes para el MVP

- **Aviso en pantalla bloqueada**: hoy muestra las direcciones de retiro y de entrega. Propuesta: mostrar solo el retiro (suele ser un comercio) y la entrega recién al abrir la app con sesión. Consultarlo con el cliente.
- **Ids consecutivos en el aviso**: "Pedido N° 42" deja inferir el volumen de pedidos. Usar un código corto.
- **Suscripción push al cerrar sesión**: hoy no se borra. Al cerrar sesión, borrar la suscripción de ese dispositivo.
- **Guía de configuración por marca** dentro de la app (Samsung, Xiaomi, Motorola, etc.) para que el ahorro de batería no frene los avisos, más un botón de "aviso de prueba" en el perfil del cadete.
- **Límite de intentos de login** contra fuerza bruta.
- **Funciones de JSON duplicadas** entre auth, orders y push: pasarlas a un paquete común.
- **Vite en el frontend** (requiere Node en el Dockerfile).
- **Entorno de pruebas**: hoy solo se prueba en Railway después del merge. Usar entornos por PR o un staging aparte.
- **Proyecto de producción desde cero**: repo, Railway y base nuevos, sin datos ni usuarios de la prueba técnica. Producción en Railway Pro.

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

- [x] Crear proyecto en Railway, agregar Postgres y conectar el repo de GitHub (rama `main`).
- [x] En el servicio de la app, definir `DATABASE_URL` como referencia a la del Postgres (`${{Postgres.DATABASE_URL}}`).
- [x] Generar dominio público y comprobar que `https://<dominio>/health` responde `ok`.
- [ ] Opcional: configurar `/health` como healthcheck del servicio en Railway.
- [x] Hacer un cambio en `main` y verificar que se despliega solo.

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

- [x] Definir `ADMIN_INITIAL_PASSWORD` (8 a 72 caracteres) en el servicio de Railway. Sin ella, el deploy de este paso no arranca.
- [x] Probar en el dominio de Railway: admin entra, crea un cadete, el cadete entra desde el celular y recibe 403 en `/api/usuarios`.

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

## Paso 4 — Push (`spike/push`)

### Qué se hizo

- `db/migrations/00004_push.sql`: tabla `push_subscriptions` según el modelo del plan. `endpoint` único (identifica al dispositivo); `ON DELETE CASCADE` con `users`.
- `internal/push` (con `webpush-go` v1.4.0):
  - **Configuración**: `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY` y `VAPID_SUBJECT` son obligatorias; si falta alguna o está mal, **la app no arranca** y dice cuál. Se valida que las claves tengan el formato correcto, que la pública corresponda a la privada (detecta claves al revés o de pares distintos) y que el subject sea `mailto:` con un email válido o una URL `https://`.
  - **Suscripción**: `POST /api/push/suscripciones` (solo cadetes) recibe `PushSubscription.toJSON()` y hace upsert por endpoint: si en el mismo teléfono entra otro cadete y activa, el dispositivo pasa a ser suyo. Se validan las claves del navegador (`p256dh` punto P-256, `auth` 16 bytes).
  - **Solo servicios push conocidos**: el endpoint tiene que ser `https` en FCM (Chrome/Android), Mozilla, Apple o Windows. El servidor hace un POST a esa URL; sin esta lista, un usuario podría registrar una URL interna y usar el servidor como proxy. El cliente HTTP del envío tampoco sigue redirecciones.
  - **Envío**: al crear un pedido, `orders` avisa a `push` (interfaz `orders.Notifier`), que manda a **todos los dispositivos del cadete** en una goroutine aparte: la respuesta al admin no espera a los servicios push. Urgencia `high`, TTL 1 hora (webpush-go manda `TTL: 0` si no se indica, y con 0 un teléfono sin señal en ese momento no lo recibe nunca). Mensaje: `Nuevo pedido N° X`, retiro y entrega, URL `/pedidos/X`.
  - Si el servicio push responde **404 o 410, se borra la suscripción**; cualquier otro error se registra y se sigue con el resto de los dispositivos. Cada envío deja en el log cuántos dispositivos había, cuántos se enviaron, cuántos se borraron y cuánto tardó (sirve para la tabla del paso 5).
  - webpush-go le antepone `mailto:` al subject si no empieza con `https:`; con `VAPID_SUBJECT=mailto:...` el JWT quedaba `mailto:mailto:...`, que Apple rechaza. Se le pasa sin el prefijo (hay test que revisa el JWT).
  - Al apagar, después de cerrar el servidor HTTP se esperan los envíos en curso (dentro del mismo timeout de 10 s).
- `cmd/vapid`: `go run ./cmd/vapid` imprime `VAPID_PUBLIC_KEY=…` y `VAPID_PRIVATE_KEY=…` listas para pegar.
- `orders`: `GET /api/pedidos/{id}` (cualquier usuario logueado). El cadete solo ve los suyos; si pide uno ajeno recibe 404, igual que si no existiera.
- Rutas nuevas: `GET /api/push/clave` (clave VAPID pública, con sesión), `POST /api/push/suscripciones` (cadete), `GET /api/pedidos/{id}`, y `GET /pedidos/{id}` que sirve la misma página.
- Frontend (sigue siendo HTML estático, sin Vite):
  - `manifest.webmanifest` (`display: standalone`, íconos 192 y 512 generados, sin diseño), `apple-touch-icon` y `theme-color`. Se registra el tipo `application/manifest+json`, porque la imagen distroless no trae `/etc/mime.types`.
  - `sw.js`: solo push, no cachea nada. Muestra siempre una notificación por cada push (Safari da de baja la suscripción si no lo hace). Al tocarla, si la app ya está abierta la lleva a `/pedidos/X` y la trae al frente; si no, la abre ahí. Maneja `pushsubscriptionchange`. Se sirve con `Cache-Control: no-cache`.
  - Sección "Notificaciones" solo para cadetes, con botón **"Activar notificaciones"**: el permiso se pide solo al tocarlo, nunca al cargar. Al abrir la app, si ya estaba activada reenvía la suscripción (por si el servidor la borró o cambió el cadete) y, si cambiaron las claves VAPID, se da de baja para volver a activar. En iPhone sin instalar explica cómo agregar la app a la pantalla de inicio; si el permiso está bloqueado, lo dice.
  - `/pedidos/{id}` muestra el detalle del pedido arriba de la lista; el N° de cada fila es un enlace.

### Cómo se probó

Tests (`go test -race ./...`):

- `internal/push`:
  - Configuración: claves recién generadas pasan, también con relleno `=`. Fallan si están vacías, rotas, al revés, de pares distintos, o con un subject sin esquema, `http:` o `mailto:` sin email válido.
  - Endpoints aceptados: FCM, Mozilla, Apple, Windows y en mayúsculas. Rechazados: `http`, dominio ajeno, `fcm.googleapis.com.evil…`, `evilfcm.googleapis.com`, con usuario, con puerto, IP, `localhost` y endpoints demasiado largos. Claves del navegador inválidas también se rechazan.
  - El upsert le pasa el dispositivo a otro usuario. El user agent se recorta sin cortar caracteres UTF-8 por la mitad.
  - Con respuestas 201, 410, 404, 500, 429 y error de red, solo se borran las de 404 y 410; los dispositivos de otro usuario no reciben nada.
  - El mensaje de pedido asignado tiene título, cuerpo, URL y tag correctos; un pedido sin cadete no manda nada.
  - **Envío real con webpush-go a un servicio push de prueba (TLS)**: el test descifra el cuerpo como lo haría el navegador (RFC 8291) y obtiene el mensaje original. Revisa `Urgency: high`, `TTL: 3600`, `Content-Encoding: aes128gcm`, el JWT VAPID (`sub` con un solo `mailto:`, `aud` = origen del endpoint) y que no sigue redirecciones.
  - HTTP: 204 con el JSON tal cual lo arma el navegador (con `expirationTime: null`); 400 con endpoint ajeno o JSON inválido; 401 sin usuario.
- `internal/orders`:
  - Crear avisa una vez al cadete correcto; datos inválidos o un cadete inactivo no avisan.
  - Detalle: cada cadete ve el suyo; el ajeno o un id inexistente dan 404; el admin ve cualquiera.

De punta a punta, imagen Docker contra Postgres 18 descartable:

- Sin variables VAPID, con `VAPID_SUBJECT` sin `mailto:` o con las claves al revés: sale con código 1 y el mensaje que corresponde. Con todo bien, aplica la migración 4.
- curl:
  - `GET /api/push/clave` → 200 con sesión y 401 sin sesión.
  - El admin en `POST /api/push/suscripciones` → 403. Un endpoint `https://169.254.169.254/…` → 400.
  - El cadete pidiendo un pedido ajeno → 404.
  - `/pedidos/1` devuelve la página; el manifest sale como `application/manifest+json`, `sw.js` con `no-cache`.
- Envío de verdad desde la app: suscripciones del cadete apuntando a un servicio push falso (que descifra) y a FCM con un token inventado.
  - Al crear el pedido, la respuesta tardó 8 ms.
  - El servicio falso recibió el mensaje cifrado, lo descifró bien y vio `Urgency=high` y `TTL=3600`. Respondió 201 en `/ok` y 410 en `/gone`.
  - **FCM real respondió 410 al token inventado**. Las dos suscripciones con 410 se borraron y la del otro cadete no recibió nada. Log: `dispositivos=3 enviados=1 borrados=2 duracion=1.482s`.
- Chrome headless por DevTools Protocol:
  - El cadete ve la sección y el botón; el admin no.
  - `/pedidos/{id}` muestra el detalle del pedido propio; uno ajeno muestra "No encontramos ese pedido o no está asignado a vos."
  - **No se pudo completar la suscripción**: el Chrome headless de esta máquina no logra registrarse en FCM (`PHONE_REGISTRATION_ERROR` / `wrong_secret` en el log de GCM). Que llegue a un navegador real queda para los celulares.

**No probado todavía**: la notificación en un teléfono real (Android e iPhone), el toque que abre `/pedidos/X` y el apagado esperando envíos en curso.

### Antes de mergear

- [x] Generar las claves con `go run ./cmd/vapid` y cargar en Railway `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY` y `VAPID_SUBJECT` (`mailto:` con un email real). **Sin ellas el deploy de este paso no arranca.**
- [x] Probar en el dominio de Railway: el cadete toca "Activar notificaciones" en el celular (en iPhone, con la app agregada a la pantalla de inicio), bloquea el teléfono, el admin le asigna un pedido y suena la notificación. Al tocarla se abre el pedido.

### Fuera de este paso

- Al salir no se borra la suscripción del dispositivo: si nadie vuelve a entrar en ese teléfono, le siguen llegando los avisos del último cadete.
- No hay límite de dispositivos por usuario, ni envío de prueba desde la pantalla.
- Vite todavía no entró: el frontend sigue siendo estático en `web/static`. Conviene sumarlo en un paso aparte, porque cambia el Dockerfile (etapa de Node).

## Prueba GPS 1a — Backend de posiciones (`spike/gps-1a`)

Primera de cuatro partes de la prueba de rastreo (ver `docs/spike-gps.md`, que tiene la tabla de mediciones y la API).

### Qué se hizo

- `db/migrations/00005_positions.sql`: tabla `positions` (nombres en inglés, como el resto del esquema; la API habla en castellano).
  - `lat`/`lng` `double precision`, `accuracy_m` `real`, `speed_ms` y `heading_deg` opcionales, `captured_at` (hora del celular), `received_at` (`now()` del servidor), `source` (`pwa` o `nativa`), `visible` opcional. Todo con `CHECK` de rangos.
  - Índice **único** en `(courier_id, captured_at DESC)`: un lote reenviado no duplica posiciones (el insert usa `ON CONFLICT DO NOTHING`).
- `db/queries/positions.sql`:
  - `InsertPositions`: el lote entero en una sentencia con `jsonb_to_recordset` (los arreglos de sqlc no admiten `NULL`, que hacen falta para los opcionales). Devuelve solo las filas nuevas.
  - `LatestPositions` (`DISTINCT ON`), `CourierTrack` (tope de 10.000 desde la más vieja) y `CourierSummary` (con `lag()`: cantidad, intervalo medio, huecos de más de 60 s y el mayor, demora media y máxima, precisión media, `visible = false`). sqlc no deja devolver agregados nulos: salen con `COALESCE` y el servicio los vuelve `null` cuando no hay datos.
- `internal/positions`:
  - Validación del lote (1 a 100, rangos, `capturada_en` entre −24 h y +1 min, origen). Un error rechaza el lote entero con 400 y dice qué posición falló.
  - El cadete sale de la sesión; un `cadete_id` en el body da 400 (campo desconocido).
  - Límite por cadete en memoria (token bucket sin dependencias): 10 seguidos y 1 cada 2 s; si se pasa, 429 con `Retry-After`.
  - Difusor en memoria: cada suscriptor tiene un buffer de 64 y, si se llena, se le descartan mensajes. Publicar nunca bloquea.
  - SSE con `net/http` y `http.ResponseController`: heartbeat cada 15 s, se quita el `WriteTimeout` (30 s) solo para esa conexión y sale al cortarse el contexto del request.
- `cmd/server/main.go`: rutas `POST /api/posiciones` (cadete) y `GET /api/posiciones/{stream,ultimas,recorrido,resumen}` (admin). `srv.RegisterOnShutdown(broker.Close)`: sin eso, un stream abierto colgaba el apagado 10 s.
- Se corrigieron 3 avisos de `errcheck` que ya estaban (`main.go` y `push.go`) para dejar `golangci-lint` limpio.

### Cómo se probó

Tests (`go test -race ./...`):

- Validación: cada regla con su mensaje (bordes incluidos: 100 pasan y 101 no, +59 s pasa y +61 s no, rumbo 359,9 pasa y 360 no). Un lote con una sola posición mala no llega a la base.
- `cmd/server/routes_test.go`, con el mux real y login real sobre un store en memoria: sin sesión, POST y los 4 GET dan 401; un cadete en los GET da 403; un admin en POST da 403; un cadete en POST da 204.
- `cadete_id` en el body (arriba o dentro de una posición) da 400 y no guarda nada; un lote válido se guarda con el id de la sesión.
- Stream contra un `httptest.Server` con `ReadTimeout` y `WriteTimeout` de 300 ms: sigue vivo pasado el timeout, llegan pings y el evento con la posición recién insertada. Al cortar el cliente se libera la suscripción; al cerrar el broker, el stream termina. Sin el `SetWriteDeadline` el test falla.
- Difusor: un suscriptor que no lee no bloquea `Publish` y se le descartan exactamente los mensajes que no entran; el otro recibe todos.
- Límite: la ráfaga pasa, el siguiente da 429 con `Retry-After: 2`, vuelve a pasar al reponerse y no afecta a otros cadetes.
- **Contra Postgres 18** (Podman, `TEST_DATABASE_URL`; sin ella se saltean):
  - Insert con opcionales nulos y milisegundos que se conservan; el reenvío del lote inserta 0 filas. Otro cadete en el mismo instante no es duplicado. El `CHECK` rechaza un origen inválido.
  - Últimas posiciones por cadete y recorrido desde una hora.
  - Resumen con datos fijos (capturas a 0, 10, 20, 100, 110 y 300 s, demoras de 1 a 9 s): 6 posiciones, intervalo medio de 60 s, 2 huecos con el mayor de 190 s, demora media de 4 s y máxima de 9 s, precisión media de 17,5 m, 2 no visibles. Las posiciones fuera de `[desde, hasta)` no cuentan. Rango vacío da `null`.

De punta a punta, el binario contra una base nueva en Podman, con curl:

- Arranque: aplica la migración 5 (`base al día version=5`).
- Lotes como cadete: 204. Con `curl -N` abierto como admin llegan los eventos `posiciones` y los `: ping`, y el stream sigue vivo pasados 34 s (más que el `WriteTimeout`).
- Resumen del lote de prueba: intervalo de 26 s, 1 hueco de 90 s, precisión media de 13 m y 1 no visible, como se esperaba.
- Cadete en `ultimas`, `stream` y `resumen`: 403. POST sin sesión: 401. Admin en POST: 403. Latitud 95: 400 con mensaje. `cadete_id` en el body: 400.
- 12 envíos seguidos: 10 dan 204 y 2 dan 429 con `Retry-After: 2`.
- SIGTERM con un stream abierto: el servidor se apaga en 0,05 s y cierra el stream.

### Fuera de este paso

Pantallas (1b), Capacitor (2a y 2b), limpieza de posiciones viejas, PostGIS y varias instancias del servidor (el difusor es en memoria).
