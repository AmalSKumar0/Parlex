# Device WebSocket Protocol Specification (`/v1/stream`)

Version: 1.0  
Subprotocol: `translator.v1`

---

## 1. Connection & Handshake

- **Endpoint**: `/v1/stream`
- **Handshake Headers**:
  - `Authorization: Bearer <stream_token>`
  - `Sec-WebSocket-Protocol: translator.v1`
- **Authentication**: Stream tokens are Ed25519-signed JWTs valid for 15 minutes, with claim `role: "device"`. Invalid, expired, or non-device tokens are rejected with HTTP 401 Unauthorized. If subprotocol does not match, rejected with HTTP 426 Upgrade Required.
- **Single Connection Rule**: Only one live connection per device is permitted. A new connection immediately supersedes the previous connection; the prior connection is closed with application code `4001` ("superseded").
- **Heartbeats**: The server sends a WebSocket ping frame every `WS_PING_INTERVAL_SECONDS` (default: 15s). The client must respond with pong. If no pong is received within `WS_PONG_TIMEOUT_SECONDS` (default: 40s), the connection is terminated.
- **Message Size Limits**:
  - Text frames: 4 KB maximum.
  - Binary frames: 8 KB maximum.
  - Fragmented messages exceeding these caps are rejected.

---

## 2. Binary Audio Framing

Audio is transported via binary WebSocket frames. Each binary frame starts with an 8-byte big-endian header followed by raw audio payload:

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

### Fields:
- **Type** (1 byte):
  - `1`: Uplink audio (Device -> Server)
  - `2`: Downlink audio (Server -> Device)
- **Flags** (1 byte):
  - Bit 0 (`0x01`): `LAST_FRAME` (marks the final audio frame of the turn).
  - Bits 1–7: Reserved (must be 0).
- **Turn ID** (2 bytes, uint16 big-endian): Matches the current `turn_id`.
- **Sequence Number** (4 bytes, uint32 big-endian): Starts at `0` for each turn and increments by 1 for each successive audio frame. Sequence gaps or duplicates result in `bad_frame` error.
- **Payload**: Raw PCM16 little-endian mono 16 kHz audio (typically 20 ms = 320 samples = 640 bytes).

---

## 3. Control Messages (Text Frames)

Control frames are JSON text messages containing a discriminator field `t`.

### 3.1 `hello` (Device -> Server)
First message sent immediately after WebSocket connection opens.
```json
{
  "t": "hello",
  "fw": "v1.0.0",
  "proto": 1,
  "codecs": ["pcm16"],
  "battery": 88
}
```

### 3.2 `ready` (Server -> Device)
Acknowledges `hello`, negotiates codec, and provides settings snapshot.
```json
{
  "t": "ready",
  "codec": "pcm16",
  "settings": {
    "version": 1,
    "button_a": { "src": "en", "dst": "es" },
    "button_b": { "src": "es", "dst": "en" },
    "volume": 70,
    "captions": false,
    "led_level": 50
  },
  "server_time": 1729850000,
  "limits": {
    "max_turn_seconds": 60,
    "daily_minutes_limit": 60
  }
}
```

### 3.3 `turn.start` (Device -> Server)
Triggered when the user presses button A or B.
```json
{
  "t": "turn.start",
  "turn_id": 1,
  "button": "a"
}
```

### 3.4 `turn.accepted` (Server -> Device)
Sent when the server confirms provider availability, quota, and state.
```json
{
  "t": "turn.accepted",
  "turn_id": 1,
  "src": "en",
  "dst": "es"
}
```

### 3.5 `turn.stop` (Device -> Server)
User released the button. Server enters `DRAINING`.
```json
{
  "t": "turn.stop",
  "turn_id": 1
}
```

### 3.6 `turn.cancel` (Device -> Server)
Aborts turn immediately (e.g., short tap or user abort).
```json
{
  "t": "turn.cancel",
  "turn_id": 1
}
```

### 3.7 `audio.start` (Server -> Device)
Notifies device that translated downlink audio is about to arrive.
```json
{
  "t": "audio.start",
  "turn_id": 1,
  "codec": "pcm16",
  "rate": 16000,
  "channels": 1
}
```

### 3.8 `audio.end` (Server -> Device)
Indicates all downlink audio has been transmitted for this turn.
```json
{
  "t": "audio.end",
  "turn_id": 1,
  "reason": "done"
}
```
`reason` is one of: `"done"`, `"cancelled"`, `"error"`.

### 3.9 `playback.done` (Device -> Server)
Device finished playing translated audio speaker output; resets state to `IDLE`.
```json
{
  "t": "playback.done",
  "turn_id": 1
}
```

### 3.10 `text` (Server -> Device)
Optional live captions (only sent if `captions: true` in settings).
```json
{
  "t": "text",
  "turn_id": 1,
  "kind": "target",
  "delta": "Hola mundo"
}
```

### 3.11 `settings.changed` (Server -> Device)
Pushed asynchronously when settings are updated via REST API.
```json
{
  "t": "settings.changed",
  "settings": {
    "version": 2,
    "button_a": { "src": "en", "dst": "fr" },
    "button_b": { "src": "fr", "dst": "en" },
    "volume": 80,
    "captions": true,
    "led_level": 60
  }
}
```

### 3.12 `error` (Bidirectional)
Reports an error.
```json
{
  "t": "error",
  "code": "bad_state",
  "message": "turn.start not allowed in LISTENING",
  "retryable": false,
  "turn_id": 1
}
```

### 3.13 `bye` (Server -> Device)
Sent before server initiates closure (e.g. device revoked, server shutdown).
```json
{
  "t": "bye",
  "reason": "device_revoked"
}
```

---

## 4. Connection State Machine

```
               [CONNECT]
                   │
                   ▼
             ┌───────────┐
             │ CONNECTED │
             └─────┬─────┘
                   │ hello
                   ▼
             ┌───────────┐
      ┌─────>│   IDLE    │<────────────────────────┐
      │      └─────┬─────┘                         │
      │            │ turn.start                    │
      │            ▼                               │
      │      ┌───────────┐                         │
      │      │ STARTING  │                         │
      │      └─────┬─────┘                         │
      │            │ session opened, turn.accepted │
      │            ▼                               │
      │      ┌───────────┐                         │
      │      │ LISTENING │                         │
      │      └─────┬─────┘                         │
      │            │ turn.stop                     │
      │            ▼                               │
      │      ┌───────────┐                         │
      │      │ DRAINING  │──(no audio, done)───────┤
      │      └─────┬─────┘                         │
      │            │ first downlink byte arrives   │
      │            ▼                               │
      │      ┌───────────┐                         │
      │      │  PLAYING  │                         │
      │      └─────┬─────┘                         │
      │            │ playback.done                 │
      └────────────┴───────────────────────────────┘
          (turn.cancel in any active turn state -> IDLE)
```

### State Transitions & Timers:
- **`CONNECTED`**: Accepts `hello` -> `IDLE` (sends `ready`).
- **`IDLE`**: Accepts `turn.start` -> `STARTING`.
- **`STARTING`**: Opens provider session. On success -> sends `turn.accepted`, enters `LISTENING`.
- **`LISTENING`**: Accepts uplink audio frames, `turn.stop`, `turn.cancel`. Timer `MAX_TURN_SECONDS` (60s) triggers implicit stop.
- **`DRAINING`**: Server awaits provider completion. Accepts `turn.cancel`. `DRAIN_TIMEOUT_SECONDS` (10s) terminates with `audio.end{reason: "error"}`.
- **`PLAYING`**: Awaits `playback.done`. `PLAYBACK_ACK_TIMEOUT_SECONDS` (30s) resets to `IDLE`.
