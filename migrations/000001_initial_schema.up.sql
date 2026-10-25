CREATE TABLE IF NOT EXISTS devices (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name          text NOT NULL DEFAULT '',
  secret_hash   bytea NOT NULL,            -- SHA-256 of a 32 byte random secret
  claim_code    text UNIQUE,               -- one time, null after claim
  claimed_at    timestamptz,
  revoked_at    timestamptz,
  firmware      text NOT NULL DEFAULT '',
  last_seen_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS device_settings (
  device_id     uuid PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  version       bigint NOT NULL DEFAULT 1,  -- used for ETag and If-Match
  button_a_src  text NOT NULL DEFAULT 'ml',
  button_a_dst  text NOT NULL DEFAULT 'en',
  button_b_src  text NOT NULL DEFAULT 'en',
  button_b_dst  text NOT NULL DEFAULT 'ml',
  volume        smallint NOT NULL DEFAULT 70 CHECK (volume BETWEEN 0 AND 100),
  captions      boolean NOT NULL DEFAULT false,
  led_level     smallint NOT NULL DEFAULT 50 CHECK (led_level BETWEEN 0 AND 100),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS turns (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id       uuid NOT NULL REFERENCES devices(id),
  turn_no         integer NOT NULL,
  src_lang        text NOT NULL,
  dst_lang        text NOT NULL,
  provider        text NOT NULL,
  status          text NOT NULL,           -- completed, cancelled, failed, quota, timeout
  audio_in_ms     integer NOT NULL DEFAULT 0,
  audio_out_ms    integer NOT NULL DEFAULT 0,
  first_audio_ms  integer,                 -- turn.start received to first downlink byte
  total_ms        integer,
  error_code      text,
  started_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS turns_device_time ON turns (device_id, started_at DESC);

CREATE TABLE IF NOT EXISTS firmware_releases (
  version     text PRIMARY KEY,
  url         text NOT NULL,
  sha256      text NOT NULL,
  min_version text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_log (
  id bigserial PRIMARY KEY,
  at timestamptz NOT NULL DEFAULT now(),
  actor text NOT NULL,
  action text NOT NULL,
  device_id uuid,
  detail jsonb
);
