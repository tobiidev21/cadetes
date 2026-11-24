-- Pedidos. La plata va en centavos (entero). Un pedido sin cadete está
-- pendiente; con cadete, asignado o entregado.

-- +goose Up
CREATE TABLE orders (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pickup_address  TEXT        NOT NULL CHECK (btrim(pickup_address) <> ''),
    dropoff_address TEXT        NOT NULL CHECK (btrim(dropoff_address) <> ''),
    price_cents     BIGINT      NOT NULL CHECK (price_cents > 0),
    status          TEXT        NOT NULL DEFAULT 'pendiente' CHECK (status IN ('pendiente', 'asignado', 'entregado')),
    courier_id      BIGINT      REFERENCES users (id),
    created_by      BIGINT      NOT NULL REFERENCES users (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'pendiente') = (courier_id IS NULL))
);

CREATE INDEX orders_courier_id_idx ON orders (courier_id, created_at DESC);
CREATE INDEX orders_created_at_idx ON orders (created_at DESC);

-- +goose Down
DROP TABLE orders;
