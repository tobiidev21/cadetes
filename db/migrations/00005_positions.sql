-- Posiciones GPS de los cadetes (prueba técnica de rastreo). captured_at es la
-- hora del celular y received_at la del servidor: la diferencia es la demora.
-- El índice es único para que un lote reenviado (reintento del cliente) no
-- duplique posiciones: el insert usa ON CONFLICT DO NOTHING.

-- +goose Up
CREATE TABLE positions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    courier_id  BIGINT           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    lat         DOUBLE PRECISION NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lng         DOUBLE PRECISION NOT NULL CHECK (lng BETWEEN -180 AND 180),
    accuracy_m  REAL             NOT NULL CHECK (accuracy_m >= 0),
    speed_ms    REAL             CHECK (speed_ms >= 0),
    heading_deg REAL             CHECK (heading_deg >= 0 AND heading_deg < 360),
    captured_at TIMESTAMPTZ      NOT NULL,
    received_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    source      TEXT             NOT NULL CHECK (source IN ('pwa', 'nativa')),
    visible     BOOLEAN
);

CREATE UNIQUE INDEX positions_courier_captured_idx ON positions (courier_id, captured_at DESC);

-- +goose Down
DROP TABLE positions;
