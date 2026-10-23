// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ListVoices fetches the list of available voices from the Edge TTS service.
func ListVoices(ctx context.Context, proxy string) ([]Voice, error) {
	client := &http.Client{}
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("edgetts: invalid proxy URL: %w", err)
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, voiceListURL, nil)
	if err != nil {
		return nil, fmt.Errorf("edgetts: creating voice list request: %w", err)
	}

	req.Header.Set("Authority", "speech.platform.bing.com")
	req.Header.Set("Sec-CH-UA", `"Microsoft Edge";v="`+chromiumMajorVersion+`", "Chromium";v="`+chromiumMajorVersion+`", "Not-A.Brand";v="99"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("edgetts: fetching voice list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("edgetts: voice list returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("edgetts: reading voice list response: %w", err)
	}

	var voices []Voice
	if err := json.Unmarshal(body, &voices); err != nil {
		return nil, fmt.Errorf("edgetts: parsing voice list: %w", err)
	}
	return voices, nil
}
