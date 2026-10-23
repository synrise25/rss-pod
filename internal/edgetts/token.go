// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from rany2/edge-tts 7.3.0 src/edge_tts/drm.py. See NOTICE.

package edgetts

import (
	"encoding/binary"
	"math/bits"
)

// Edge v2 uses a modified SHA-256 digest and a CS64 checksum. These constants
// and the partial first block follow the upstream implementation; replacing
// this digest with crypto/sha256 does not produce a valid handshake token.
var tokenInitial = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

var tokenState = [8]uint32{
	0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A,
	0x6C006F49, 0x04BD35AB, 0xFEB0A7E4, 0x03EFFBD2,
}

var tokenSchedule = [...]uint32{
	0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA,
	0xA622EE8C, 0xD1DD9491, 0xC0DD25A0, 0xF9E70AB6,
	0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33,
	0xD258B060, 0x72766564,
}

var tokenRoundConstants = [...]uint32{
	0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5,
	0x3956C25B, 0x59F111F1, 0x923F82A4, 0xAB1C5ED5,
	0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3,
	0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
	0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC,
	0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA,
	0x983E5152, 0xA831C66D, 0xB00327C8, 0xBF597FC7,
	0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967,
	0x27B70A85, 0x2E1B2138, 0x4D2C6DFC, 0x53380D13,
	0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85,
	0xA2BFE8A1, 0xA81A664B, 0xC24B8B70, 0xC76C51A3,
	0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070,
	0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5,
	0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3,
	0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208,
	0x90BEFFFA, 0xA4506CEB, 0xBEF9A3F7, 0xC67178F2,
}

func tokenSigma0(x uint32) uint32 {
	return bits.RotateLeft32(x, -7) ^ bits.RotateLeft32(x, -18) ^ (x >> 3)
}

func tokenSigma1(x uint32) uint32 {
	return bits.RotateLeft32(x, -17) ^ bits.RotateLeft32(x, -19) ^ (x >> 10)
}

func tokenExpand(words *[64]uint32, start int) {
	for i := start; i < len(words); i++ {
		words[i] = tokenSigma1(words[i-2]) + words[i-7] + tokenSigma0(words[i-15]) + words[i-16]
	}
}

func tokenRounds(state [8]uint32, words *[64]uint32, start int) [8]uint32 {
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for i := start; i < 64; i++ {
		k := tokenRoundConstants[i]
		if start == 14 && i == 14 {
			k = 0xC9DC06A7
		}
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		t1 := h + s1 + (g ^ (e & (f ^ g))) + k + words[i]
		t2 := s0 + ((a & b) | (c & (a | b)))
		a, b, c, d, e, f, g, h = t1+t2, a, b, c, d+t1, e, f, g
	}
	return [8]uint32{a, b, c, d, e, f, g, h}
}

func tokenSHA256(data []byte) [32]byte {
	var words [64]uint32
	words[14] = uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2])
	words[15] = binary.BigEndian.Uint32(data[3:7])
	for i := 16; i < 30; i++ {
		value := tokenSchedule[i-16]
		if i-2 >= 14 {
			x := tokenSigma1(words[i-2])
			if i == 16 {
				x ^= 0x000B92C0
			}
			value += x
		}
		if i-7 >= 14 {
			value += words[i-7]
		}
		if i-15 >= 14 {
			x := tokenSigma0(words[i-15])
			if i == 29 {
				x ^= 0x059C0B80
			}
			value += x
		}
		words[i] = value
	}
	words[30] = tokenSigma1(words[28]) + words[23] + tokenSigma0(words[15]) + words[14] + uint32(0xC9DC06A7-0x9BDC06A7)
	tokenExpand(&words, 31)
	working := tokenRounds(tokenState, &words, 14)
	state := tokenInitial
	for i := range state {
		state[i] += working[i]
	}

	length := 57 + len(data)
	padded := append([]byte(nil), data[7:]...)
	padded = append(padded, 0x80)
	padding := (55 - length%64 + 64) % 64
	padded = append(padded, make([]byte, padding)...)
	padded = binary.BigEndian.AppendUint64(padded, uint64(length)*8)
	for offset := 0; offset < len(padded); offset += 64 {
		for i := 0; i < 16; i++ {
			words[i] = binary.BigEndian.Uint32(padded[offset+i*4:])
		}
		tokenExpand(&words, 16)
		working = tokenRounds(state, &words, 0)
		for i := range state {
			state[i] += working[i]
		}
	}
	var digest [32]byte
	for i, word := range state {
		binary.BigEndian.PutUint32(digest[i*4:], word)
	}
	return digest
}

var tokenCS64Coefficients = [...][2]uint32{
	{0xE82E91B1, 0xB32700D0}, {0x44DE24C6, 0x88DFF51D},
	{0x3BEB65BC, 0x29514038}, {0xC89C5933, 0xB328CBDF},
	{0x8D1EC211, 0xD405B4D5}, {0x314EF5D5, 0x26E55F3C},
	{0xE92491B1, 0x607801C6}, {0xF0A5B9D5, 0x82B2C1FB},
	{0x06064CF3, 0xFA51A3DA}, {0xDA79FA60, 0x207CC464},
	{0xBED20560, 0x3C3C74F3}, {0x89F9C9F8, 0xD2D14B61},
	{0x35FA38C6, 0xBB04A2C1}, {0xCB49E3C6, 0xC5626167},
}

func tokenCS64(data []byte, digest [32]byte) [8]byte {
	keys := [2]uint32{binary.LittleEndian.Uint32(digest[:4]) | 1, binary.LittleEndian.Uint32(digest[4:8]) | 1}
	factors := [2]uint32{keys[0] * 0x82893933, keys[1] * 0x82893933}
	var terms, offsets []uint32
	for i, pair := range tokenCS64Coefficients {
		terms = append(terms, pair[0]*factors[i%2])
		offsets = append(offsets, pair[1])
	}
	padded := append([]byte{0}, data...)
	padded = append(padded, make([]byte, (8-len(padded)%8)%8)...)
	for i := 0; i < len(padded)/4; i++ {
		word := binary.LittleEndian.Uint32(padded[i*4:])
		term := word * keys[i%2]
		coefficient := uint32(0xEF0569FB)
		if i%2 == 1 {
			coefficient = 0xC31713DB
		}
		offset := bits.RotateLeft32(word, 16) * coefficient
		if i == 0 {
			term += 0xF2F90B1A * factors[0]
			offset += 0x0B1A0000
		}
		terms = append(terms, term)
		offsets = append(offsets, offset)
	}
	var swapAcc, swapLast, state, acc uint32
	for i := 0; i < len(terms); i += 2 {
		x, y := terms[i], terms[i+1]
		q := bits.RotateLeft32(offsets[i]+x, 16) * 0x76F50F34
		swapAcc += bits.RotateLeft32(q, 16)*0xC3EFEA97 + q
		q = bits.RotateLeft32(offsets[i+1]+y, 16) * 0x1881CE3C
		swapLast = bits.RotateLeft32(q, 16)*0x35BD1EC9 + q
		swapAcc += swapLast
		state = state*keys[0] + x
		for _, multiplier := range [...]uint32{0xCF98B111, 0x87085B9F, 0x12CEB96D, 0x257E1D83} {
			state = bits.RotateLeft32(state, 16) * multiplier
		}
		acc += state
		state = state*keys[1] + y
		for _, multiplier := range [...]uint32{0xA27416F5, 0xD38396FF, 0x7C932B89, 0xBFA49F69} {
			state = bits.RotateLeft32(state, 16) * multiplier
		}
		acc += state
	}
	var checksum [8]byte
	binary.LittleEndian.PutUint32(checksum[:4], swapLast^state)
	binary.LittleEndian.PutUint32(checksum[4:], swapAcc^acc)
	return checksum
}
