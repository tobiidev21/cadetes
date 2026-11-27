#!/usr/bin/env bash
# Corre todos los tests, incluidos los de integración contra Postgres.
#
# Levanta un Postgres 18 descartable con Podman (aparte del de desarrollo),
# espera a que acepte conexiones, corre go test con TEST_DATABASE_URL y para
# el contenedor al terminar, aunque los tests fallen. Los argumentos extra se
# pasan a go test, p. ej.: scripts/test-integracion.sh -run Integration -v
set -euo pipefail

cd "$(dirname "$0")/.."

NAME=cadetes-test-db
PORT=5433

podman run -d --rm --name "$NAME" -e POSTGRES_PASSWORD=test -p "$PORT:5432" docker.io/library/postgres:18 >/dev/null
# Recién acá: si el run falla (p. ej. el contenedor ya existe), no hay que
# parar uno que no levantó este script.
trap 'podman stop "$NAME" >/dev/null' EXIT

# Por TCP y no por el socket: durante la inicialización, la imagen levanta un
# Postgres temporal solo en el socket, y pg_isready daría listo antes de tiempo.
echo "Esperando a Postgres en $NAME..."
tries=0
until podman exec "$NAME" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1; do
	tries=$((tries + 1))
	if [ "$tries" -ge 60 ]; then
		echo "Postgres no arrancó en 60 s" >&2
		exit 1
	fi
	sleep 1
done

TEST_DATABASE_URL="postgres://postgres:test@localhost:$PORT/postgres?sslmode=disable" \
	go test -race -count=1 ./... "$@"
