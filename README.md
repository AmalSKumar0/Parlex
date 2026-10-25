# Parlex 🎙️

[![Go Version](https://img.shields.io/badge/go-1.23%2B-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![CI Status](https://img.shields.io/badge/build-passing-brightgreen.svg)](.github/workflows/ci.yml)
[![OpenAPI](https://img.shields.io/badge/API-OpenAPI%203.0-orange.svg)](api/openapi.yaml)
[![WebSocket Protocol](https://img.shields.io/badge/WS-translator.v1-purple.svg)](docs/PROTOCOL.md)

**Parlex** is a production-grade, low-latency Go backend designed for ESP32-S3 push-to-talk speech translation devices and companion mobile applications.

It bridges hardware devices to cloud speech-to-speech translation engines (such as OpenAI's `gpt-realtime-translate` and fallback STT $\rightarrow$ MT $\rightarrow$ TTS pipelines) over resilient WebSockets with deterministic binary audio framing, strict actor-based concurrency, zero data retention, and enterprise observability.

---

## 🌟 Highlights & Architecture

```
ESP32-S3 Device ───WSS───► Reverse Proxy (Caddy TLS) ───► Go Server (Parlex)
Phone Companion ──HTTPS──► Reverse Proxy (Caddy TLS) ───► Go Server (Parlex)

Go Server Architecture:
  ┌──────────────────────────┐      ┌─────────────────────────────┐
  │  REST API (go-chi/chi)   │      │ Device Gateway (coder/ws)   │
  │  Device, Settings, Auth  │      │ /v1/stream (translator.v1)  │
  └─────────────┬────────────┘      └──────────────┬──────────────┘
                │                                  │
                ▼                                  ▼
      ┌──────────────────────────────────────────────────────────┐
      │  Device Actor (Single-goroutine state machine per conn)  │
      └────────────────────────────┬─────────────────────────────┘
                                   │
                   ┌───────────────┴───────────────┐
                   ▼                               ▼
       ┌───────────────────────┐       ┌─────────────────────────┐
       │   Provider Router     │       │ PostgreSQL 16 (pgx/v5)  │
       │   & Circuit Breaker   │       │ Devices, Settings, Usage│
       └───────────┬───────────┘       └─────────────────────────┘
                   │
         ┌─────────┼────────────────────────┐
         ▼         ▼                        ▼
  ┌────────────┐ ┌──────────────────────┐ ┌───────────────────────┐
  │  Realtime  │ │  Pipeline Fallback   │ │     Mock Adapter      │
  │ (OpenAI WS)│ │ (STT > MT > TTS: ml) │ │(Simulator/Contract UI)│
  └────────────┘ └──────────────────────┘ └───────────────────────┘
```

- **Low-Latency Push-to-Talk Turn State Machine**: Enforces sequential turns (`IDLE` $\rightarrow$ `STARTING` $\rightarrow$ `LISTENING` $\rightarrow$ `DRAINING` $\rightarrow$ `PLAYING` $\rightarrow$ `IDLE`) with configurable hard caps (`MAX_TURN_SECONDS`, `DRAIN_TIMEOUT_SECONDS`, `PLAYBACK_ACK_TIMEOUT_SECONDS`).
- **Provider-Agnostic Core**: Connects to OpenAI Realtime translation via WebSocket, composite fallback pipelines (e.g. for Malayalam `ml`), or deterministic mock adapters.
- **Circuit Breaker & Session Limiter**: Built-in 5 failures in 30s $\rightarrow$ 20s cooldown circuit breaker, plus global concurrency protection (`MAX_PROVIDER_SESSIONS`, default 200).
- **Single-Goroutine Actor Model**: Each connection runs 3 goroutines (Reader, Actor, Writer). The Actor uniquely mutates state with non-blocking 2s timeout bounds to protect memory against slow consumers.
- **Hardware-Optimized Audio Pipeline**: In-memory linear interpolation resampler converting 16 kHz PCM16 (hardware) to/from 24 kHz PCM16 (cloud provider) with zero allocations on hot paths.
- **Zero Audio & Transcript Retention**: Audio and transcripts are strictly processed in-memory and discarded upon turn completion. Only aggregated durations and metadata are retained in PostgreSQL for 90 days.
- **Device Security & Rotation**: 32-byte SHA-256 device secrets, Ed25519 JWT stream tokens (15-min expiry), single-use claim codes with 15-minute lockout, and instant revocation terminating live sockets within 5 seconds.

---

## 📁 Repository Layout

```
├── api/
│   └── openapi.yaml           # Complete REST OpenAPI 3.0 specification
├── cmd/
│   ├── server/main.go         # Core daemon wiring & graceful shutdown
│   └── simulator/             # Fake device simulator & load testing client
├── config/
│   └── languages.yaml         # Language catalog and provider routing rules
├── deploy/
│   ├── Dockerfile             # Multi-stage distroless static container
│   ├── docker-compose.yml     # Server, PostgreSQL 16, and Caddy services
│   ├── Caddyfile              # Auto-TLS reverse proxy with WebSocket rules
│   └── dashboards/            # Production Grafana dashboard JSON
├── docs/
│   ├── PROTOCOL.md            # Detailed WebSocket wire protocol & framing
│   ├── DECISIONS.md           # Architecture, cloud provider specs & benchmarks
│   └── SECURITY.md            # Threat model, controls & security review
├── internal/
│   ├── audio/                 # 16kHz <-> 24kHz resamplers & frame chunkers
│   ├── auth/                  # Ed25519 tokens, secrets, claim & revocation
│   ├── catalog/               # Dynamic language catalog & resolution
│   ├── config/                # Environment variable parsing & validation
│   ├── gateway/               # WebSocket upgrade, codec, actor, state machine
│   ├── httpapi/               # Chi router, RFC 9457 problem+json, handlers
│   ├── observability/         # Structured slog, Prometheus metrics & pprof
│   ├── provider/              # Provider interface, circuit breaker & router
│   │   ├── realtime/          # OpenAI gpt-realtime-translate WebSocket adapter
│   │   ├── pipeline/          # Fallback STT -> MT -> TTS pipeline adapter
│   │   └── mock/              # Deterministic test & simulation adapter
│   ├── store/                 # PostgreSQL migrations & sqlc queries
│   └── usage/                 # Token bucket limiters & daily audio quotas
├── migrations/                # Versioned SQL migrations (golang-migrate)
└── Makefile                   # Build, test, lint, run, sim, docker targets
```

---

## 🚀 Quickstart

### 1. Prerequisites
- **Go 1.23+**
- **Docker & Docker Compose** (or local PostgreSQL 16)
- **Make**

### 2. Configure Environment
Copy the template configuration:
```bash
cp .env.example .env
```
Generate an Ed25519 private key for signing device tokens:
```bash
openssl genpkey -algorithm ed25519 -out config/jwt_ed25519_key.pem
```

Update `.env` with your settings:
```env
DATABASE_URL=postgres://translator:translator_secret@localhost:5432/translator?sslmode=disable
JWT_SIGNING_KEY_FILE=config/jwt_ed25519_key.pem
ADMIN_API_KEY=your-admin-secret-key
PROVIDER_DEFAULT=realtime
PROVIDER_REALTIME_API_KEY=sk-... # or leave empty and use PROVIDER_DEFAULT=mock
```

### 3. Launch via Docker Compose
To run the full stack (Go server, PostgreSQL 16, Caddy with TLS):
```bash
cd deploy
docker compose up -d
```

### 4. Or Run Locally with Make
```bash
# Run database migrations
make migrate

# Build binaries
make build

# Start the server (:8080 public, :9090 internal metrics/pprof)
make run
```

---

## 🧪 Device Simulator & Testing

The included simulator ([`cmd/simulator`](cmd/simulator/main.go)) functions as a virtual ESP32-S3 device that authenticates, establishes the WebSocket connection, streams 20 ms PCM audio frames in real time, collects translated audio, and measures latency percentiles.

### Run End-to-End Turns
```bash
# Run 10 consecutive turns using simulated speech
go run ./cmd/simulator --server http://localhost:8080 --loops 10 --button a

# Or stream an input 16kHz 16-bit mono WAV file
go run ./cmd/simulator --server http://localhost:8080 --wav sample.wav --loops 1
```

### Run Full Test Suite with Race Detector
```bash
make test
```
Runs unit, integration, and contract suites under `-race` with `goleak` goroutine leak detection.

### Run Linting
```bash
make lint
```

---

## 📡 Protocols & APIs

### 1. Device WebSocket Stream (`/v1/stream`)
- **Handshake**: Requires `Authorization: Bearer <stream_token>` and subprotocol `translator.v1`.
- **Binary Audio Framing**: Big-endian 8-byte header followed by PCM16 little-endian audio payload:
  ```
   0                   1                   2                   3
   0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |     Type      |     Flags     |            Turn ID            |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                        Sequence Number                        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                         Audio Payload ...                     |
  ```
  - `Type`: `1` = Uplink, `2` = Downlink.
  - `Flags`: Bit 0 = `LAST_FRAME` (`0x01`).
- **Text Control Messages**: JSON messages tagged by discriminator `t` (`hello`, `ready`, `turn.start`, `turn.accepted`, `turn.stop`, `turn.cancel`, `text`, `audio.start`, `audio.end`, `playback.done`, `error`, `bye`).
- Read the complete protocol specification in [docs/PROTOCOL.md](docs/PROTOCOL.md).

### 2. REST API (`/v1`)
Full specification available in [api/openapi.yaml](api/openapi.yaml).

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/health/live` | None | Process liveness probe |
| `GET` | `/health/ready` | None | Database and provider readiness probe |
| `GET` | `/v1/languages` | None | Language catalog and supported directions |
| `POST` | `/v1/admin/devices` | Admin Key | Provision new device credentials |
| `POST` | `/v1/admin/devices/{id}/revoke` | Admin Key | Instantly revoke device access |
| `POST` | `/v1/admin/firmware` | Admin Key | Register OTA firmware release |
| `POST` | `/v1/devices/claim` | None (Rate Limited) | Claim device using single-use code |
| `POST` | `/v1/auth/device-token` | Device Secret | Exchange device secret for 15-min stream token |
| `GET` | `/v1/devices/{id}/settings` | Owner / Device | Fetch settings snapshot with `ETag` |
| `PATCH` | `/v1/devices/{id}/settings` | Owner | Update settings with `If-Match` optimistic lock |
| `GET` | `/v1/devices/{id}/status` | Owner | Check online status, firmware, and turn state |
| `GET` | `/v1/devices/{id}/usage` | Owner | Query daily minutes usage history |
| `GET` | `/v1/firmware/latest` | Device | Check for OTA firmware updates (204 if current) |
| `WS` | `/v1/stream` | Stream Token | Realtime audio & control channel |

---

## 📊 Observability & Metrics

Prometheus metrics and Go `pprof` profilers are exposed on the private listener (`127.0.0.1:9090`):

- **Prometheus Metrics**: `GET http://127.0.0.1:9090/metrics`
  - `translator_ws_connections`: Active WebSocket connections gauge.
  - `translator_turns_total{direction,provider,status}`: Total turns counter.
  - `translator_first_audio_seconds`: Latency histogram from `turn.start` to first downlink audio byte.
  - `translator_turn_total_seconds`: Latency histogram from `turn.start` to `playback.done`.
  - `translator_provider_open_seconds`: Latency to open provider session.
  - `translator_audio_in_seconds_total`, `translator_audio_out_seconds_total`: Audio throughput.
  - `translator_breaker_state{provider}`: Circuit breaker state (0 = Closed, 1 = Open, 2 = Half-Open).
  - `translator_queue_overflow_total{queue}`, `translator_protocol_violations_total{code}`.
- **Grafana Dashboard**: Import ready-to-use dashboard from [`deploy/dashboards/translator.json`](deploy/dashboards/translator.json).

---

## ⚙️ Configuration Reference

| Environment Variable | Default | Description |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Public HTTP & WebSocket listener |
| `INTERNAL_ADDR` | `127.0.0.1:9090` | Internal `/metrics` & pprof listener |
| `DATABASE_URL` | *Required* | PostgreSQL 16 DSN connection string |
| `JWT_SIGNING_KEY_FILE` | *Required* | Path to Ed25519 PEM private key |
| `JWT_ROTATION_KEY_FILE` | *Optional* | Path to secondary Ed25519 PEM key for rotation |
| `ADMIN_API_KEY` | *Required* | Secret key protecting `/v1/admin/*` endpoints |
| `PROVIDER_DEFAULT` | `realtime` | Default translation adapter (`realtime`, `pipeline`, `mock`) |
| `PROVIDER_REALTIME_API_KEY` | *Required if realtime* | OpenAI or Azure API credential |
| `PROVIDER_REALTIME_BASE_URL` | Provider Default | Base WebSocket URL (`wss://api.openai.com/v1/...`) |
| `PROVIDER_AUTH_MODE` | `bearer` | Header authentication mode (`bearer` or `azure-api-key`) |
| `MAX_PROVIDER_SESSIONS` | `200` | Global concurrency cap on active provider sessions |
| `DEVICE_DAILY_MINUTES_LIMIT` | `60` | Per-device daily limit of translated audio minutes |
| `MAX_TURN_SECONDS` | `60` | Maximum speaking duration per turn before forced stop |
| `DRAIN_TIMEOUT_SECONDS` | `10` | Maximum wait time for trailing provider audio after turn stop |
| `PLAYBACK_ACK_TIMEOUT_SECONDS`| `30` | Maximum wait time for device `playback.done` acknowledgement |
| `WS_PING_INTERVAL_SECONDS` | `15` | Interval between server WebSocket ping frames |
| `WS_PONG_TIMEOUT_SECONDS` | `40` | Timeout to disconnect socket if no pong received |
| `PREWARM_TTL_SECONDS` | `0` | Seconds to retain warm provider session across turns |
| `CORS_ALLOWED_ORIGINS` | Empty | Comma-separated list of permitted CORS origins |
| `LOG_LEVEL` | `info` | Structured logging level (`debug`, `info`, `warn`, `error`) |

---

## 🛠️ Makefile Commands

```bash
make build    # Build bin/server and bin/simulator binaries
make test     # Run all unit, integration, and contract tests with -race
make lint     # Run golangci-lint across all packages
make migrate  # Apply PostgreSQL database migrations
make sqlc     # Re-generate type-safe store queries from queries.sql
make sim      # Run fake device simulator against running server
make docker   # Build production distroless Docker image
```

---

## 📜 License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
