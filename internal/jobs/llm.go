package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

// callLLMCompletion shares transport and error classification across script generation and screening.
func callLLMCompletion(ctx context.Context, service config.LLMService, systemPrompt, userPrompt string, temperature float64) (string, bool, error) {
	timeout, err := time.ParseDuration(service.Timeout)
	if err != nil {
		return "", false, fmt.Errorf("invalid timeout: %w", err)
	}
	requestBody := map[string]any{
		"model": service.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": temperature,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(service.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+service.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client, err := contentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return "", false, fmt.Errorf("invalid proxy: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", true, err
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 500))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return "", true, fmt.Errorf("decode completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return "", true, fmt.Errorf("completion contains no choices")
	}
	return completion.Choices[0].Message.Content, false, nil
}
