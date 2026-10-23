// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import "errors"

var (
	// ErrNoAudioReceived is returned when the TTS stream completes without any audio data.
	ErrNoAudioReceived = errors.New("edgetts: no audio received from server")

	// ErrUnexpectedResponse is returned when the server sends a malformed message.
	ErrUnexpectedResponse = errors.New("edgetts: unexpected response from server")

	// ErrUnknownResponse is returned when the server sends an unrecognized metadata type.
	ErrUnknownResponse = errors.New("edgetts: unknown response type from server")

	// ErrWebSocket is returned when a WebSocket connection error occurs.
	ErrWebSocket = errors.New("edgetts: websocket error")

	// ErrSkewAdjustment is returned when clock skew correction fails.
	ErrSkewAdjustment = errors.New("edgetts: failed to adjust clock skew")

	// ErrInvalidConfig is returned when TTS configuration parameters are invalid.
	ErrInvalidConfig = errors.New("edgetts: invalid configuration")

	// ErrMixedBoundaryTypes is returned when word and sentence boundaries are mixed.
	ErrMixedBoundaryTypes = errors.New("edgetts: cannot mix word and sentence boundary types")
)
