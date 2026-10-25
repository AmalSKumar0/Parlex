package audio_test

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/audio"
)

func generateSineWavePCM16(sampleRate int, freq float64, durationMs int) []byte {
	numSamples := sampleRate * durationMs / 1000
	buf := make([]byte, numSamples*2)

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		val := math.Sin(2 * math.Pi * freq * t)
		sample := int16(val * 30000)
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(sample))
	}
	return buf
}

func TestResamplePCM16(t *testing.T) {
	t.Run("16kHz to 24kHz length correctness", func(t *testing.T) {
		// 1 second of 16 kHz audio = 16000 samples = 32000 bytes
		input := generateSineWavePCM16(16000, 440, 1000)
		require.Len(t, input, 32000)

		output, err := audio.ResamplePCM16(input, 16000, 24000)
		require.NoError(t, err)

		// 1 second of 24 kHz audio = 24000 samples = 48000 bytes
		assert.Len(t, output, 48000)
	})

	t.Run("24kHz to 16kHz length correctness", func(t *testing.T) {
		// 100 ms of 24 kHz audio = 2400 samples = 4800 bytes
		input := generateSineWavePCM16(24000, 440, 100)
		require.Len(t, input, 4800)

		output, err := audio.ResamplePCM16(input, 24000, 16000)
		require.NoError(t, err)

		// 100 ms of 16 kHz audio = 1600 samples = 3200 bytes
		assert.Len(t, output, 3200)
	})

	t.Run("Identity resampling", func(t *testing.T) {
		input := generateSineWavePCM16(16000, 440, 20)
		output, err := audio.ResamplePCM16(input, 16000, 16000)
		require.NoError(t, err)
		assert.Equal(t, input, output)
	})

	t.Run("Errors on odd byte length", func(t *testing.T) {
		_, err := audio.ResamplePCM16([]byte{1, 2, 3}, 16000, 24000)
		require.ErrorIs(t, err, audio.ErrInvalidByteLength)
	})

	t.Run("Errors on zero sample rates", func(t *testing.T) {
		_, err := audio.ResamplePCM16([]byte{1, 2}, 0, 24000)
		require.ErrorIs(t, err, audio.ErrUnsupportedRate)
	})
}

func TestFrameChunker(t *testing.T) {
	// Frame size 640 bytes (20 ms @ 16 kHz)
	chunker := audio.NewFrameChunker(640)

	// Feed 1000 bytes -> should produce 1 frame (640 bytes), leaving 360 bytes in buffer
	input1 := make([]byte, 1000)
	frames1 := chunker.Push(input1)
	assert.Len(t, frames1, 1)
	assert.Len(t, frames1[0], 640)

	// Feed another 300 bytes -> total 660 bytes -> produces 1 frame (640 bytes), leaving 20 bytes
	input2 := make([]byte, 300)
	frames2 := chunker.Push(input2)
	assert.Len(t, frames2, 1)
	assert.Len(t, frames2[0], 640)

	// Flush with padding
	flushed := chunker.Flush(true)
	assert.Len(t, flushed, 640)

	// Second flush is nil
	assert.Nil(t, chunker.Flush(true))
}
