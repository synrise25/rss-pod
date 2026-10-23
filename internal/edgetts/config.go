// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import (
	"fmt"
	"regexp"
)

var (
	ratePattern   = regexp.MustCompile(`^[+-]\d+%$`)
	volumePattern = regexp.MustCompile(`^[+-]\d+%$`)
	pitchPattern  = regexp.MustCompile(`^[+-]\d+Hz$`)
)

// TTSConfig holds the configuration for a TTS request.
type TTSConfig struct {
	Voice    string
	Rate     string
	Volume   string
	Pitch    string
	Boundary BoundaryType
}

// DefaultConfig returns a TTSConfig with default values.
func DefaultConfig() TTSConfig {
	return TTSConfig{
		Voice:    DefaultVoice,
		Rate:     "+0%",
		Volume:   "+0%",
		Pitch:    "+0Hz",
		Boundary: SentenceBoundary,
	}
}

// Validate checks the configuration values and returns an error if any are invalid.
func (c TTSConfig) Validate() error {
	if c.Voice == "" {
		return fmt.Errorf("%w: voice must not be empty", ErrInvalidConfig)
	}
	if !ratePattern.MatchString(c.Rate) {
		return fmt.Errorf("%w: rate %q must match pattern [+-]N%%", ErrInvalidConfig, c.Rate)
	}
	if !volumePattern.MatchString(c.Volume) {
		return fmt.Errorf("%w: volume %q must match pattern [+-]N%%", ErrInvalidConfig, c.Volume)
	}
	if !pitchPattern.MatchString(c.Pitch) {
		return fmt.Errorf("%w: pitch %q must match pattern [+-]NHz", ErrInvalidConfig, c.Pitch)
	}
	if c.Boundary != WordBoundary && c.Boundary != SentenceBoundary {
		return fmt.Errorf("%w: boundary must be WordBoundary or SentenceBoundary", ErrInvalidConfig)
	}
	return nil
}
