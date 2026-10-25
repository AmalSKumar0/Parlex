-- name: CreateDevice :one
INSERT INTO devices (name, secret_hash, claim_code)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetDeviceByID :one
SELECT * FROM devices
WHERE id = $1;

-- name: GetDeviceByClaimCode :one
SELECT * FROM devices
WHERE claim_code = $1 AND claimed_at IS NULL AND revoked_at IS NULL;

-- name: ClaimDevice :one
UPDATE devices
SET claim_code = NULL, claimed_at = now()
WHERE id = $1 AND claimed_at IS NULL AND revoked_at IS NULL
RETURNING *;

-- name: RevokeDevice :one
UPDATE devices
SET revoked_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateDeviceLastSeen :exec
UPDATE devices
SET last_seen_at = now(),
    firmware = CASE WHEN $2 != '' THEN $2 ELSE firmware END
WHERE id = $1;

-- name: ListRevokedDeviceIDs :many
SELECT id FROM devices
WHERE revoked_at IS NOT NULL;

-- name: CreateDeviceSettings :one
INSERT INTO device_settings (
  device_id, version, button_a_src, button_a_dst, button_b_src, button_b_dst, volume, captions, led_level
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: GetDeviceSettings :one
SELECT * FROM device_settings
WHERE device_id = $1;

-- name: UpdateDeviceSettings :one
UPDATE device_settings
SET version = version + 1,
    button_a_src = $2,
    button_a_dst = $3,
    button_b_src = $4,
    button_b_dst = $5,
    volume = $6,
    captions = $7,
    led_level = $8,
    updated_at = now()
WHERE device_id = $1 AND version = $9
RETURNING *;

-- name: CreateTurn :one
INSERT INTO turns (
  id, device_id, turn_no, src_lang, dst_lang, provider, status,
  audio_in_ms, audio_out_ms, first_audio_ms, total_ms, error_code, started_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
RETURNING *;

-- name: GetDeviceDailyUsageMinutes :one
SELECT COALESCE(SUM(audio_in_ms + audio_out_ms), 0)::float8 / 60000.0 AS minutes_used
FROM turns
WHERE device_id = $1
  AND started_at >= $2
  AND started_at < $3
  AND status != 'failed';

-- name: GetUsageByDateRange :many
SELECT date_trunc('day', started_at)::date AS day,
       COALESCE(SUM(audio_in_ms + audio_out_ms), 0)::float8 / 60000.0 AS minutes_used
FROM turns
WHERE device_id = $1
  AND started_at >= $2
  AND started_at <= $3
GROUP BY 1
ORDER BY 1 ASC;

-- name: CleanupOldTurns :exec
DELETE FROM turns
WHERE started_at < now() - INTERVAL '90 days';

-- name: CreateFirmwareRelease :one
INSERT INTO firmware_releases (version, url, sha256, min_version)
VALUES ($1, $2, $3, $4)
ON CONFLICT (version) DO UPDATE
SET url = EXCLUDED.url, sha256 = EXCLUDED.sha256, min_version = EXCLUDED.min_version
RETURNING *;

-- name: GetLatestFirmwareRelease :one
SELECT * FROM firmware_releases
ORDER BY created_at DESC
LIMIT 1;

-- name: CreateAuditLog :one
INSERT INTO audit_log (actor, action, device_id, detail)
VALUES ($1, $2, $3, $4)
RETURNING *;
