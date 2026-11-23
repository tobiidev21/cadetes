-- Migración base del esqueleto: no crea tablas. Sirve para comprobar que goose
-- corre al arrancar (crea goose_db_version). Las tablas llegan en pasos siguientes.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
