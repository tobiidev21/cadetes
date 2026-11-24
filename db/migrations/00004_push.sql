-- Suscripciones push: un usuario puede tener varios dispositivos. El endpoint
-- identifica al dispositivo, así que es único: si otro usuario se suscribe
-- desde el mismo navegador, la fila pasa a ser suya.

-- +goose Up
CREATE TABLE push_subscriptions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint   TEXT        NOT NULL UNIQUE,
    p256dh     TEXT        NOT NULL,
    auth       TEXT        NOT NULL,
    user_agent TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX push_subscriptions_user_id_idx ON push_subscriptions (user_id);

-- +goose Down
DROP TABLE push_subscriptions;
