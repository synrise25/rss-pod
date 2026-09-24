package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/synrise25/rss-pod/internal/config"
)

func screeningTestConfig(endpoint string) *config.Config {
	enabled := true
	return &config.Config{
		Services: config.ServicesConfig{LLM: map[string]config.LLMService{"cheap": {Type: "openai_compatible", BaseURL: endpoint, Model: "test-model", Timeout: "1s"}}},
		Defaults: config.DefaultsConfig{Screening: config.ScreeningConfig{Enabled: &enabled, LLM: []string{"cheap"}}},
		Sources:  []config.SourceConfig{{ID: "test", Name: "Test"}},
	}
}

func writeScreeningCompletion(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
}

func TestScreeningFullInputAndCacheKey(t *testing.T) {
	cfg := screeningTestConfig("http://localhost")
	docs := []llmDocument{{Title: "抽奖", Content: strings.Repeat("参与抽奖\n", 5000) + "末尾实质讨论"}}
	input := makeScreeningInput(cfg, cfg.Sources[0], docs)
	want, _ := renderDocuments(docs)
	if input.User != want || !strings.Contains(input.User, "末尾实质讨论") {
		t.Fatal("screening sampled or changed the script input")
	}
	if input.Hash != makeScreeningInput(cfg, cfg.Sources[0], docs).Hash {
		t.Fatal("unstable hash")
	}
	service := cfg.Services.LLM["cheap"]
	service.APIKey = "rotated"
	cfg.Services.LLM["cheap"] = service
	if input.Hash != makeScreeningInput(cfg, cfg.Sources[0], docs).Hash {
		t.Fatal("credential rotation invalidated editorial decision")
	}
	service.Model = "new-model"
	cfg.Services.LLM["cheap"] = service
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], docs).Hash {
		t.Fatal("model change not detected")
	}
	cfg = screeningTestConfig("http://localhost")
	instructions := "new policy"
	cfg.Defaults.Screening.Instructions = &instructions
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], docs).Hash {
		t.Fatal("policy change not detected")
	}
	cfg = screeningTestConfig("http://localhost")
	docs[0].Content += " changed"
	if input.Hash == makeScreeningInput(cfg, cfg.Sources[0], docs).Hash {
		t.Fatal("content change not detected")
	}
}

func TestScreeningLLMFallbackAndInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		status        int
		want          string
		permanent     bool
	}{
		{name: "allow", content: `{"decision":"allow","reason":"存在实质讨论"}`, want: "cheap"},
		{name: "skip", content: "```json\n{\"decision\":\"skip\",\"reason\":\"主要为发码刷楼\"}\n```", want: "cheap"},
		{name: "malformed", content: `not json`, want: "backup"},
		{name: "missing decision", content: `{"reason":"uncertain"}`, want: "backup"},
		{name: "unknown decision", content: `{"decision":"uncertain","reason":"uncertain"}`, want: "backup"},
		{name: "empty reason", content: `{"decision":"skip","reason":" "}`, want: "backup"},
		{name: "rate limit", status: 429, want: "backup"},
		{name: "server failure", status: 500, want: "backup"},
		{name: "auth failure", status: 401, permanent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Temperature float64
					Messages    []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Temperature != 0 || len(request.Messages) != 2 {
					t.Error("invalid screening request")
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					return
				}
				writeScreeningCompletion(w, tc.content)
			}))
			defer first.Close()
			backupCalls := 0
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls++
				writeScreeningCompletion(w, `{"decision":"allow","reason":"不确定，保留"}`)
			}))
			defer backup.Close()
			cfg := screeningTestConfig(first.URL)
			cfg.Services.LLM["backup"] = config.LLMService{BaseURL: backup.URL, Timeout: "1s"}
			cfg.Defaults.Screening.LLM = append(cfg.Defaults.Screening.LLM, "backup")
			_, service, err := screenWithLLM(context.Background(), cfg, makeScreeningInput(cfg, cfg.Sources[0], nil))
			if tc.permanent {
				var pe *permanentError
				if !errors.As(err, &pe) || backupCalls != 0 {
					t.Fatalf("error=%v backup calls=%d", err, backupCalls)
				}
				return
			}
			if err != nil || service != tc.want {
				t.Fatalf("service=%s err=%v", service, err)
			}
		})
	}
}

func TestScreeningAllInvalidDoesNotAllow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeScreeningCompletion(w, `{"decision":"allow"}`) }))
	defer server.Close()
	cfg := screeningTestConfig(server.URL)
	result, _, err := screenWithLLM(context.Background(), cfg, makeScreeningInput(cfg, cfg.Sources[0], nil))
	if err == nil || result.Decision == "allow" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
