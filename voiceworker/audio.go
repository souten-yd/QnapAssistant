package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type pcmAudio struct {
	SampleRate int
	Samples    []float32
}

func decodePCM16(data []byte, sampleRate int) (pcmAudio, error) {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if len(data) == 0 || len(data)%2 != 0 {
		return pcmAudio{}, errors.New("PCM16 body must contain an even, non-zero number of bytes")
	}
	samples := make([]float32, len(data)/2)
	for i := range samples {
		v := int16(binary.LittleEndian.Uint16(data[i*2:]))
		samples[i] = float32(v) / 32768.0
	}
	return pcmAudio{SampleRate: sampleRate, Samples: samples}, nil
}

func decodeWAV(data []byte) (pcmAudio, error) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return pcmAudio{}, errors.New("invalid RIFF/WAVE file")
	}
	var format, channels, bits uint16
	var sampleRate uint32
	var pcm []byte
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		n := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if n < 0 || pos+n > len(data) {
			return pcmAudio{}, errors.New("invalid WAV chunk length")
		}
		chunk := data[pos : pos+n]
		switch id {
		case "fmt ":
			if len(chunk) < 16 {
				return pcmAudio{}, errors.New("short WAV fmt chunk")
			}
			format = binary.LittleEndian.Uint16(chunk[0:2])
			channels = binary.LittleEndian.Uint16(chunk[2:4])
			sampleRate = binary.LittleEndian.Uint32(chunk[4:8])
			bits = binary.LittleEndian.Uint16(chunk[14:16])
		case "data":
			pcm = chunk
		}
		pos += n
		if n%2 == 1 {
			pos++
		}
	}
	if format != 1 || (channels != 1 && channels != 2) || bits != 16 || sampleRate == 0 || len(pcm) == 0 {
		return pcmAudio{}, fmt.Errorf("only PCM16 mono/stereo WAV is supported (format=%d channels=%d bits=%d rate=%d)", format, channels, bits, sampleRate)
	}
	frameBytes := int(channels) * 2
	frames := len(pcm) / frameBytes
	if frames == 0 {
		return pcmAudio{}, errors.New("WAV contains no complete audio frames")
	}
	out := make([]float32, frames)
	for i := 0; i < frames; i++ {
		off := i * frameBytes
		a := float32(int16(binary.LittleEndian.Uint16(pcm[off:]))) / 32768.0
		if channels == 2 {
			b := float32(int16(binary.LittleEndian.Uint16(pcm[off+2:]))) / 32768.0
			a = (a + b) * 0.5
		}
		out[i] = a
	}
	return pcmAudio{SampleRate: int(sampleRate), Samples: out}, nil
}

func encodeWAV(samples []float32, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	dataLen := len(samples) * 2
	b := bytes.NewBuffer(make([]byte, 0, 44+dataLen))
	b.WriteString("RIFF")
	_ = binary.Write(b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(b, binary.LittleEndian, uint32(16))
	_ = binary.Write(b, binary.LittleEndian, uint16(1))
	_ = binary.Write(b, binary.LittleEndian, uint16(1))
	_ = binary.Write(b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(b, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(b, binary.LittleEndian, uint16(2))
	_ = binary.Write(b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(b, binary.LittleEndian, uint32(dataLen))
	for _, f := range samples {
		if f > 1 {
			f = 1
		} else if f < -1 {
			f = -1
		}
		v := int16(math.Round(float64(f * 32767)))
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	return b.Bytes()
}
