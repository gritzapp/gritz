-- name: GetEventCursor :one
SELECT cursor FROM replication WHERE id = 1;

-- name: SetEventCursor :exec
INSERT INTO replication (id, cursor) VALUES (1, ?)
ON CONFLICT (id) DO UPDATE SET cursor = excluded.cursor;
