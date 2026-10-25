package audio

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
)

var (
	ErrInvalidByteLength = errors.New("PCM16 byte length must be even")
	ErrUnsupportedRate   = errors.New("unsupported sample rate for resampling")
)

var pcmBufferPool = sync.Pool{
	New: func() any {
		// Default 32 KB scratch buffer
		b := make([]byte, 32*1024)
		return &b
	},
}

// GetScratchBuffer borrows a scratch buffer from the pool.
func GetScratchBuffer(minCapacity int) *[]byte {
	buf := pcmBufferPool.Get().(*[]byte)
	if cap(*buf) < minCapacity {
		*buf = make([]byte, minCapacity)
	}
	*buf = (*buf)[:minCapacity]
	return buf
}

// PutScratchBuffer returns a scratch buffer to the pool.
func PutScratchBuffer(buf *[]byte) {
	if buf != nil && cap(*buf) <= 128*1024 {
		pcmBufferPool.Put(buf)
	}
}

// ResamplePCM16 resamples mono PCM16 little-endian audio from inRate to outRate.
func ResamplePCM16(input []byte, inRate, outRate int) ([]byte, error) {
	if len(input)%2 != 0 {
		return nil, ErrInvalidByteLength
	}
	if inRate <= 0 || outRate <= 0 {
		return nil, ErrUnsupportedRate
	}
	if inRate == outRate {
		out := make([]byte, len(input))
		copy(out, input)
		return out, nil
	}

	inSamplesCount := len(input) / 2
	if inSamplesCount == 0 {
		return []byte{}, nil
	}

	// Calculate output sample count
	outSamplesCount := int(math.Round(float64(inSamplesCount) * float64(outRate) / float64(inRate)))
	if outSamplesCount == 0 {
		return []byte{}, nil
	}

	outBytes := make([]byte, outSamplesCount*2)
	ratio := float64(inRate) / float64(outRate)

	for i := 0; i < outSamplesCount; i++ {
		srcPos := float64(i) * ratio
		srcIdx := int(srcPos)
		frac := srcPos - float64(srcIdx)

		var sampleVal int16
		if srcIdx >= inSamplesCount-1 {
			offset := (inSamplesCount - 1) * 2
			sampleVal = int16(binary.LittleEndian.Uint16(input[offset:]))
		} else {
			offset1 := srcIdx * 2
			offset2 := offset1 + 2
			s1 := float64(int16(binary.LittleEndian.Uint16(input[offset1:])))
			s2 := float64(int16(binary.LittleEndian.Uint16(input[offset2:])))
			interp := s1 + frac*(s2-s1)

			if interp > 32767 {
				interp = 32767
			} else if interp < -32768 {
				interp = -32768
			}
			sampleVal = int16(interp)
		}

		binary.LittleEndian.PutUint16(outBytes[i*2:], uint16(sampleVal))
	}

	return outBytes, nil
}

// FrameChunker buffers and slices PCM audio into fixed frame sizes.
type FrameChunker struct {
	frameBytes int
	buffer     []byte
}

// NewFrameChunker creates a chunker for frames of frameBytes length.
func NewFrameChunker(frameBytes int) *FrameChunker {
	return &FrameChunker{
		frameBytes: frameBytes,
		buffer:     make([]byte, 0, frameBytes*4),
	}
}

// Push appends data to the chunker and returns all complete frames.
func (fc *FrameChunker) Push(pcm []byte) [][]byte {
	fc.buffer = append(fc.buffer, pcm...)
	var frames [][]byte

	for len(fc.buffer) >= fc.frameBytes {
		frame := make([]byte, fc.frameBytes)
		copy(frame, fc.buffer[:fc.frameBytes])
		frames = append(frames, frame)
		fc.buffer = fc.buffer[fc.frameBytes:]
	}

	return frames
}

// Flush returns any remaining bytes as the last frame (padded with silence if needed).
func (fc *FrameChunker) Flush(padWithSilence bool) []byte {
	if len(fc.buffer) == 0 {
		return nil
	}

	frame := make([]byte, fc.frameBytes)
	copy(frame, fc.buffer)
	// Remaining bytes are 0 (PCM16 silence)
	fc.buffer = fc.buffer[:0]

	if !padWithSilence {
		return frame[:]
	}
	return frame
}
