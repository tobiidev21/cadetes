-- name: UpsertPushSubscription :exec
INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (endpoint) DO UPDATE
SET user_id    = EXCLUDED.user_id,
    p256dh     = EXCLUDED.p256dh,
    auth       = EXCLUDED.auth,
    user_agent = EXCLUDED.user_agent;

-- name: ListPushSubscriptionsByUser :many
SELECT * FROM push_subscriptions WHERE user_id = $1 ORDER BY id;

-- name: DeletePushSubscription :exec
DELETE FROM push_subscriptions WHERE id = $1;
