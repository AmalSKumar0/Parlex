package gateway_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/gateway"
)

func TestBinaryFrameCodec(t *testing.T) {
	t.Run("Encode and decode uplink frame", func(t *testing.T) {
		payload := []byte{1, 2, 3, 4, 5, 6}
		encoded := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, 0, 42, 100, payload)
		require.Len(t, encoded, 8+6)

		hdr, decPayload, err := gateway.ParseBinaryFrame(encoded)
		require.NoError(t, err)
		assert.Equal(t, gateway.BinaryTypeUplink, hdr.Type)
		assert.Equal(t, byte(0), hdr.Flags)
		assert.False(t, hdr.IsLastFrame())
		assert.Equal(t, uint16(42), hdr.TurnID)
		assert.Equal(t, uint32(100), hdr.Seq)
		assert.Equal(t, payload, decPayload)
	})

	t.Run("Encode and decode downlink frame with LAST_FRAME flag", func(t *testing.T) {
		payload := []byte{10, 20}
		encoded := gateway.EncodeBinaryFrame(gateway.BinaryTypeDownlink, gateway.FlagLastFrame, 1, 0, payload)

		hdr, decPayload, err := gateway.ParseBinaryFrame(encoded)
		require.NoError(t, err)
		assert.Equal(t, gateway.BinaryTypeDownlink, hdr.Type)
		assert.True(t, hdr.IsLastFrame())
		assert.Equal(t, uint16(1), hdr.TurnID)
		assert.Equal(t, uint32(0), hdr.Seq)
		assert.Equal(t, payload, decPayload)
	})

	t.Run("Rejects frame under 8 bytes", func(t *testing.T) {
		_, _, err := gateway.ParseBinaryFrame([]byte{1, 0, 0, 1})
		require.ErrorIs(t, err, gateway.ErrFrameTooShort)
	})

	t.Run("Rejects invalid frame type", func(t *testing.T) {
		frame := gateway.EncodeBinaryFrame(3, 0, 1, 0, []byte{1, 2})
		_, _, err := gateway.ParseBinaryFrame(frame)
		require.ErrorIs(t, err, gateway.ErrInvalidFrameType)
	})

	t.Run("Rejects invalid flags with reserved bits set", func(t *testing.T) {
		frame := gateway.EncodeBinaryFrame(gateway.BinaryTypeUplink, 0x02, 1, 0, []byte{1, 2})
		_, _, err := gateway.ParseBinaryFrame(frame)
		require.ErrorIs(t, err, gateway.ErrInvalidFrameFlags)
	})
}

func FuzzBinaryFrameParser(f *testing.F) {
	// Seed valid frames
	f.Add(gateway.EncodeBinaryFrame(1, 0, 1, 0, []byte("pcm")))
	f.Add(gateway.EncodeBinaryFrame(2, 1, 2, 5, []byte("audio")))
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on arbitrary binary fuzz input
		_, _, _ = gateway.ParseBinaryFrame(data)
	})
}

func FuzzTextMessageParser(f *testing.F) {
	f.Add([]byte(`{"t":"hello","fw":"v1"}`))
	f.Add([]byte(`{"t":"turn.start","turn_id":1,"button":"a"}`))
	f.Add([]byte(`{"t":"invalid}`))
	f.Add([]byte(`{}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic on arbitrary JSON text input
		_, _ = gateway.ParseTextMessage(data)
	})
}

func TestJsonMessages(t *testing.T) {
	t.Run("Parse valid control messages", func(t *testing.T) {
		raw := []byte(`{"t":"turn.start","turn_id":5,"button":"b"}`)
		msgType, err := gateway.ParseTextMessage(raw)
		require.NoError(t, err)
		assert.Equal(t, gateway.TypeTurnStart, msgType)

		var ts gateway.TurnStartMessage
		err = json.Unmarshal(raw, &ts)
		require.NoError(t, err)
		assert.Equal(t, uint16(5), ts.TurnID)
		assert.Equal(t, "b", ts.Button)
	})
}
