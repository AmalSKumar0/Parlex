package audio

import (
	"bytes"
	"encoding/binary"
)

// EncodeWAV wraps raw mono PCM16 audio bytes with a standard 44-byte RIFF/WAVE header.
func EncodeWAV(pcm []byte, sampleRate int) []byte {
	buf := new(bytes.Buffer)
	numChannels := uint16(1)
	bitsPerSample := uint16(16)
	byteRate := uint32(sampleRate * int(numChannels) * int(bitsPerSample) / 8)
	blockAlign := uint16(numChannels * bitsPerSample / 8)
	subchunk2Size := uint32(len(pcm))
	chunkSize := 36 + subchunk2Size

	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, chunkSize)
	buf.WriteString("WAVE")

	// fmt subchunk
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // subchunk1Size
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // audioFormat: 1 (PCM)
	_ = binary.Write(buf, binary.LittleEndian, numChannels)
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, byteRate)
	_ = binary.Write(buf, binary.LittleEndian, blockAlign)
	_ = binary.Write(buf, binary.LittleEndian, bitsPerSample)

	// data subchunk
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, subchunk2Size)
	buf.Write(pcm)

	return buf.Bytes()
}
