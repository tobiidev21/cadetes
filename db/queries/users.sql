-- name: CountAdmins :one
SELECT count(*) FROM users WHERE role = 'admin';

-- name: CreateUser :one
INSERT INTO users (username, password_hash, role, name)
VALUES ($1, $2, $3, $4)
RETURNING id, username, role, name, active, created_at;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: ListUsers :many
SELECT id, username, role, name, active, created_at
FROM users
ORDER BY role, name, id;

-- name: ListActiveCouriers :many
SELECT id, username, name
FROM users
WHERE role = 'cadete' AND active
ORDER BY name, id;
