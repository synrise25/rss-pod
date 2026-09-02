package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/synrise25/rss-pod/internal/config"
)

const maxCrawl4AIResponseBytes = 32 << 20

type crawl4AIResult struct {
	HTML         string `json:"html"`
	Success      bool   `json:"success"`
	ErrorMessage string `json:"error_message"`
}

func (w *ResolveContentWorker) fetchCrawl4AI(ctx context.Context, targetURL string, contentConfig config.ContentConfig) (string, error) {
	service := contentConfig.Crawl4AI.EffectiveService(w.Config.Services.Content.Crawl4AI)
	filter := service.EffectiveFilter()
	mode := service.EffectiveMode()
	switch mode {
	case "md":
		return w.fetchCrawl4AIMarkdown(ctx, targetURL, filter, service)
	case "crawl":
		if strings.EqualFold(strings.TrimSpace(contentConfig.Transform.Type), "v2ex-topic") {
			return w.fetchV2EXTopic(ctx, targetURL, service)
		}
		return "", permanent("Crawl4AI crawl mode requires a supported content transform")
	default:
		return "", permanent("unsupported Crawl4AI mode %q", mode)
	}
}

func (w *ResolveContentWorker) fetchCrawl4AIMarkdown(ctx context.Context, targetURL, filter string, service config.Crawl4AIService) (string, error) {
	client, err := crawl4AIClient(service)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		URL    string `json:"url"`
		Filter string `json:"f"`
	}{URL: targetURL, Filter: filter})
	if err != nil {
		return "", permanent("encode Crawl4AI request: %v", err)
	}
	body, err := doCrawl4AIRequest(ctx, client, service, "/md", payload)
	if err != nil {
		return "", err
	}
	var result struct {
		Markdown string `json:"markdown"`
		Success  bool   `json:"success"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", permanent("decode Crawl4AI response: %v", err)
	}
	if !result.Success {
		return "", fmt.Errorf("Crawl4AI reported an unsuccessful crawl")
	}
	if strings.TrimSpace(result.Markdown) == "" {
		return "", fmt.Errorf("Crawl4AI returned empty content")
	}
	return result.Markdown, nil
}

func (w *ResolveContentWorker) fetchCrawl4AIResults(ctx context.Context, urls []string, service config.Crawl4AIService) ([]crawl4AIResult, error) {
	if len(urls) == 0 {
		return nil, permanent("Crawl4AI crawl request contains no URLs")
	}
	client, err := crawl4AIClient(service)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(struct {
		URLs []string `json:"urls"`
	}{URLs: urls})
	if err != nil {
		return nil, permanent("encode Crawl4AI request: %v", err)
	}
	body, err := doCrawl4AIRequest(ctx, client, service, "/crawl", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Success bool             `json:"success"`
		Results []crawl4AIResult `json:"results"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, permanent("decode Crawl4AI response: %v", err)
	}
	if !response.Success {
		return nil, fmt.Errorf("Crawl4AI reported an unsuccessful crawl")
	}
	if len(response.Results) != len(urls) {
		return nil, fmt.Errorf("Crawl4AI returned %d results for %d URLs", len(response.Results), len(urls))
	}
	for index, result := range response.Results {
		if !result.Success {
			message := strings.TrimSpace(result.ErrorMessage)
			if message == "" {
				message = "unsuccessful crawl"
			}
			return nil, fmt.Errorf("Crawl4AI result %d for %s failed: %s", index, urls[index], message)
		}
	}
	return response.Results, nil
}

func crawl4AIClient(service config.Crawl4AIService) (*http.Client, error) {
	if strings.TrimSpace(service.BaseURL) == "" {
		return nil, permanent("Crawl4AI base_url is not configured")
	}
	timeout, err := service.TimeoutDuration()
	if err != nil {
		return nil, permanent("invalid Crawl4AI timeout: %v", err)
	}
	client, err := contentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return nil, permanent("invalid Crawl4AI proxy: %v", err)
	}
	return client, nil
}

func doCrawl4AIRequest(ctx context.Context, client *http.Client, service config.Crawl4AIService, path string, payload []byte) ([]byte, error) {
	requestURL := strings.TrimRight(strings.TrimSpace(service.BaseURL), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, permanent("create Crawl4AI request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if service.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+service.APIToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Crawl4AI request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCrawl4AIResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Crawl4AI response: %w", err)
	}
	if len(body) > maxCrawl4AIResponseBytes {
		return nil, permanent("Crawl4AI response exceeds %d bytes", maxCrawl4AIResponseBytes)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("Crawl4AI returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, permanent("Crawl4AI returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}
