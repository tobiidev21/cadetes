-- name: InsertPositions :many
-- Inserta un lote en una sola sentencia. El lote viaja como un arreglo JSON
-- (jsonb_to_recordset) porque los arreglos de sqlc no admiten NULL y
-- velocidad, rumbo y visible son opcionales. Las posiciones repetidas (mismo
-- cadete y misma hora de captura) se ignoran: RETURNING trae solo las nuevas.
INSERT INTO positions (courier_id, lat, lng, accuracy_m, speed_ms, heading_deg, captured_at, source, visible)
SELECT sqlc.arg(courier_id)::bigint, p.lat, p.lng, p.accuracy_m, p.speed_ms, p.heading_deg, p.captured_at, p.source, p.visible
FROM jsonb_to_recordset(sqlc.arg(rows)::jsonb) AS p(
    lat         double precision,
    lng         double precision,
    accuracy_m  real,
    speed_ms    real,
    heading_deg real,
    captured_at timestamptz,
    source      text,
    visible     boolean
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: LatestPositions :many
-- Última posición de cada cadete (por hora de captura).
SELECT DISTINCT ON (p.courier_id)
       p.id, p.courier_id, u.name AS courier_name, p.lat, p.lng, p.accuracy_m,
       p.speed_ms, p.heading_deg, p.captured_at, p.received_at, p.source, p.visible
FROM positions p
JOIN users u ON u.id = p.courier_id
ORDER BY p.courier_id, p.captured_at DESC;

-- name: CourierTrack :many
-- Recorrido de un cadete desde una hora, del más viejo al más nuevo. Tope de
-- 10.000 posiciones contadas desde la más vieja.
SELECT * FROM positions
WHERE courier_id = sqlc.arg(courier_id)::bigint AND captured_at >= sqlc.arg(since)::timestamptz
ORDER BY captured_at
LIMIT 10000;

-- name: CourierSummary :one
-- Resumen de un cadete en [since, until): para medir cada prueba. Los tiempos
-- van en segundos. Los intervalos son entre posiciones consecutivas del rango.
-- sqlc no deja devolver estos agregados como nulos, así que salen con COALESCE
-- a 0 (o a since) y el servicio los vuelve null según total y gaps_over_60s.
WITH p AS (
    SELECT captured_at, accuracy_m, visible,
           extract(epoch FROM captured_at - lag(captured_at) OVER (ORDER BY captured_at))::float8 AS gap_s,
           extract(epoch FROM received_at - captured_at)::float8 AS delay_s
    FROM positions
    WHERE courier_id = sqlc.arg(courier_id)::bigint
      AND captured_at >= sqlc.arg(since)::timestamptz
      AND captured_at < sqlc.arg(until)::timestamptz
)
SELECT count(*)::bigint                                                    AS total,
       COALESCE(min(captured_at), sqlc.arg(since)::timestamptz)::timestamptz AS first_at,
       COALESCE(max(captured_at), sqlc.arg(since)::timestamptz)::timestamptz AS last_at,
       COALESCE(avg(gap_s), 0)::float8                                     AS avg_interval_s,
       count(*) FILTER (WHERE gap_s > 60)::bigint                          AS gaps_over_60s,
       COALESCE(max(gap_s) FILTER (WHERE gap_s > 60), 0)::float8           AS longest_gap_s,
       COALESCE(avg(delay_s), 0)::float8                                   AS avg_delay_s,
       COALESCE(max(delay_s), 0)::float8                                   AS max_delay_s,
       COALESCE(avg(accuracy_m::float8), 0)::float8                        AS avg_accuracy_m,
       count(*) FILTER (WHERE visible = false)::bigint                     AS not_visible
FROM p;
