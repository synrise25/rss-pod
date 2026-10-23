// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// xmlEscape escapes special XML characters in a string.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// makeSSML generates an SSML document for the given config and pre-escaped text.
func makeSSML(cfg TTSConfig, escapedText string) string {
	var b strings.Builder
	b.WriteString("<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'>")
	b.WriteString("<voice name='")
	b.WriteString(cfg.Voice)
	b.WriteString("'>")
	b.WriteString("<prosody pitch='")
	b.WriteString(cfg.Pitch)
	b.WriteString("' rate='")
	b.WriteString(cfg.Rate)
	b.WriteString("' volume='")
	b.WriteString(cfg.Volume)
	b.WriteString("'>")
	b.WriteString(escapedText)
	b.WriteString("</prosody></voice></speak>")
	return b.String()
}

// removeIncompatibleCharacters replaces control characters that are unsupported
// by the TTS service with spaces.
func removeIncompatibleCharacters(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 0 && r <= 8) || r == 11 || r == 12 || (r >= 14 && r <= 31) {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitTextByByteLength splits text into chunks not exceeding maxBytes,
// preferring to split at newline or space boundaries.
func splitTextByByteLength(text string, maxBytes int) []string {
	textBytes := []byte(text)
	if len(textBytes) <= maxBytes {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	}

	var chunks []string
	for len(textBytes) > 0 {
		if len(textBytes) <= maxBytes {
			trimmed := strings.TrimSpace(string(textBytes))
			if trimmed != "" {
				chunks = append(chunks, trimmed)
			}
			break
		}

		chunk := textBytes[:maxBytes]

		// Find the best split point.
		splitAt := -1

		// Prefer splitting at newline.
		if idx := bytes.LastIndexByte(chunk, '\n'); idx != -1 {
			splitAt = idx
		}

		// Fall back to space.
		if splitAt == -1 {
			if idx := bytes.LastIndexByte(chunk, ' '); idx != -1 {
				splitAt = idx
			}
		}

		// Fall back to a safe UTF-8 boundary.
		if splitAt == -1 {
			splitAt = maxBytes
			for splitAt > 0 && !utf8.RuneStart(textBytes[splitAt]) {
				splitAt--
			}
			// Avoid splitting inside XML entities like &amp;
			if ampIdx := bytes.LastIndexByte(textBytes[:splitAt], '&'); ampIdx != -1 {
				if !bytes.Contains(textBytes[ampIdx:splitAt], []byte(";")) {
					splitAt = ampIdx
				}
			}
		}

		if splitAt == 0 {
			// Impossible to split cleanly; take one rune.
			_, size := utf8.DecodeRune(textBytes)
			splitAt = size
		}

		trimmed := strings.TrimSpace(string(textBytes[:splitAt]))
		if trimmed != "" {
			chunks = append(chunks, trimmed)
		}
		textBytes = textBytes[splitAt:]
	}

	return chunks
}

// isEmptyOrWhitespace returns true if the string contains only whitespace.
func isEmptyOrWhitespace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
