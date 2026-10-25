# Security Architecture & Review Checklist

Version: 1.0  
Status: Production-Grade

This document outlines the security controls, threat mitigations, and compliance checklist implemented in `Parlex`.

---

## 1. Security Architecture & Threat Model

```
ESP32-S3 Device (TLS client)
         │
         ▼  (WSS / HTTPS via Caddy reverse proxy)
[ Security Boundary: Caddy Reverse Proxy ]
  - Enforces TLS 1.3 / HSTS
  - Strips untrusted client headers
  - Proxies to Go backend
         │
         ▼  (Cleartext over loopback / private network)
[ Go Backend (Parlex) ]
  - Holds cloud provider credentials (OpenAI / Azure)
  - Enforces per-device tokens (EdDSA JWT)
  - Enforces per-device rate limits & quotas
  - Manages connection lifecycle
         │
         ▼  (WSS with Provider API Key)
[ Cloud Provider (OpenAI / Azure Realtime) ]
```

### Key Principles:
1. **Server as Sole Security Boundary**: Devices NEVER see or store cloud provider API keys.
2. **Zero Audio / Transcript Retention**: Audio frames and transcription text are strictly processed in-memory and discarded upon turn completion. Neither raw audio nor transcripts are ever written to database tables or persistent logs.
3. **Defense in Depth**: Every layer validates inputs, rate-limits abuse, and enforces strict bounded memory.

---

## 2. Authentication & Credential Management

- **Device Secrets**:
  - Generated as 32 cryptographically secure random bytes (`crypto/rand`).
  - Shown exactly once to the administrator upon device provisioning.
  - Stored in PostgreSQL solely as SHA-256 hashes (`bytea`).
  - Verification uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.
- **Claim Codes**:
  - 10-character alphanumeric codes using unambiguous characters (`23456789ABCDEFGHJKLMNPQRSTUVWXYZ`), avoiding confusing characters (`0`, `O`, `1`, `I`).
  - Single-use: immediately cleared to `NULL` upon successful claim.
  - Protected against brute force: max 5 claim attempts per minute; 10 failed attempts trigger a 15-minute IP lockout.
- **Tokens**:
  - Ed25519 EdDSA-signed JWTs (`golang-jwt/jwt/v5`).
  - Stream tokens: strictly 15-minute expiry, role `device`.
  - Owner tokens: 30-day expiry, role `owner`.
  - Key rotation: supports primary and rotation keys via `kid` header claim.
- **Admin API**:
  - Protected by `X-Admin-Key` header with constant-time comparison.
  - Rate limited to 60 requests per minute.

---

## 3. Revocation & Connection Lifecycle

- When an admin calls `POST /v1/admin/devices/{id}/revoke`:
  - `devices.revoked_at` timestamp is set in PostgreSQL.
  - In-memory revocation registry updates immediately.
  - Any live WebSocket stream for that device is immediately pushed a `bye` control message with `reason: "device_revoked"` and terminated within 5 seconds.
  - Subsequent token issuance or WebSocket handshakes are rejected with HTTP 401.

---

## 4. Rate Limiting & Denial of Service Protection

| Surface | Limit | Action on Exceeded |
|---|---|---|
| Device Token Exchange | 10 per minute per `device_id` | HTTP 429 `rate_limited` |
| Device Claim | 5 per minute per IP | HTTP 429 `rate_limited` |
| Claim Failures | 10 failures | 15-minute lockout |
| Settings Writes (PATCH) | 30 per minute per `device_id` | HTTP 429 `rate_limited` |
| Admin Endpoints | 60 per minute per IP | HTTP 429 `rate_limited` |
| Daily Speaking Audio | `DEVICE_DAILY_MINUTES_LIMIT` (60 min) | `quota_exceeded` error |
| Turn Duration | `MAX_TURN_SECONDS` (60s) | Forced stop & drain |
| Actor In/Outbox Buffers | 2-second backpressure limit | Terminate connection (`inbox_overflow`) |
| Concurrent Provider Sessions | `MAX_PROVIDER_SESSIONS` (200) | `provider_unavailable` |

---

## 5. Memory Safety & Resource Bounds

- **Goroutine Ownership**: Every goroutine has an owner and cancellation path tied to `context.Context`. Verified using Uber's `goleak` in gateway tests.
- **Zero Memory Leaks on Slow Consumers**: Outbox buffer is capped at 128 items with a 2s write backpressure timeout; slow clients are disconnected rather than allowing unbounded heap growth.
- **Bounded Audio Pipelines**: Audio resampling utilizes preallocated buffers and bounded chunkers to avoid garbage collection spikes.

---

## 6. Audit Logging & Compliance

- Admin actions (`device.create`, `device.revoke`, `firmware.register`) and claim events are recorded to the immutable `audit_log` PostgreSQL table.
- Turn metrics (`turns` table) are automatically purged after 90 days via scheduled database maintenance.
- Container executes as non-root user (`USER nonroot:nonroot`) with read-only root filesystem.
