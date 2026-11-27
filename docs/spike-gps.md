# Prueba técnica de GPS

Objetivo: saber si el admin ve al cadete en el mapa con el celular bloqueado o con otra app encima, con la PWA y con Capacitor en Android.

Cómo medir: anotar hora de inicio y fin de cada prueba y consultar el resumen en el mapa del admin.

| Prueba | Dispositivo | Origen | Posiciones | Intervalo medio | Huecos > 60 s (cantidad / mayor) | Demora media | Precisión media | Batería (inicio → fin) | Observaciones |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| App abierta, 10 min | iPhone | PWA | | | | | | | |
| App abierta, 10 min | Samsung | PWA | | | | | | | |
| Pantalla bloqueada, 20 min | iPhone | PWA | | | | | | | |
| Pantalla bloqueada, 20 min | Samsung | PWA | | | | | | | |
| WhatsApp encima, 10 min | iPhone | PWA | | | | | | | |
| WhatsApp encima, 10 min | Samsung | PWA | | | | | | | |
| Ahorro de batería, 20 min | Samsung | PWA | | | | | | | |
| Modo avión 5 min en medio | iPhone | PWA | | | | | | | |
| Modo avión 5 min en medio | Samsung | PWA | | | | | | | |
| 1 h rastreando (batería) | iPhone | PWA | | | | | | | |
| 1 h rastreando (batería) | Samsung | PWA | | | | | | | |

## Conclusiones

(Completar al terminar las cuatro partes.)

## Cómo hacer cada prueba

Las pantallas son **Rastreo (prueba)** (`/rastreo.html`, solo cadetes) y **Mapa (prueba)** (`/mapa.html`, solo admins). Se entra desde los links de la pantalla principal, después de iniciar sesión.

El navegador solo da la ubicación en páginas seguras: HTTPS, o `localhost` en la compu. En la calle se prueba siempre contra la URL de Railway.

### Qué muestra cada pantalla

**Rastreo (prueba)**, en el celular del cadete:

| Dato | Qué es |
| --- | --- |
| Estado | Rastreando o Detenido |
| Enviadas | posiciones que el servidor confirmó desde que se tocó "Empezar" |
| Pendientes en cola | capturadas que todavía no llegaron al servidor. Se guardan en el celular (`localStorage`) y se reintentan cada 10 s, aunque se recargue la página. Tope: 2.000 |
| Descartadas | las que no se van a mandar: inválidas (mismas reglas que el servidor), las que pasaron el tope de 2.000 o un lote que el servidor rechazó con 400 (el motivo queda en pantalla y en la consola) |
| Última precisión y última posición | lo último que dio el GPS |
| Pasó a segundo plano | cuántas veces la página dejó de estar a la vista (pantalla bloqueada, otra app encima, cambio de pestaña) |
| Reanudaciones | cuántas veces la página se recargó mientras rastreaba y retomó sola (por ejemplo, si el sistema la descartó en segundo plano) |
| Errores de GPS | veces que el GPS no pudo dar la ubicación |

Cada posición se manda con `visible` (si la página estaba a la vista al capturarla). "Mantener pantalla encendida" usa Screen Wake Lock y solo aparece si el navegador lo soporta.

Al tocar "Empezar", los contadores vuelven a cero. Las pendientes de antes no se borran: se siguen enviando.

**Mapa (prueba)**, en la compu del admin:

- Un punto por cadete con su nombre y la hora de la última posición, el círculo de precisión y la línea del recorrido de las últimas 2 h.
- Se actualiza solo (stream SSE). Si se corta, arriba aparece "Reconectando…" y al volver se piden de nuevo las últimas posiciones y los recorridos, así no se pierde nada. Railway corta el stream cada 15 minutos: es normal que el aviso aparezca unos segundos.
- Al tocar un cadete en la lista (o su punto), el panel muestra la última posición, la demora (`recibida_en − capturada_en`), la precisión, el origen y si estaba visible. Abajo está el formulario **Resumen de una prueba**: con desde y hasta (hora de la compu, con segundos) muestra el resumen de `/api/posiciones/resumen` y un link al JSON crudo.

### Antes de mergear, en la compu

1. Levantá el servidor local. Abrí dos ventanas de Chrome (una de ellas de incógnito, para tener dos sesiones): una como cadete en "Rastreo (prueba)" y otra como admin en "Mapa (prueba)".
2. En la ventana del cadete: DevTools → menú de tres puntos → More tools → Sensors → Location. Elegí una ciudad o cargá coordenadas a mano y tocá "Empezar". Aceptá el permiso de ubicación.
3. Cambiá las coordenadas varias veces. El punto tiene que moverse en el mapa del admin (a lo sumo 10 s después, que es cada cuánto se envía) y dejar la línea del recorrido.
4. Apagá el servidor, cambiá las coordenadas un par de veces y volvé a prenderlo. "Pendientes en cola" sube mientras está apagado y vuelve a 0 solo. En el mapa, los puntos llegan con su hora original (la demora de esas posiciones es de varios segundos).

Lo mismo, automático, con `scripts/verificar-gps.mjs` (ver abajo).

### Después de mergear, con los celulares

1. Esperá el deploy en Railway.
2. En la compu, entrá como admin y abrí "Mapa (prueba)". Dejalo abierto toda la sesión de pruebas: a los 15 minutos Railway corta el stream y el mapa se tiene que reconectar solo en unos segundos, sin recargar y sin perder posiciones.
3. En el iPhone (la PWA agregada desde Safari) y en el Samsung, entrá como cadete, abrí "Rastreo (prueba)" y tocá "Empezar". Aceptá el permiso de ubicación. Si se negó, la pantalla explica cómo habilitarlo.
4. Para cada fila de la tabla:
   1. Anotá la hora de inicio (la que muestra el celular, con segundos) y la batería.
   2. Tocá "Empezar" (pone los contadores en cero) y hacé la prueba durante el tiempo de la fila:
      - **App abierta:** la pantalla de rastreo a la vista todo el tiempo. Sirve de referencia. Podés activar "Mantener pantalla encendida".
      - **Pantalla bloqueada:** tocá "Empezar" y bloqueá el celular.
      - **WhatsApp encima:** tocá "Empezar" y abrí WhatsApp (o cualquier otra app) sin cerrar la PWA.
      - **Ahorro de batería (Samsung):** activá el ahorro de batería antes de empezar y bloqueá la pantalla.
      - **Modo avión 5 min en medio:** con la app abierta, a mitad de la prueba poné modo avión 5 minutos y sacalo. Las posiciones de esos minutos tienen que aparecer después, con demora alta; si no aparecen, el GPS no dio posiciones sin red.
      - **1 h rastreando:** la app abierta (o como se vaya a usar de verdad) durante una hora, para medir la batería.
   3. Al terminar, volvé a la app, mirá "Pendientes en cola" y esperá a que llegue a 0. Anotá la hora de fin, la batería y los contadores de "Pasó a segundo plano" y "Reanudaciones".
   4. En el mapa, elegí el cadete, cargá desde y hasta, y tocá "Ver resumen". Pasá los números a la tabla.
5. Los huecos de más de 60 s, las posiciones con `visible = false` (columna "No visibles") o la falta de posiciones con la app en segundo plano son lo que responde la pregunta. Si en segundo plano no llega nada, el resumen va a mostrar un hueco del largo de la prueba y 0 no visibles.

### Verificación automática con Chrome headless

`scripts/verificar-gps.mjs` hace los pasos de "Antes de mergear" solo, con Chrome sin ventana: simula la ubicación, comprueba que el mapa sigue al cadete, corta la red del cadete y mira que la cola se vacíe al volver con las horas originales, recarga la página del cadete mientras rastrea, compara el resumen del panel con la API y revisa que nada se pida a un CDN. No instala nada: usa Node 22 y el Chrome de la compu.

```sh
VERIF_URL=http://localhost:8080 ADMIN_PASS=… CADETE_USER=… CADETE_PASS=… node scripts/verificar-gps.mjs
```

- `--servidor-apagado`: en vez de cortar la red del cadete, pide apagar y prender el servidor de verdad (espera un Enter en cada paso). Además comprueba que el mapa muestre "Reconectando…".
- `--ver`: abre Chrome con ventana, para mirar lo que hace.
- `CHROME=…` si el ejecutable no se llama `google-chrome`.

Deja posiciones del cadete en la base: usalo contra la base local, no contra Railway.

## API de posiciones

Todas las rutas piden sesión (cookie `cadetes_sesion`). Sin sesión responden **401**; con el rol equivocado, **403**. Los errores vienen como `{"error": "mensaje"}`, en castellano y listos para mostrar.

Las fechas van en RFC 3339. En la URL, el `+` de una zona horaria va como `%2B` (o usar `Z` o `-03:00`).

| Ruta | Quién | Para qué |
| --- | --- | --- |
| `POST /api/posiciones` | cadete | Enviar un lote de posiciones |
| `GET /api/posiciones/stream` | admin | Recibir en vivo cada lote guardado (Server-Sent Events) |
| `GET /api/posiciones/ultimas` | admin | Última posición de cada cadete (estado inicial del mapa) |
| `GET /api/posiciones/recorrido?cadete=ID&desde=FECHA` | admin | Recorrido de un cadete desde una hora |
| `GET /api/posiciones/resumen?cadete=ID&desde=FECHA&hasta=FECHA` | admin | Números de una prueba (la tabla de arriba) |

### `POST /api/posiciones` (cadete)

```http
POST /api/posiciones
Content-Type: application/json

{
  "posiciones": [
    {
      "lat": -35.5772,
      "lng": -58.0094,
      "precision_m": 8.5,
      "velocidad_ms": 3.2,
      "rumbo_grados": 90,
      "capturada_en": "2026-11-25T17:30:05.123Z",
      "origen": "pwa",
      "visible": false
    }
  ]
}
```

Respuesta: **204** sin cuerpo.

| Campo | Obligatorio | Regla |
| --- | --- | --- |
| `lat` | sí | entre -90 y 90 |
| `lng` | sí | entre -180 y 180 |
| `precision_m` | sí | ≥ 0 (la precisión que informa el celular, en metros) |
| `velocidad_ms` | no | ≥ 0, en m/s |
| `rumbo_grados` | no | de 0 a 360, sin incluir 360 |
| `capturada_en` | sí | hora del celular. No más de 1 minuto en el futuro ni más de 24 h en el pasado. **Con milisegundos** (ver notas) |
| `origen` | sí | `"pwa"` o `"nativa"` |
| `visible` | no | si la página estaba a la vista al capturar. Solo aplica a la PWA |

- Entre 1 y 100 posiciones por lote. Si una sola es inválida, **se rechaza el lote entero** con **400** y un mensaje que dice cuál, por ejemplo `{"error": "posición 3: la latitud tiene que estar entre -90 y 90"}`.
- El cadete sale de la sesión. Un campo que no está en la tabla de arriba (por ejemplo `cadete_id`) da **400** `JSON inválido: campo desconocido "cadete_id"`.
- **Límite**: hasta 10 envíos seguidos por cadete y después 1 cada 2 s (unos 30 por minuto). Si se pasa, responde **429** con `Retry-After` en segundos. El cliente tiene que conservar las posiciones y reintentarlas después, no descartarlas.
- Una posición repetida (mismo cadete y la misma `capturada_en`) se ignora sin error. Así, reintentar un lote que el servidor ya había guardado no duplica nada.

### `GET /api/posiciones/stream` (admin)

Server-Sent Events (`text/event-stream`). Al conectarse llega:

```text
retry: 3000
: conectado

```

Por cada lote guardado llega un evento `posiciones` con las posiciones nuevas (no las repetidas):

```text
event: posiciones
data: [{"id":17,"cadete_id":2,"cadete_nombre":"Juan","lat":-35.59,"lng":-58.02,"precision_m":5,"velocidad_ms":null,"rumbo_grados":null,"capturada_en":"2026-11-25T16:41:02.123-03:00","recibida_en":"2026-11-25T16:41:02.186-03:00","origen":"nativa","visible":null}]

```

Cada 15 s llega `: ping` para que ningún proxy corte la conexión por inactividad. Un `EventSource` del navegador ignora los comentarios y se reconecta solo a los 3 s si se corta. Los eventos no tienen `id` y no se repiten al reconectar: el mapa tiene que volver a pedir `ultimas` (y `recorrido` si lo muestra).

Si un admin no lee lo bastante rápido (más de 64 eventos pendientes), **se le descartan eventos** a él solo. Insertar nunca espera al stream. Lo descartado queda en el log del servidor al desconectarse.

### `GET /api/posiciones/ultimas` (admin)

```json
[
  {"id":6,"cadete_id":2,"cadete_nombre":"Juan","lat":-35.58,"lng":-58.013,"precision_m":18,"velocidad_ms":null,"rumbo_grados":null,"capturada_en":"2026-11-25T16:37:05-03:00","recibida_en":"2026-11-25T16:39:55.271-03:00","origen":"pwa","visible":true}
]
```

Una por cadete que haya mandado alguna vez, la de `capturada_en` más nueva. Si no hay ninguna, devuelve `[]`.

### `GET /api/posiciones/recorrido?cadete=2&desde=2026-11-25T14:00:00-03:00` (admin)

Devuelve un arreglo de posiciones con el mismo formato (sin `cadete_nombre`), ordenadas de la más vieja a la más nueva por `capturada_en`.

**Tope: 10.000 posiciones, contadas desde la más vieja.** Si hay más, faltan las más nuevas: hay que volver a pedir con un `desde` posterior a la última recibida. A 1 posición por segundo alcanza para casi 3 h.

### `GET /api/posiciones/resumen?cadete=2&desde=…&hasta=…` (admin)

Rango `[desde, hasta)` sobre `capturada_en`. `hasta` es opcional (si falta, es ahora). El rango puede ser de hasta 7 días.

```json
{
  "cadete_id": 2,
  "desde": "2026-11-25T19:30:00Z",
  "hasta": "2026-11-25T19:45:00Z",
  "posiciones": 6,
  "primera": "2026-11-25T16:34:54-03:00",
  "ultima": "2026-11-25T16:37:04-03:00",
  "intervalo_medio_s": 26,
  "huecos_60s": {"cantidad": 1, "mayor_s": 90},
  "demora_media_s": 235.59,
  "demora_max_s": 300.75,
  "precision_media_m": 13,
  "no_visibles": 1
}
```

| Campo | Qué es |
| --- | --- |
| `posiciones` | cantidad en el rango |
| `intervalo_medio_s` | promedio entre posiciones consecutivas (`null` con menos de 2) |
| `huecos_60s` | intervalos de más de 60 s: cuántos y el mayor (`mayor_s` es `null` si no hay) |
| `demora_media_s`, `demora_max_s` | `recibida_en - capturada_en`: cuánto tardó en llegar al servidor |
| `precision_media_m` | promedio de `precision_m` |
| `no_visibles` | cuántas tienen `visible = false` |

Sin posiciones en el rango, los promedios salen `null`. Los tiempos van en segundos, redondeados a milisegundos.

### Notas

- **`capturada_en` con milisegundos.** El índice único es por cadete y `capturada_en`. Si el cliente manda la hora sin milisegundos (o redondeada al segundo), dos posiciones del mismo segundo se toman como repetidas y se guarda una sola. Usar `new Date(position.timestamp).toISOString()`, que siempre incluye milisegundos.
- **Demora y reloj del celular.** `capturada_en` es la hora del celular y `recibida_en` la del servidor. Si el reloj del celular está adelantado, la demora puede salir negativa o más chica que la real. Con la hora automática del celular la diferencia suele ser de menos de un segundo.
- **Una sola instancia.** El stream se difunde en memoria: solo ve los lotes que entran por la misma instancia del servidor. Hoy Railway corre una sola.
- Sin limpieza de posiciones viejas (fuera de esta prueba).

## Cómo correr los tests contra Postgres

Los tests de la base (`internal/positions/integration_test.go`) corren solo si existe `TEST_DATABASE_URL`. Si no, se saltean y `go test ./...` igual pasa. Cada corrida crea un esquema descartable, aplica las migraciones y lo borra al terminar.

Se corren con el script, que usa Podman y un contenedor aparte del de desarrollo (`cadetes-test-db`, puerto 5433):

```sh
scripts/test-integracion.sh
```

El script hace esto:

1. Levanta el contenedor.
2. Espera a que Postgres acepte conexiones por TCP (`pg_isready -h 127.0.0.1`), hasta 60 s. Si los tests arrancan antes, fallan con `unexpected EOF`, porque la imagen tarda unos segundos en inicializarse.
3. Corre `go test -race -count=1 ./...` con `TEST_DATABASE_URL`.
4. Para el contenedor al terminar, aunque los tests fallen. Sale con el mismo código que `go test`.

Los argumentos extra pasan a `go test`, por ejemplo: `scripts/test-integracion.sh -run Integration -v`.
