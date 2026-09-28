package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestJevChain(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"allow", `{"model":"resolved-version","answers":{"skip":{"type":"noul","noul":0.69}}}`, "allow", 200},
		{"threshold", `{"model":"resolved-version","answers":{"skip":{"type":"noul","noul":0.7}}}`, "skip", 200},
		{"zero", `{"model":"resolved-version","answers":{"skip":{"type":"noul","noul":0}}}`, "allow", 200},
		{"missing probability", `{"model":"v","answers":{"skip":{"type":"noul"}}}`, "fallback", 200},
		{"wrong type", `{"model":"v","answers":{"skip":{"type":"choice","noul":0.9}}}`, "fallback", 200},
		{"outside range", `{"model":"v","answers":{"skip":{"type":"noul","noul":1.1}}}`, "fallback", 200},
		{"missing model", `{"answers":{"skip":{"type":"noul","noul":0.9}}}`, "fallback", 200},
		{"malformed", `broken`, "fallback", 200},
		{"too long", `{"detail":{"error_type":"max_tokens_exceeded"}}`, "fallback", 400},
		{"auth", `private upstream error`, "fallback", 401},
		{"rate limit", ``, "fallback", 429},
		{"overload", ``, "fallback", 529},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				writeScreeningCompletion(w, `{"decision":"allow","reason":"LLM fallback"}`)
			}))
			defer backup.Close()
			jev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/systemone" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("incorrect endpoint/auth")
				}
				var body struct {
					State     string
					Questions map[string]struct{ Type, Instructions string }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.State == "" || body.Questions["skip"].Type != "noul" || !strings.Contains(body.Questions["skip"].Instructions, "custom-policy") || strings.Contains(body.Questions["skip"].Instructions, "只返回 JSON") {
					t.Error("incorrect input or policy")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer jev.Close()
			cfg := screeningTestConfig(backup.URL)
			cfg.Defaults.Screening.Services = []string{"jev", "llm.cheap"}
			policy := "custom-policy"
			cfg.Defaults.Screening.Instructions = &policy
			cfg.Services.Jev = config.LLMService{BaseURL: jev.URL, APIKey: "secret", Model: "configured-version", Timeout: "1s"}
			input := makeScreeningInput(cfg, cfg.Sources[0], []llmDocument{{Content: "full content"}})
			result, err := screenContent(context.Background(), cfg, input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "fallback" {
				if calls != 1 || result.Service != "llm.cheap" || result.SkipProbability != nil {
					t.Fatalf("fallback=%+v calls=%d", result, calls)
				}
			} else {
				if calls != 0 || result.Decision != tc.want || result.Model != "resolved-version" || result.SkipProbability == nil || !strings.Contains(result.Reason, "70.0%") {
					t.Fatalf("result=%+v calls=%d", result, calls)
				}
			}
		})
	}
}

func TestJevCacheAndCancellation(t *testing.T) {
	cfg := screeningTestConfig("http://localhost")
	cfg.Defaults.Screening.Services = []string{"jev", "llm.cheap"}
	cfg.Services.Jev = config.LLMService{BaseURL: "http://localhost", Model: "v1", Timeout: "1s"}
	input := makeScreeningInput(cfg, cfg.Sources[0], nil)
	cfg.Services.Jev.APIKey = "rotated"
	if input.Hash != makeScreeningInput(cfg, cfg.Sources[0], nil).Hash {
		t.Fatal("key changed hash")
	}
	threshold := 0.8
	cfg.Defaults.Screening.Jev.SkipThreshold = &threshold
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], nil).Hash {
		t.Fatal("threshold missing from hash")
	}
	cfg.Defaults.Screening.Jev.SkipThreshold = nil
	cfg.Services.Jev.Model = "v2"
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], nil).Hash {
		t.Fatal("model missing from hash")
	}
	cfg.Services.Jev.Model = "v1"
	cfg.Defaults.Screening.Services = []string{"llm.cheap", "jev"}
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], nil).Hash {
		t.Fatal("order missing from hash")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := screenContent(ctx, cfg, input); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestJevFailureNeverAllows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); fmt.Fprint(w, "secret") }))
	defer server.Close()
	cfg := screeningTestConfig(server.URL)
	cfg.Defaults.Screening.Services = []string{"jev"}
	cfg.Services.Jev = config.LLMService{BaseURL: server.URL, Model: "v", Timeout: "1s"}
	result, err := screenContent(context.Background(), cfg, makeScreeningInput(cfg, cfg.Sources[0], nil))
	if err == nil || result.Decision != "" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestJevTimeoutFallsBack(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(50 * time.Millisecond):
		}
	}))
	defer slow.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeScreeningCompletion(w, `{"decision":"skip","reason":"fallback"}`)
	}))
	defer backup.Close()
	cfg := screeningTestConfig(backup.URL)
	cfg.Defaults.Screening.Services = []string{"jev", "llm.cheap"}
	cfg.Services.Jev = config.LLMService{BaseURL: slow.URL, Model: "v", Timeout: "10ms"}
	result, err := screenContent(context.Background(), cfg, makeScreeningInput(cfg, cfg.Sources[0], nil))
	if err != nil || result.Service != "llm.cheap" || result.Decision != "skip" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExplicitLLMChainFallsThroughPermanentError(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeScreeningCompletion(w, `{"decision":"allow","reason":"fallback"}`)
	}))
	defer second.Close()
	cfg := screeningTestConfig(first.URL)
	cfg.Services.LLM["backup"] = config.LLMService{BaseURL: second.URL, Model: "b", Timeout: "1s"}
	cfg.Defaults.Screening.Services = []string{"llm.cheap", "llm.backup"}
	result, err := screenContent(context.Background(), cfg, makeScreeningInput(cfg, cfg.Sources[0], nil))
	if err != nil || result.Service != "llm.backup" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
