# Prueba técnica — app de cadetes

Una semana para confirmar que Go + Postgres + PWA en Railway resuelve lo crítico. Si pasa, el stack queda cerrado.

## Objetivo y criterio de éxito

La prueba pasa si funcionan estas cuatro cosas, desplegadas en Railway y probadas desde celulares reales. Nada más: no es el MVP.

- [ ] **Login** con dos roles: admin y cadete.
- [ ] **Crear un pedido**: el admin lo carga y se lo asigna a un cadete.
- [ ] **Push con pantalla apagada**: al cadete le llega la notificación en menos de 10 segundos y, al tocarla, abre el pedido.
- [ ] **Deploy automático**: un push a `main` en GitHub actualiza la app en Railway sin pasos manuales.

Si las cuatro pasan, el stack queda cerrado. Si falla una, se cambia solo esa pieza (ver "Si algo falla").

## Stack y estructura

Un solo binario de Go sirve la API y el frontend. Un solo servicio en Railway, más Postgres.

| Pieza | Elección | Para qué |
| --- | --- | --- |
| Lenguaje | Go (última versión estable) | Backend completo |
| Rutas HTTP | `net/http` estándar | Sin framework: el router estándar ya soporta métodos y parámetros |
| Base de datos | Postgres + `pgx/v5` | Conexión |
| Consultas | `sqlc` | SQL escrito a mano, código Go generado y tipado |
| Migraciones | `goose` | Versionadas en el repo, se aplican al arrancar |
| Contraseñas | `bcrypt` (`golang.org/x/crypto`) | Hash seguro |
| Sesiones | Cookie `HttpOnly` + token con hash en la base | Nunca se guarda el token en claro |
| Push | `webpush-go` + claves VAPID | Notificaciones al celular |
| Frontend | Vite + PWA | Se compila y se embebe con `go:embed` |
| Deploy | Dockerfile multi-etapa en Railway | Portable a cualquier proveedor |

```text
cadetes/
├── cmd/server/main.go        # arranque, rutas, migraciones
├── internal/
│   ├── auth/                 # login, sesiones, roles
│   ├── orders/               # pedidos
│   ├── push/                 # suscripciones y envío de push
│   └── db/                   # código generado por sqlc
├── db/
│   ├── migrations/           # goose
│   └── queries/              # .sql para sqlc
├── web/                      # frontend Vite (PWA + service worker)
├── docs/                     # especificación, estado, design system
├── Dockerfile
└── sqlc.yaml
```

Variables de entorno en Railway: `DATABASE_URL`, `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY`, `VAPID_SUBJECT` (un `mailto:`) y `ADMIN_INITIAL_PASSWORD`. El primer admin se crea al arrancar solo si no existe ninguno: nada de usuarios demo.

## Modelo de datos mínimo

| Tabla | Columnas | Nota |
| --- | --- | --- |
| `users` | id, username, password_hash, role, name, active, created_at | `role` es `admin` o `cadete` |
| `sessions` | token_hash, user_id, expires_at | Se guarda el hash, nunca el token |
| `orders` | id, pickup_address, dropoff_address, price_cents, status, courier_id, created_by, created_at | `status`: `pendiente`, `asignado`, `entregado`. Plata en centavos |
| `push_subscriptions` | id, user_id, endpoint, p256dh, auth, user_agent, created_at | Varios dispositivos por usuario. Si el push devuelve 404 o 410, se borra la fila |

## Plan día por día

Una rama por paso. Cada paso cierra con una prueba concreta que tiene que pasar antes de mergear.

1. **Esqueleto y deploy** — rama `spike/esqueleto`
    - Servidor Go con `net/http` que responda `GET /health` y sirva un `index.html` embebido con `go:embed`. Dockerfile multi-etapa (build con la imagen oficial de Go, final con distroless o alpine). Conexión a Postgres con `pgx` usando `DATABASE_URL` y migraciones con `goose` al arrancar. Sin lógica de negocio.
    - Prueba: repo conectado a Railway con Postgres; `/health` responde `ok` en el dominio HTTPS de Railway; un cambio en `main` se despliega solo.
2. **Login y roles** — rama `spike/auth`
    - Tablas `users` y `sessions` con goose, consultas con sqlc. Login con bcrypt; cookie `HttpOnly`, `Secure`, `SameSite=Lax` con token aleatorio; en la base solo su hash SHA-256. Middleware que exige rol. Admin inicial con `ADMIN_INITIAL_PASSWORD` solo si no existe ninguno. Tests de la lógica de sesión.
    - Prueba: el admin entra, el cadete entra, el cadete recibe 403 en una ruta de admin.
3. **Pedidos** — rama `spike/pedidos`
    - Tabla `orders`. El admin crea un pedido y lo asigna a un cadete; el cadete ve solo los suyos. Precio en centavos. Pantallas mínimas, sin diseño.
    - Prueba: se crea un pedido desde la compu y aparece en el celular del cadete al recargar.
4. **Push** — rama `spike/push`
    - Tabla `push_subscriptions`. Frontend: `manifest.webmanifest`, service worker y botón "Activar notificaciones" que pide permiso con un toque del usuario y guarda la suscripción. Servidor: al asignar un pedido, push con `webpush-go` a todos los dispositivos del cadete, con urgencia alta. Al tocar la notificación se abre `/pedidos/{id}`. Borrar suscripciones que devuelvan 404 o 410. Comando para generar las claves VAPID.
    - Prueba: con el celular bloqueado, se asigna un pedido y suena la notificación.
5. **Pruebas en celulares reales** — sin rama
    - Completar la tabla de abajo.
    - Nota corta en `docs/estado.md`: qué pasó, qué falló y si el stack queda cerrado. Tag `v0.0.1-spike`.

En iPhone, el push solo funciona con la app agregada a la pantalla de inicio y el permiso pedido desde un botón, no al cargar la página.

## Prueba de push en celulares reales

| Dispositivo | Escenario | Resultado | Demora (s) | Notas |
| --- | --- | --- | --- | --- |
| Android gama baja | Bloqueado 1 min | Pendiente | | |
| Android gama baja | Bloqueado 15 min | Pendiente | | |
| Android gama baja | App cerrada (deslizada) | Pendiente | | |
| Android con ahorro de batería | Bloqueado 15 min | Pendiente | | |
| Android con ahorro de batería | App cerrada | Pendiente | | |
| iPhone con la app instalada | Bloqueado 1 min | Pendiente | | |
| iPhone con la app instalada | Bloqueado 15 min | Pendiente | | |
| iPhone con la app instalada | App cerrada | Pendiente | | |

Criterio: pasa si llega en menos de 10 segundos en todos los casos de Android sin ahorro de batería y en el iPhone. Si falla solo con ahorro de batería, se resuelve configurando ese teléfono, no cambiando el stack.

## Si algo falla

Se cambia solo la pieza que falla, nunca todo el stack.

| Si falla… | Plan B | Qué se mantiene |
| --- | --- | --- |
| Push en la PWA | Empaquetar el mismo frontend con Capacitor y push por Firebase Cloud Messaging | Backend, base y deploy |
| Deploy en Railway | Mismo Dockerfile en otro proveedor (Render, Fly.io o un VPS) | Todo el código |
| sqlc o goose traban | Seguir con `pgx` y SQL a mano por ahora | El resto |

Fuera de la prueba: zonas y tarifas, comercios, pedido público como invitado, tiempo real con WebSockets, reportes, diseño visual y dominio del cliente.

Prueba en Railway con el crédito gratuito o el plan Hobby; producción con el cliente, en Pro.
