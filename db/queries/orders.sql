-- name: CreateAssignedOrder :one
-- Crea el pedido ya asignado. Si courier_id no es un cadete activo no inserta
-- nada (pgx.ErrNoRows): la validación y el alta van en la misma sentencia.
INSERT INTO orders (pickup_address, dropoff_address, price_cents, status, courier_id, created_by)
SELECT sqlc.arg(pickup_address)::text,
       sqlc.arg(dropoff_address)::text,
       sqlc.arg(price_cents)::bigint,
       'asignado',
       u.id,
       sqlc.arg(created_by)::bigint
FROM users u
WHERE u.id = sqlc.arg(courier_id)::bigint AND u.role = 'cadete' AND u.active
RETURNING *;

-- name: ListOrders :many
SELECT o.id, o.pickup_address, o.dropoff_address, o.price_cents, o.status,
       o.courier_id, c.name AS courier_name, o.created_at
FROM orders o
LEFT JOIN users c ON c.id = o.courier_id
ORDER BY o.created_at DESC, o.id DESC;

-- name: ListOrdersByCourier :many
SELECT o.id, o.pickup_address, o.dropoff_address, o.price_cents, o.status,
       o.courier_id, c.name AS courier_name, o.created_at
FROM orders o
LEFT JOIN users c ON c.id = o.courier_id
WHERE o.courier_id = sqlc.arg(courier_id)::bigint
ORDER BY o.created_at DESC, o.id DESC;

-- name: GetOrder :one
SELECT o.id, o.pickup_address, o.dropoff_address, o.price_cents, o.status,
       o.courier_id, c.name AS courier_name, o.created_at
FROM orders o
LEFT JOIN users c ON c.id = o.courier_id
WHERE o.id = sqlc.arg(id)::bigint;

-- name: GetOrderForCourier :one
SELECT o.id, o.pickup_address, o.dropoff_address, o.price_cents, o.status,
       o.courier_id, c.name AS courier_name, o.created_at
FROM orders o
LEFT JOIN users c ON c.id = o.courier_id
WHERE o.id = sqlc.arg(id)::bigint AND o.courier_id = sqlc.arg(courier_id)::bigint;
