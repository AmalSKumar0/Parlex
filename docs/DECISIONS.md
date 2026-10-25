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
