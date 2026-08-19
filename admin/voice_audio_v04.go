package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type serverPCMAudio struct {
	SampleRate int
	Samples    []float32
}

func decodeServerWAV(data []byte) (serverPCMAudio, error) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return serverPCMAudio{}, errors.New("invalid RIFF/WAVE file")
	}
	var format, channels, bits uint16
	var sampleRate uint32
	var pcm []byte
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		n := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if n < 0 || pos+n > len(data) {
			return serverPCMAudio{}, errors.New("invalid WAV chunk length")
		}
		chunk := data[pos : pos+n]
		switch id {
		case "fmt ":
			if len(chunk) < 16 {
				return serverPCMAudio{}, errors.New("short WAV fmt chunk")
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
		return serverPCMAudio{}, fmt.Errorf("only PCM16 mono/stereo WAV is supported (format=%d channels=%d bits=%d rate=%d)", format, channels, bits, sampleRate)
	}
	frameBytes := int(channels) * 2
	frames := len(pcm) / frameBytes
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
	return serverPCMAudio{SampleRate: int(sampleRate), Samples: out}, nil
}

func encodeServerWAV(samples []float32, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = 16000
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

func audioPeak(samples []float32) float64 {
	peak := 0.0
	for _, s := range samples {
		v := math.Abs(float64(s))
		if v > peak {
			peak = v
		}
	}
	return peak
}

func limitAudioPeak(samples []float32, target float64) float64 {
	peak := audioPeak(samples)
	if target <= 0 || target >= 1 || peak <= target || peak == 0 {
		return peak
	}
	scale := float32(target / peak)
	for i := range samples {
		samples[i] *= scale
	}
	return audioPeak(samples)
}

func resampleBandlimited(samples []float32, srcRate, dstRate int) []float32 {
	if len(samples) == 0 || srcRate <= 0 || dstRate <= 0 || srcRate == dstRate {
		return append([]float32(nil), samples...)
	}
	outLen := int(math.Round(float64(len(samples)) * float64(dstRate) / float64(srcRate)))
	if outLen < 1 {
		outLen = 1
	}
	out := make([]float32, outLen)
	const half = 16
	cutoff := 0.47
	if dstRate < srcRate {
		cutoff = 0.5 * float64(dstRate) / float64(srcRate) * 0.94
	}
	for i := range out {
		center := float64(i) * float64(srcRate) / float64(dstRate)
		base := int(math.Floor(center))
		var sum, weightSum float64
		for k := -half + 1; k <= half; k++ {
			idx := base + k
			if idx < 0 || idx >= len(samples) {
				continue
			}
			x := center - float64(idx)
			if math.Abs(x) > half {
				continue
			}
			arg := 2 * cutoff * x
			sinc := 1.0
			if math.Abs(arg) > 1e-12 {
				sinc = math.Sin(math.Pi*arg) / (math.Pi * arg)
			}
			window := 0.5 + 0.5*math.Cos(math.Pi*x/half)
			weight := 2 * cutoff * sinc * window
			sum += float64(samples[idx]) * weight
			weightSum += weight
		}
		if math.Abs(weightSum) > 1e-12 {
			out[i] = float32(sum / weightSum)
		}
	}
	return out
}

type processedVoiceAudio struct {
	WAV              []byte
	SourceSampleRate int
	SampleRate       int
	Peak             float64
	DurationSeconds  float64
}

func processVoiceWAV(wav []byte, targetSampleRate int, peakTarget float64) (processedVoiceAudio, error) {
	audio, err := decodeServerWAV(wav)
	if err != nil {
		return processedVoiceAudio{}, err
	}
	sourceRate := audio.SampleRate
	if targetSampleRate > 0 && targetSampleRate != audio.SampleRate {
		audio.Samples = resampleBandlimited(audio.Samples, audio.SampleRate, targetSampleRate)
		audio.SampleRate = targetSampleRate
	}
	peak := limitAudioPeak(audio.Samples, peakTarget)
	duration := 0.0
	if audio.SampleRate > 0 {
		duration = float64(len(audio.Samples)) / float64(audio.SampleRate)
	}
	return processedVoiceAudio{WAV: encodeServerWAV(audio.Samples, audio.SampleRate), SourceSampleRate: sourceRate, SampleRate: audio.SampleRate, Peak: peak, DurationSeconds: duration}, nil
}
