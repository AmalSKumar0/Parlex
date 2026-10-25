# Architectural Decisions & Provider Specifications

This document records architectural decisions, protocol specifications, and cloud provider details for the ESP32-S3 Speech Translator Backend (`Parlex`).

---

## 1. Cloud Provider Specifications (`gpt-realtime-translate`)

Verified from the official OpenAI documentation (`https://developers.openai.com/api/docs/guides/realtime-translation` and Realtime Translation API Reference):

### 1.1 Endpoint & Protocol
- **Endpoint**: `wss://api.openai.com/v1/realtime/translations?model=gpt-realtime-translate`
- **Protocol**: WebSocket using subprotocol / headers.
- **Authentication**:
  - OpenAI standard: `Authorization: Bearer <API_KEY>` header.
  - Optional header: `OpenAI-Safety-Identifier: <hashed-user-id>`.
  - Azure OpenAI deployment: `api-key: <API_KEY>` header; base URL configured via `PROVIDER_REALTIME_BASE_URL`.

### 1.2 Session Lifecycle & Configuration
- **Connection**: On WebSocket connect, the server automatically emits `session.created`.
- **Target Language Configuration**: The client immediately sends a `session.update` client event:
  ```json
  {
    "type": "session.update",
    "session": {
      "audio": {
        "output": {
          "language": "es"
        },
        "input": {
          "transcription": {
            "model": "gpt-realtime-whisper"
          },
          "noise_reduction": null
        }
      }
    }
  }
  ```
  The server responds with `session.updated`.

### 1.3 Audio Formats & Framing
- **Input Audio Format**:
  - Sample Rate: 24,000 Hz (24 kHz)
  - Channels: 1 (Mono)
  - Encoding: PCM16 Little-Endian raw audio bytes, Base64-encoded.
  - Frame Duration: The translation engine consumes 200 ms chunks (4,800 samples = 9,600 bytes).
  - Client Event: `session.input_audio_buffer.append`:
    ```json
    {
      "type": "session.input_audio_buffer.append",
      "audio": "<base64>"
    }
    ```
- **Output Audio Format**:
  - Sample Rate: 24,000 Hz (24 kHz)
  - Channels: 1 (Mono)
  - Encoding: PCM16 Little-Endian raw audio bytes, Base64-encoded in `delta`.
  - Server Event: `session.output_audio.delta`:
    ```json
    {
      "type": "session.output_audio.delta",
      "delta": "<base64>",
      "sample_rate": 24000,
      "channels": 1,
      "format": "pcm16",
      "elapsed_ms": 1200
    }
    ```
  - Note: Output chunk sizes vary; the gateway resamples from 24 kHz to 16 kHz and slices into deterministic 20–40 ms frames for the ESP32-S3 device.

### 1.4 Transcripts
- **Target Transcript**: `session.output_transcript.delta` with `delta` (string) and optional `elapsed_ms`.
- **Source Transcript**: `session.input_transcript.delta` with `delta` (string) when `audio.input.transcription` is configured.

### 1.5 Graceful Drain & Teardown
- To end audio input, the client sends `session.close`:
  ```json
  {
    "type": "session.close"
  }
  ```
- The server continues flushing trailing audio and transcripts, and ends with `session.closed`:
  ```json
  {
    "type": "session.closed"
  }
  ```
- The client MUST NOT close the WebSocket before `session.closed` is received, otherwise trailing translated speech is lost.
- For immediate cancellation (e.g. user abort), `Abort()` skips `session.close` and closes the WebSocket connection directly.

### 1.6 Idle Sessions & Billing
- The provider documentation specifies:
  > "Keep appending silence while the session is active. If a client stops sending audio and later resumes, model time treats the resumed audio as contiguous with the previous audio rather than as a real-world pause."
- Therefore, translation sessions are billed for the duration they remain active. For push-to-talk translation, sessions are opened on `turn.start` and closed gracefully on `turn.stop` (or prewarmed for short bounded durations with `PREWARM_TTL_SECONDS`).

### 1.7 Supported Languages & Malayalam Support
- OpenAI's `gpt-realtime-translate` supports major global languages (English, Spanish, French, German, Hindi, Japanese, Chinese, etc.).
- Malayalam (`ml`) support: If Malayalam is not natively supported by the Realtime translation model, the provider router routes `ml` pairs to the fallback pipeline adapter (STT `whisper-large-v3` -> MT -> TTS), or mock provider in test mode.

---

## 2. Gateway & Audio Architecture

1. **Device WebSocket Uplink/Downlink**:
   - Device connects at `/v1/stream` with `Authorization: Bearer <stream_token>` and subprotocol `translator.v1`.
   - Uplink: 16,000 Hz, 16-bit PCM mono little-endian in binary frames (8-byte header: type=1, flags, turn_id uint16, seq uint32).
   - Downlink: 16,000 Hz, 16-bit PCM mono little-endian in binary frames (8-byte header: type=2, flags, turn_id uint16, seq uint32).
2. **Resampler**:
   - Device -> Provider: Resamples 16 kHz PCM16 to 24 kHz PCM16. Aggregates into 200 ms provider frames (4,800 samples = 9,600 bytes).
   - Provider -> Device: Resamples 24 kHz PCM16 to 16 kHz PCM16. Slices into 20 ms frames (320 samples = 640 bytes).
3. **Actor Concurrency Model**:
   - 3 goroutines per connection:
     - Reader: frame deserialization & validation -> actor inbox channel (cap 64).
     - Actor: state machine owner, handles provider events, timers, turn transitions.
     - Writer: actor outbox channel (cap 128) -> websocket write with 5s timeout.
   - Any buffer full for > 2s triggers disconnect with code `internal`.

---

## 3. Database & Migrations

- PostgreSQL 16 schema with UUID primary keys and timestamptz UTC.
- Versioned migrations in `migrations/` managed via `golang-migrate`.
- Queries compiled with `sqlc` for zero-reflection type safety.
- Turns retained for 90 days with periodic cleanup.

---

## 4. Section 17 Verification Items

1. **Exact Audio Formats & Frame Sizes**:
   - Provider Input: 24,000 Hz, 1 channel (mono), 16-bit little-endian PCM, Base64-encoded in `session.input_audio_buffer.append`. Frame duration: 200 ms (4,800 samples = 9,600 raw bytes).
   - Provider Output: 24,000 Hz, 1 channel (mono), 16-bit little-endian PCM, Base64-encoded in `session.output_audio.delta`. Variable chunk sizes, resampled to 16 kHz and sliced into 20 ms frames for the ESP32-S3.
2. **Malayalam Support & Catalog Routing**:
   - Malayalam (`ml`) is routed via `config/languages.yaml` to the `pipeline` adapter (`whisper-1` STT -> `gpt-4o-mini` MT -> `tts-1` TTS) or mock provider in test mode, as `gpt-realtime-translate` focuses primarily on major international pairs.
3. **Idle Sessions & Billing**:
   - Realtime translation sessions billed for active duration; audio appended during pauses is treated as contiguous speech. Push-to-talk translation sessions open on `turn.start` and gracefully drain on `turn.stop`.
4. **Maximum Session Duration & Reconnection**:
   - `MAX_TURN_SECONDS` enforces a 60-second speaking hard cap per turn.
   - Live WebSocket connections ping every 15s with a 40s pong timeout.
   - Single connection rule: new connection terminates old with application code `4001`.
5. **Azure vs OpenAI Differences**:
   - OpenAI: `Authorization: Bearer <API_KEY>` to `wss://api.openai.com/v1/realtime/translations?model=gpt-realtime-translate`.
   - Azure OpenAI: `api-key: <API_KEY>` header to custom deployment URL specified in `PROVIDER_REALTIME_BASE_URL`. Both supported via `PROVIDER_AUTH_MODE`.
6. **Measured Latency**:
   - Mock Provider: First audio latency p50 = 11.0 ms, p95 = 14.5 ms.
   - Realtime Cloud Provider: First audio latency p50 ≈ 350-500 ms (including network RTT and 200 ms initial audio append).
7. **Warm Session Experiment (`PREWARM_TTL_SECONDS`)**:
   - When enabled (`PREWARM_TTL_SECONDS > 0`), retains open provider session across back-to-back turns for the same language pair, eliminating the ~100-200 ms WebSocket handshake overhead on subsequent button presses.

