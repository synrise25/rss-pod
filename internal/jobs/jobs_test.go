package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestEpisodeFailureUpdateContextSurvivesParentCancellation(t *testing.T) {
	type contextKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "episode"))
	cancelParent()

	ctx, cancel := episodeFailureUpdateContext(parent)
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("cleanup context inherited cancellation: %v", err)
	}
	if got := ctx.Value(contextKey{}); got != "episode" {
		t.Fatalf("cleanup context value = %v, want episode", got)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > episodeFailureUpdateTimeout {
		t.Fatalf("cleanup context remaining timeout = %s", remaining)
	}
}

func TestGenerateTTSUsesFiveRiverAttempts(t *testing.T) {
	if got := (GenerateTTSArgs{}).InsertOpts().MaxAttempts; got != 5 {
		t.Fatalf("GenerateTTS River max attempts = %d, want 5", got)
	}
}

func TestCleanupDataJob(t *testing.T) {
	args := CleanupDataArgs{}
	if got := args.Kind(); got != "cleanup_data" {
		t.Fatalf("CleanupDataArgs.Kind() = %q", got)
	}
	opts := args.InsertOpts()
	if opts.Queue != "source" || opts.MaxAttempts != 5 {
		t.Fatalf("CleanupDataArgs.InsertOpts() = %#v", opts)
	}

	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	want := time.Date(2026, time.August, 16, 4, 0, 0, 0, time.UTC)
	if got := cleanupCutoff(now); !got.Equal(want) {
		t.Fatalf("cleanupCutoff() = %s, want %s", got, want)
	}
}

func TestContentHTTPClientUsesOnlyExplicitProxy(t *testing.T) {
	direct, err := contentHTTPClient("", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	directTransport := direct.Transport.(*http.Transport)
	if directTransport.Proxy != nil {
		t.Fatal("direct client inherited a proxy function")
	}

	proxied, err := contentHTTPClient("http://127.0.0.1:4090", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := proxied.Transport.(*http.Transport).Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := proxyURL.String(); got != "http://127.0.0.1:4090" {
		t.Fatalf("proxy URL = %q", got)
	}
}

func TestFetchCrawl4AI(t *testing.T) {
	for _, test := range []struct {
		name   string
		filter string
		want   string
	}{
		{name: "default fit", want: "fit"},
		{name: "explicit raw", filter: "raw", want: "raw"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/md" {
					t.Errorf("request = %s %s, want POST /md", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				var request struct {
					URL    string `json:"url"`
					Filter string `json:"f"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if request.URL != "https://example.com/article" || request.Filter != test.want {
					t.Errorf("request body = %#v", request)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"markdown":"# Crawled article"}`))
			}))
			defer server.Close()

			worker := ResolveContentWorker{Config: &config.Config{Services: config.ServicesConfig{
				Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
					BaseURL: server.URL, APIToken: "test-token", Filter: test.filter,
				}},
			}}}
			got, err := worker.fetchCrawl4AI(context.Background(), "https://example.com/article")
			if err != nil {
				t.Fatal(err)
			}
			if got != "# Crawled article" {
				t.Fatalf("fetchCrawl4AI() = %q", got)
			}
		})
	}
}

func TestFetchCrawl4AIErrorClassification(t *testing.T) {
	for _, test := range []struct {
		status        int
		wantPermanent bool
	}{
		{status: http.StatusBadRequest, wantPermanent: true},
		{status: http.StatusTooManyRequests, wantPermanent: false},
		{status: http.StatusInternalServerError, wantPermanent: false},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			worker := ResolveContentWorker{Config: &config.Config{Services: config.ServicesConfig{
				Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{BaseURL: server.URL}},
			}}}
			_, err := worker.fetchCrawl4AI(context.Background(), "https://example.com")
			if err == nil {
				t.Fatal("fetchCrawl4AI() unexpectedly succeeded")
			}
			var permanentErr *permanentError
			if got := errors.As(err, &permanentErr); got != test.wantPermanent {
				t.Fatalf("permanent = %t, want %t (error: %v)", got, test.wantPermanent, err)
			}
		})
	}
}

func TestFetchCrawl4AIRejectsMissingBaseURL(t *testing.T) {
	worker := ResolveContentWorker{Config: &config.Config{}}
	_, err := worker.fetchCrawl4AI(context.Background(), "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "base_url is not configured") {
		t.Fatalf("fetchCrawl4AI() error = %v", err)
	}
	var permanentErr *permanentError
	if !errors.As(err, &permanentErr) {
		t.Fatalf("fetchCrawl4AI() error is retryable: %v", err)
	}
}

func TestCallLLMRetryClassification(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "host"}}
	tests := []struct {
		name          string
		status        int
		response      string
		wantRetryable bool
		wantError     string
	}{
		{
			name:          "unknown speaker is retryable",
			status:        http.StatusOK,
			response:      `{"choices":[{"message":{"content":"{\"title\":\"一期\",\"turns\":[{\"speaker_id\":\"xiaowang\",\"text\":\"内容\"}]}"}}]}`,
			wantRetryable: true,
			wantError:     `unknown speaker "xiaowang"`,
		},
		{
			name:          "malformed completion is retryable",
			status:        http.StatusOK,
			response:      `not json`,
			wantRetryable: true,
			wantError:     "decode completion",
		},
		{
			name:          "bad request is permanent",
			status:        http.StatusBadRequest,
			response:      `{"error":"invalid model"}`,
			wantRetryable: false,
			wantError:     "HTTP 400",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			service := config.LLMService{
				BaseURL: server.URL,
				APIKey:  "test-key",
				Model:   "test-model",
				Timeout: "1s",
			}
			_, _, retryable, err := callLLM(context.Background(), service, "system", "user", speakers)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("callLLM() error = %v, want containing %q", err, test.wantError)
			}
			if retryable != test.wantRetryable {
				t.Fatalf("callLLM() retryable = %t, want %t", retryable, test.wantRetryable)
			}
		})
	}
}

func TestDueSourcesAtOnlyMatchesCurrentMinute(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Schedule: config.ScheduleConfig{Timezone: "Asia/Shanghai"},
		},
		Sources: []config.SourceConfig{
			{ID: "due", Enabled: true, Schedule: config.ScheduleConfig{Cron: "50 21 * * *"}},
			{ID: "disabled", Enabled: false, Schedule: config.ScheduleConfig{Cron: "50 21 * * *"}},
			{ID: "other-minute", Enabled: true, Schedule: config.ScheduleConfig{Cron: "49 21 * * *"}},
		},
	}

	now := time.Date(2026, time.August, 23, 13, 50, 42, 0, time.UTC)
	due, err := dueSourcesAt(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "due" {
		t.Fatalf("dueSourcesAt() = %#v, want only source due", due)
	}

	due, err = dueSourcesAt(cfg, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("dueSourcesAt() one minute later = %#v, want no catch-up", due)
	}
}

func TestDueSourcesAtRejectsInvalidCron(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Schedule: config.ScheduleConfig{Timezone: "UTC"},
		},
		Sources: []config.SourceConfig{
			{ID: "broken", Enabled: true, Schedule: config.ScheduleConfig{Cron: "not-a-cron"}},
		},
	}

	if _, err := dueSourcesAt(cfg, time.Now()); err == nil {
		t.Fatal("dueSourcesAt() accepted an invalid cron expression")
	}
}

func TestHTMLToText(t *testing.T) {
	input := `<article><h1>标题</h1><p>第一段 <strong>重点</strong></p><script>不可见</script><p>第二段&amp;结尾</p></article>`
	got := htmlToText(input)
	if strings.Contains(got, "不可见") {
		t.Fatalf("script content was retained: %q", got)
	}
	for _, want := range []string{"标题", "第一段 重点", "第二段&结尾"} {
		if !strings.Contains(got, want) {
			t.Errorf("htmlToText() = %q, missing %q", got, want)
		}
	}
}

func TestRenderPromptParameters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.tmpl")
	if err := os.WriteFile(path, []byte(`为 {{ source.name }} 生成 {{ speaker_count }} 人、约 {{ generation.target_duration }} 的对话。\n{{ speakers }}`), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := config.GenerationConfig{
		TargetDuration: "15m",
	}
	speakers := []config.SpeakerConfig{
		{ID: "host", Name: "主持人", Role: "负责提问和串场", Voice: "voice-a"},
		{ID: "guest", Name: "嘉宾", Role: "负责分析和评论", Voice: "voice-b"},
	}
	got, err := renderPrompt(path, config.SourceConfig{Name: "测试源"}, generation, speakers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"测试源", "2 人", "15m", "- host（主持人）：负责提问和串场", "- guest（嘉宾）：负责分析和评论", `"speaker_id":"host"`} {
		if !strings.Contains(got, want) {
			t.Errorf("renderPrompt() missing %q in %q", want, got)
		}
	}
}

func TestExtractJSONObjectFromFence(t *testing.T) {
	input := "说明如下：\n```json\n{\"title\":\"一期\",\"turns\":[]}\n```\n"
	got := string(extractJSONObject(input))
	want := `{"title":"一期","turns":[]}`
	if got != want {
		t.Fatalf("extractJSONObject() = %q, want %q", got, want)
	}
}

func TestValidateScript(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "host"}, {ID: "guest"}}
	valid := generatedScript{Turns: []generatedTurn{{SpeakerID: "host", Text: "你好"}, {SpeakerID: "guest", Text: "你好"}}}
	if err := validateScript(valid, speakers); err != nil {
		t.Fatalf("valid script rejected: %v", err)
	}
	invalid := generatedScript{Turns: []generatedTurn{{SpeakerID: "other", Text: "你好"}}}
	if err := validateScript(invalid, speakers); err == nil {
		t.Fatal("script with unknown speaker was accepted")
	}
}

func TestDecodeGeneratedScriptRepairsObservedLLMResponse(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "xiaoya"}, {ID: "laowang"}}
	raw := []byte(`{"title":"Codex凌晨重置，用户边骂边蹬","turns":[{"speaker\_id":"xiaoya","text":"第一轮内容不应被改变。"},{"speaker\_id":"laowang","text":"第二轮内容不应被改变。"},{"speer\_id":"laowang","text":"我送你一点甜头，你继续留下来用。"{"speaker\_id":"xiaoya","text":"但问题是，这个甜头好像大家吃得也挺急的。"}]}`)

	script, canonical, repairs, err := decodeGeneratedScript(raw, speakers)
	if err != nil {
		t.Fatalf("decodeGeneratedScript() error = %v", err)
	}
	for _, want := range []string{
		"escaped underscore",
		"turn key speer_id -> speaker_id",
		"missing delimiter between turns",
	} {
		if !containsString(repairs, want) {
			t.Errorf("repairs = %#v, missing %q", repairs, want)
		}
	}
	if len(script.Turns) != 4 {
		t.Fatalf("turn count = %d, want 4", len(script.Turns))
	}
	if got := script.Turns[2]; got.SpeakerID != "laowang" || got.Text != "我送你一点甜头，你继续留下来用。" {
		t.Fatalf("repaired turn = %#v", got)
	}
	if got := script.Turns[3]; got.SpeakerID != "xiaoya" || got.Text != "但问题是，这个甜头好像大家吃得也挺急的。" {
		t.Fatalf("following turn = %#v", got)
	}
	if !json.Valid(canonical) {
		t.Fatalf("canonical response is invalid JSON: %s", canonical)
	}
}

func TestDecodeGeneratedScriptRepairsOnlyUnambiguousSyntax(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "host"}}
	raw := []byte("{\"title\":\"一期\",\"turns\":[{\"speaker_id\":\"host\",\"text\":\"第一行\n第二行\",},],}")

	script, _, repairs, err := decodeGeneratedScript(raw, speakers)
	if err != nil {
		t.Fatalf("decodeGeneratedScript() error = %v", err)
	}
	if script.Turns[0].Text != "第一行\n第二行" {
		t.Fatalf("text = %q, want original newline", script.Turns[0].Text)
	}
	for _, want := range []string{"raw control character in string", "trailing comma"} {
		if !containsString(repairs, want) {
			t.Errorf("repairs = %#v, missing %q", repairs, want)
		}
	}
}

func TestDecodeGeneratedScriptDoesNotGuessContent(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "host"}}
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unknown speaker value",
			raw:  `{"title":"一期","turns":[{"speaker_id":"hots","text":"内容"}]}`,
		},
		{
			name: "unknown turn field",
			raw:  `{"title":"一期","turns":[{"speaker":"host","text":"内容"}]}`,
		},
		{
			name: "ambiguous single quotes",
			raw:  `{'title':'一期','turns':[{'speaker_id':'host','text':'内容'}]}`,
		},
		{
			name: "conflicting typo and correct key",
			raw:  `{"title":"一期","turns":[{"speer_id":"host","speaker_id":"host","text":"内容"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := decodeGeneratedScript([]byte(test.raw), speakers); err == nil {
				t.Fatal("decodeGeneratedScript() unexpectedly accepted ambiguous content")
			}
		})
	}
}

func TestDecodeGeneratedScriptLeavesValidJSONUntouched(t *testing.T) {
	speakers := []config.SpeakerConfig{{ID: "host"}}
	raw := []byte(`{"title":"一期","turns":[{"speaker_id":"host","text":"内容"}]}`)

	script, canonical, repairs, err := decodeGeneratedScript(raw, speakers)
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 0 {
		t.Fatalf("valid JSON repairs = %#v, want none", repairs)
	}
	if script.Title != "一期" || string(canonical) != string(raw) {
		t.Fatalf("script/canonical changed: %#v, %s", script, canonical)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
