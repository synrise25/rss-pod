// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

const (
	// trustedClientToken is the token used to authenticate with the Edge TTS service.
	trustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

	// secMSGECVersion is the Chromium version used for security token generation.
	secMSGECVersion = "2-143.0.3650.75"

	// baseURL is the base URL for the Edge TTS service.
	baseURL = "speech.platform.bing.com/consumer/speech/synthesize/readaloud"

	// voiceListURL is the URL for fetching the list of available voices.
	voiceListURL = "https://" + baseURL + "/voices/list?trustedclienttoken=" + trustedClientToken

	// wssURL is the WebSocket URL for the Edge TTS service.
	wssURL = "wss://" + baseURL + "/edge/v1"

	// outputFormat is the audio output format requested from the service.
	outputFormat = "audio-24khz-48kbitrate-mono-mp3"

	// chromiumFullVersion is used in User-Agent and GEC version headers.
	chromiumFullVersion = "143.0.3650.75"

	// chromiumMajorVersion is the major version component.
	chromiumMajorVersion = "143"

	// userAgent is the User-Agent header sent with requests.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
		chromiumMajorVersion + ".0.0.0 Safari/537.36 Edg/" + chromiumFullVersion

	// DefaultVoice is the default voice used for TTS.
	DefaultVoice = "en-US-EmmaMultilingualNeural"

	// maxTextBytes is the maximum number of bytes per text chunk sent to the service.
	maxTextBytes = 4096

	// audioBitrate is the bitrate of the output audio in bits per second.
	audioBitrate = 48000

	// ticksPerSecond is the number of 100-nanosecond ticks per second.
	ticksPerSecond = 10_000_000
)
