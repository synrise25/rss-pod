package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

const jevScreeningQuestion = `
应按整体内容是否足以支撑实质播客讨论判断。如果大部分内容都是推广、邀请码互助或拉新，少量零散技术讨论不足以支撑播客，仍应跳过。
问题：是否有充分依据跳过这份资料，不为它生成播客？`

// Explicit chains fall through on provider failures, never on a valid decision.
func screenContent(ctx context.Context, cfg *config.Config, input screeningInput) (screeningResult, error) {
	var failures []string
	for _, ref := range input.Chain {
		if err := ctx.Err(); err != nil {
			return screeningResult{}, err
		}
		var result screeningResult
		var err error
		if ref == "jev" {
			result, err = screenWithJev(ctx, cfg.Services.Jev, input)
		} else if strings.HasPrefix(ref, "llm.") {
			name := strings.TrimPrefix(ref, "llm.")
			result, err = screenWithLLM(ctx, cfg.Services.LLM[name], input)
			if err == nil {
				result.Backend = "llm"
				result.Service = ref
				result.Model = cfg.Services.LLM[name].Model
			}
		} else {
			return screeningResult{}, permanent("unknown screening service %q", ref)
		}
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return screeningResult{}, ctx.Err()
		}
		// Do not include response bodies, endpoints or credentials in errors/logs.
		failures = append(failures, ref+": "+safeScreeningError(ref, err))
		slog.WarnContext(ctx, "screening service failed; trying next configured service", "service", ref, "error", safeScreeningError(ref, err))
	}
	return screeningResult{}, fmt.Errorf("all screening services failed: %s", strings.Join(failures, "; "))
}
func safeScreeningError(ref string, err error) string {
	if ref == "jev" {
		return err.Error()
	}
	return "LLM request failed or returned an invalid decision"
}

func screenWithJev(ctx context.Context, service config.LLMService, input screeningInput) (screeningResult, error) {
	timeout, err := time.ParseDuration(service.Timeout)
	if err != nil || timeout <= 0 {
		return screeningResult{}, fmt.Errorf("invalid Jev timeout")
	}
	payload := map[string]any{"model": service.Model, "state": input.User, "questions": map[string]any{"skip": jevQuestion(input.JevInstructions)}}
	body, err := json.Marshal(payload)
	if err != nil {
		return screeningResult{}, fmt.Errorf("encode Jev request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(service.BaseURL, "/")+"/systemone", bytes.NewReader(body))
	if err != nil {
		return screeningResult{}, fmt.Errorf("invalid Jev endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+service.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client, err := contentHTTPClient(service.Proxy, timeout)
	if err != nil {
		return screeningResult{}, fmt.Errorf("invalid Jev proxy")
	}
	// Never forward the key to a redirect destination.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return screeningResult{}, fmt.Errorf("Jev connection failed or timed out")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return screeningResult{}, fmt.Errorf("read Jev response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Detail struct {
				ErrorType string `json:"error_type"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(data, &failure)
		if resp.StatusCode == 400 && failure.Detail.ErrorType == "max_tokens_exceeded" {
			return screeningResult{}, fmt.Errorf("Jev input exceeds model token limit")
		}
		return screeningResult{}, fmt.Errorf("Jev HTTP %d", resp.StatusCode)
	}
	var response struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return screeningResult{}, fmt.Errorf("invalid Jev response JSON")
	}
	answer, ok := response.Answers["skip"]
	if !ok || answer.Type != "noul" || answer.Noul == nil || *answer.Noul < 0 || *answer.Noul > 1 || strings.TrimSpace(response.Model) == "" {
		return screeningResult{}, fmt.Errorf("invalid Jev probability or model")
	}
	result := screeningResult{Decision: "allow", Backend: "jev", Service: "jev", Model: response.Model, SkipProbability: answer.Noul}
	action := "保留"
	if *answer.Noul >= input.SkipThreshold {
		result.Decision = "skip"
		action = "跳过"
	}
	result.Reason = fmt.Sprintf("Jev 判定%s：跳过概率 %.1f%%，阈值 %.1f%%。", action, *answer.Noul*100, input.SkipThreshold*100)
	return result, nil
}

// Keep the exact question in both the request and cache key.
func jevQuestion(instructions string) map[string]any {
	return map[string]any{"type": "noul", "instructions": instructions, "criteria": map[string]string{
		"true":  "整体缺乏足够实质内容，符合跳过条件。",
		"false": "整体具有足够实质讨论价值，或没有充分依据跳过。",
	}}
}
