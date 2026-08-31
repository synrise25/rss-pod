package audioinfo

import (
	"encoding/binary"
	"fmt"
	"time"
)

// MP3Duration calculates duration from MPEG audio frame headers without
// decoding the audio payload. This keeps composition lightweight while still
// supporting constant- and variable-bitrate MP3 output.
func MP3Duration(data []byte) (time.Duration, error) {
	var duration time.Duration
	frames := 0
	for offset := 0; offset+4 <= len(data); {
		if next, ok := skipID3v2(data, offset); ok {
			offset = next
			continue
		}

		frame, ok := parseFrameHeader(data[offset : offset+4])
		if !ok {
			offset++
			continue
		}
		if offset+frame.size > len(data) {
			break
		}
		duration += time.Duration(frame.samples) * time.Second / time.Duration(frame.sampleRate)
		frames++
		offset += frame.size
	}
	if frames == 0 {
		return 0, fmt.Errorf("MP3 contains no complete audio frames")
	}
	return duration, nil
}

type frameHeader struct {
	size       int
	samples    int
	sampleRate int
}

func parseFrameHeader(data []byte) (frameHeader, bool) {
	value := binary.BigEndian.Uint32(data)
	if value>>21 != 0x7ff {
		return frameHeader{}, false
	}
	versionID := int((value >> 19) & 0x3)
	layerID := int((value >> 17) & 0x3)
	bitrateIndex := int((value >> 12) & 0xf)
	sampleRateIndex := int((value >> 10) & 0x3)
	padding := int((value >> 9) & 0x1)
	if versionID == 1 || layerID == 0 || bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return frameHeader{}, false
	}

	layer := 4 - layerID
	bitrate := bitrateKbps(versionID, layer, bitrateIndex)
	sampleRate := sampleRateHz(versionID, sampleRateIndex)
	if bitrate == 0 || sampleRate == 0 {
		return frameHeader{}, false
	}

	var size, samples int
	switch layer {
	case 1:
		size = (12*bitrate*1000/sampleRate + padding) * 4
		samples = 384
	case 2:
		size = 144*bitrate*1000/sampleRate + padding
		samples = 1152
	case 3:
		if versionID == 3 {
			size = 144*bitrate*1000/sampleRate + padding
			samples = 1152
		} else {
			size = 72*bitrate*1000/sampleRate + padding
			samples = 576
		}
	}
	if size < 4 {
		return frameHeader{}, false
	}
	return frameHeader{size: size, samples: samples, sampleRate: sampleRate}, true
}

func bitrateKbps(versionID, layer, index int) int {
	var table []int
	if versionID == 3 {
		switch layer {
		case 1:
			table = []int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}
		case 2:
			table = []int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384}
		case 3:
			table = []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
		}
	} else if layer == 1 {
		table = []int{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}
	} else {
		table = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	}
	if len(table) <= index {
		return 0
	}
	return table[index]
}

func sampleRateHz(versionID, index int) int {
	sampleRate := []int{44100, 48000, 32000}[index]
	switch versionID {
	case 2:
		return sampleRate / 2
	case 0:
		return sampleRate / 4
	default:
		return sampleRate
	}
}

func skipID3v2(data []byte, offset int) (int, bool) {
	if offset+10 > len(data) || string(data[offset:offset+3]) != "ID3" {
		return offset, false
	}
	sizeBytes := data[offset+6 : offset+10]
	for _, value := range sizeBytes {
		if value&0x80 != 0 {
			return offset, false
		}
	}
	size := int(sizeBytes[0])<<21 | int(sizeBytes[1])<<14 | int(sizeBytes[2])<<7 | int(sizeBytes[3])
	next := offset + 10 + size
	if data[offset+5]&0x10 != 0 {
		next += 10
	}
	if next > len(data) {
		return offset, false
	}
	return next, true
}
