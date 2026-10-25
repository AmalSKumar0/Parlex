package gateway

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrFrameTooShort     = errors.New("binary frame too short (under 8 bytes)")
	ErrInvalidFrameType  = errors.New("invalid binary frame type (must be 1 for uplink or 2 for downlink)")
	ErrInvalidFrameFlags = errors.New("reserved flags bits must be 0")
	ErrInvalidMessage    = errors.New("invalid or empty message type")
)

const (
	BinaryTypeUplink   byte = 1
	BinaryTypeDownlink byte = 2

	FlagLastFrame byte = 0x01
	FlagsReserved byte = 0xFE // bits 1..7 must be 0
)

// BinaryHeader contains the decoded 8-byte big-endian header.
type BinaryHeader struct {
	Type   byte
	Flags  byte
	TurnID uint16
	Seq    uint32
}

// IsLastFrame returns true if the LAST_FRAME flag bit is set.
func (h BinaryHeader) IsLastFrame() bool {
	return (h.Flags & FlagLastFrame) != 0
}

// ParseBinaryFrame parses an 8-byte big-endian header and payload from raw frame.
func ParseBinaryFrame(data []byte) (BinaryHeader, []byte, error) {
	if len(data) < 8 {
		return BinaryHeader{}, nil, ErrFrameTooShort
	}

	frameType := data[0]
	if frameType != BinaryTypeUplink && frameType != BinaryTypeDownlink {
		return BinaryHeader{}, nil, fmt.Errorf("%w: %d", ErrInvalidFrameType, frameType)
	}

	flags := data[1]
	if (flags & FlagsReserved) != 0 {
		return BinaryHeader{}, nil, ErrInvalidFrameFlags
	}

	turnID := binary.BigEndian.Uint16(data[2:4])
	seq := binary.BigEndian.Uint32(data[4:8])

	return BinaryHeader{
		Type:   frameType,
		Flags:  flags,
		TurnID: turnID,
		Seq:    seq,
	}, data[8:], nil
}

// EncodeBinaryFrame builds an 8-byte big-endian header followed by payload.
func EncodeBinaryFrame(frameType, flags byte, turnID uint16, seq uint32, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	buf[0] = frameType
	buf[1] = flags
	binary.BigEndian.PutUint16(buf[2:4], turnID)
	binary.BigEndian.PutUint32(buf[4:8], seq)
	copy(buf[8:], payload)
	return buf
}

// Text message discriminator types
const (
	TypeHello           = "hello"
	TypeReady           = "ready"
	TypeTurnStart       = "turn.start"
	TypeTurnAccepted    = "turn.accepted"
	TypeTurnStop        = "turn.stop"
	TypeTurnCancel      = "turn.cancel"
	TypeText            = "text"
	TypeAudioStart      = "audio.start"
	TypeAudioEnd        = "audio.end"
	TypePlaybackDone    = "playback.done"
	TypeSettingsChanged = "settings.changed"
	TypeError           = "error"
	TypeBye             = "bye"
)

type BaseMessage struct {
	Type string `json:"t"`
}

type HelloMessage struct {
	Type    string   `json:"t"`
	FW      string   `json:"fw"`
	Proto   int      `json:"proto"`
	Codecs  []string `json:"codecs"`
	Battery *int     `json:"battery,omitempty"`
}

type ReadyMessage struct {
	Type       string         `json:"t"`
	Codec      string         `json:"codec"`
	Settings   map[string]any `json:"settings"`
	ServerTime int64          `json:"server_time"`
	Limits     map[string]any `json:"limits"`
}

type TurnStartMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
	Button string `json:"button"` // "a" or "b"
}

type TurnAcceptedMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
	Src    string `json:"src"`
	Dst    string `json:"dst"`
}

type TurnStopMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
}

type TurnCancelMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
}

type TextMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
	Kind   string `json:"kind"` // "source" or "target"
	Delta  string `json:"delta"`
}

type AudioStartMessage struct {
	Type     string `json:"t"`
	TurnID   uint16 `json:"turn_id"`
	Codec    string `json:"codec"`
	Rate     int    `json:"rate"`
	Channels int    `json:"channels"`
}

type AudioEndMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
	Reason string `json:"reason"` // "done", "cancelled", "error"
}

type PlaybackDoneMessage struct {
	Type   string `json:"t"`
	TurnID uint16 `json:"turn_id"`
}

type SettingsChangedMessage struct {
	Type     string         `json:"t"`
	Settings map[string]any `json:"settings"`
}

type ErrorMessage struct {
	Type      string  `json:"t"`
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Retryable bool    `json:"retryable"`
	TurnID    *uint16 `json:"turn_id,omitempty"`
}

type ByeMessage struct {
	Type   string `json:"t"`
	Reason string `json:"reason"`
}

// ParseTextMessage extracts the message type discriminator and raw data.
func ParseTextMessage(data []byte) (string, error) {
	var base BaseMessage
	if err := json.Unmarshal(data, &base); err != nil {
		return "", err
	}
	if base.Type == "" {
		return "", ErrInvalidMessage
	}
	return base.Type, nil
}
