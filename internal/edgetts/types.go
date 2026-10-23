// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import "time"

// ChunkType represents the type of a TTS chunk.
type ChunkType string

const (
	// ChunkTypeAudio indicates the chunk contains audio data.
	ChunkTypeAudio ChunkType = "audio"
	// ChunkTypeWordBoundary indicates the chunk contains word boundary metadata.
	ChunkTypeWordBoundary ChunkType = "WordBoundary"
	// ChunkTypeSentenceBoundary indicates the chunk contains sentence boundary metadata.
	ChunkTypeSentenceBoundary ChunkType = "SentenceBoundary"
)

// TTSChunk represents a single chunk of TTS output, either audio data or metadata.
type TTSChunk struct {
	Type     ChunkType
	Data     []byte        // Audio data (only for ChunkTypeAudio)
	Offset   time.Duration // Time offset (only for metadata chunks)
	Duration time.Duration // Duration (only for metadata chunks)
	Text     string        // Text content (only for metadata chunks)
}

// Voice represents a single available TTS voice.
type Voice struct {
	Name           string   `json:"Name"`
	ShortName      string   `json:"ShortName"`
	Gender         string   `json:"Gender"`
	Locale         string   `json:"Locale"`
	SuggestedCodec string   `json:"SuggestedCodec"`
	FriendlyName   string   `json:"FriendlyName"`
	Status         string   `json:"Status"`
	VoiceTag       VoiceTag `json:"VoiceTag"`
}

// VoiceTag contains categorization information for a voice.
type VoiceTag struct {
	ContentCategories  []string `json:"ContentCategories"`
	VoicePersonalities []string `json:"VoicePersonalities"`
}

// BoundaryType specifies the type of boundary metadata to request.
type BoundaryType string

const (
	// WordBoundary requests word-level timing metadata.
	WordBoundary BoundaryType = "WordBoundary"
	// SentenceBoundary requests sentence-level timing metadata.
	SentenceBoundary BoundaryType = "SentenceBoundary"
)
