package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestOptionalChecksIgnoreDisabledAndOverriddenContent(t *testing.T) {
	for _, kind := range []string{"jina", "crawl4ai"} {
		t.Run(kind, func(t *testing.T) {
			check := checkJina
			if kind == "crawl4ai" {
				check = checkCrawl4AI
			}
			cfg := &config.Config{
				Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: kind}},
				Sources: []config.SourceConfig{
					{Enabled: true, Content: &config.ContentConfig{Type: "rss-item"}},
					{Enabled: false, Content: &config.ContentConfig{Type: kind}},
				},
			}
			detail, err := check(context.Background(), cfg)
			if err != nil || detail != "not used" {
				t.Fatalf("unused content check = %q, %v", detail, err)
			}
			cfg.Sources = nil
			detail, err = check(context.Background(), cfg)
			if err != nil || detail != "not used" {
				t.Fatalf("no enabled sources = %q, %v", detail, err)
			}
		})
	}
}

func TestCheckLLMOnlyUsesEnabledSourceDependencies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation []string
		screening  *config.ScreeningConfig
		sources    bool
		want       []string
	}{
		{name: "inherited generation and fallback", sources: true, want: []string{"fallback", "primary"}},
		{name: "source overrides defaults", sources: true, generation: []string{"override"}, want: []string{"override"}},
		{name: "screening fallback is checked", sources: true, screening: &config.ScreeningConfig{Enabled: boolPointer(true), Services: []string{"jev", "llm.screen"}}, want: []string{"fallback", "primary", "screen"}},
		{name: "disabled screening is ignored", sources: true, screening: &config.ScreeningConfig{Enabled: boolPointer(false), Services: []string{"llm.unused"}}, want: []string{"fallback", "primary"}},
		{name: "no enabled sources"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/models")
				called = append(called, name)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("request = %s %s, Authorization = %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
			}))
			defer server.Close()
			cfg := &config.Config{
				Defaults: config.DefaultsConfig{LLM: []string{"primary", "fallback"}},
				Services: config.ServicesConfig{LLM: map[string]config.LLMService{}},
				Sources: []config.SourceConfig{
					{Enabled: tc.sources, LLM: tc.generation, Screening: tc.screening},
					{Enabled: false, LLM: []string{"unused"}, Screening: &config.ScreeningConfig{Enabled: boolPointer(true), Services: []string{"llm.unused"}}},
				},
			}
			for _, name := range []string{"primary", "fallback", "override", "screen", "unused"} {
				cfg.Services.LLM[name] = config.LLMService{BaseURL: server.URL + "/" + name, APIKey: "test-key", Model: "test-model", Timeout: "1s"}
			}
			if _, err := checkLLM(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(called, tc.want) {
				t.Fatalf("checked services = %v, want %v", called, tc.want)
			}
		})
	}
}

func TestCheckLLMFallbackFailureIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg := &config.Config{
		Services: config.ServicesConfig{LLM: map[string]config.LLMService{"backup": {BaseURL: server.URL, Timeout: "1s"}}},
		Sources:  []config.SourceConfig{{Enabled: true, LLM: []string{"backup"}}},
	}
	if _, err := checkLLM(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "backup models endpoint returned HTTP 401") {
		t.Fatalf("fallback check error = %v", err)
	}
}

func TestCheckTTSOnlyUsesEnabledProfiles(t *testing.T) {
	for _, override := range []bool{false, true} {
		var calls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodPost || r.Header.Get("Ocp-Apim-Subscription-Key") != "test-key" {
				t.Errorf("unexpected synthesis request")
			}
			_, _ = w.Write([]byte("test-audio"))
		}))
		cfg := &config.Config{
			Defaults: config.DefaultsConfig{Generation: config.GenerationConfig{DialogueProfile: "active"}},
			Services: config.ServicesConfig{TTS: map[string]config.TTSService{"azure": {Endpoint: server.URL, APIKey: "test-key", ConnectTimeout: "1s", ReceiveTimeout: "1s"}}},
			DialogueProfiles: map[string]config.DialogueProfile{
				"active": {Speakers: []config.SpeakerConfig{{ID: "host", Name: "Host", Voice: "azure:test-voice"}}},
				"unused": {Speakers: []config.SpeakerConfig{{Voice: "invalid-voice"}}},
			},
			Sources: []config.SourceConfig{{Enabled: true}, {Enabled: false, Generation: &config.GenerationConfig{DialogueProfile: "unused"}}},
		}
		if override {
			cfg.Defaults.Generation.DialogueProfile = "unused"
			cfg.Sources[0].Generation = &config.GenerationConfig{DialogueProfile: "active"}
		}
		if detail, err := checkTTS(context.Background(), cfg); err != nil || detail != "active voices and synthesis OK" {
			t.Fatalf("TTS check = %q, %v", detail, err)
		}
		if calls != 1 {
			t.Fatalf("synthesis calls = %d, want 1", calls)
		}
		cfg.Sources[0].Enabled = false
		if detail, err := checkTTS(context.Background(), cfg); err != nil || detail != "not used" {
			t.Fatalf("disabled TTS = %q, %v", detail, err)
		}
		server.Close()
	}
}

func TestCheckJevUsageAndResponse(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantError string
		status                    int
	}{
		{name: "valid", status: 200, response: `{"model":"resolved-model","answers":{"check":{"type":"noul","noul":0.9}}}`},
		{name: "provider failure", status: 401, response: "not JSON", wantError: "Jev HTTP 401"},
		{name: "invalid answer", status: 200, response: `{"model":"test","answers":{"check":{"type":"noul","noul":1.1}}}`, wantError: "invalid Jev probability or model"},
		{name: "trailing garbage", status: 200, response: `{"model":"test","answers":{"check":{"type":"noul","noul":0.9}}} garbage`, wantError: "invalid Jev response JSON"},
		{name: "redirect", status: 302, wantError: "Jev HTTP 302"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/systemone" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected Jev request")
				}
				var body struct {
					Model     string `json:"model"`
					Questions map[string]struct {
						Type string `json:"type"`
					} `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "test-model" || body.Questions["check"].Type != "noul" {
					t.Errorf("invalid Jev request body: %v", err)
				}
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			cfg := &config.Config{
				Services: config.ServicesConfig{Jev: config.LLMService{BaseURL: server.URL, APIKey: "test-key", Model: "test-model", Timeout: "1s"}},
				Defaults: config.DefaultsConfig{Screening: config.ScreeningConfig{Enabled: boolPointer(true), Services: []string{"jev"}}},
				Sources:  []config.SourceConfig{{Enabled: false}},
			}
			if detail, err := checkJev(context.Background(), cfg); err != nil || detail != "not used" || calls != 0 {
				t.Fatalf("disabled source Jev check = %q, %v, calls=%d", detail, err, calls)
			}
			cfg.Sources[0].Enabled = true
			cfg.Sources[0].Screening = &config.ScreeningConfig{Enabled: boolPointer(false)}
			if detail, err := checkJev(context.Background(), cfg); err != nil || detail != "not used" || calls != 0 {
				t.Fatalf("disabled screening Jev check = %q, %v, calls=%d", detail, err, calls)
			}
			cfg.Sources[0].Screening = nil
			_, err := checkJev(context.Background(), cfg)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || err.Error() != tc.wantError) {
				t.Fatalf("Jev error = %v, want %q", err, tc.wantError)
			}
			if calls != 1 {
				t.Fatalf("Jev calls = %d, want 1", calls)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestCheckCrawl4AI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/md" {
			t.Errorf("request = %s %s, want POST /md", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer health-token" {
			t.Errorf("Authorization = %q", got)
		}
		var request struct {
			URL    string `json:"url"`
			Filter string `json:"f"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.URL != "https://example.com" || request.Filter != "fit" {
			t.Errorf("request body = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"markdown":"# Example Domain"}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
			BaseURL: "  " + server.URL + "  ", APIToken: "health-token", Proxy: "   ",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
		Sources:  []config.SourceConfig{{Enabled: true}},
	}
	detail, err := checkCrawl4AI(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail != "fit Markdown content OK via direct connection" {
		t.Fatalf("detail = %q", detail)
	}
}

func TestCheckCrawl4AICrawlModeWithSourceOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/crawl" {
			t.Errorf("request = %s %s, want POST /crawl", r.Method, r.URL.Path)
		}
		var request struct {
			URLs []string `json:"urls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.URLs) != 1 || request.URLs[0] != "https://example.com" {
			t.Errorf("request body = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"results":[{"success":true,"html":"<h1>Example Domain</h1>"}]}`))
	}))
	defer server.Close()

	mode := "crawl"
	baseURL := server.URL
	content := &config.ContentConfig{
		Type:     "crawl4ai",
		Crawl4AI: config.Crawl4AIContentConfig{Mode: &mode, BaseURL: &baseURL},
	}
	cfg := &config.Config{Sources: []config.SourceConfig{{Enabled: true, Content: content}}}
	detail, err := checkCrawl4AI(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail != "HTML content OK via direct connection" {
		t.Fatalf("detail = %q", detail)
	}
}

func TestCheckCrawl4AINotUsed(t *testing.T) {
	detail, err := checkCrawl4AI(context.Background(), &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if detail != "not used" {
		t.Fatalf("detail = %q", detail)
	}
}

func TestCheckCrawl4AIServiceContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		response string
		wantErr  string
	}{
		{name: "markdown body", mode: "md", response: `{"success":true,"markdown":"This domain is for use in documentation examples."}`},
		{name: "markdown collapsed body", mode: "md", response: `{"success":true,"markdown":"Thisdomainisforuseindocumentationexamples."}`},
		{name: "markdown mixed whitespace", mode: "md", response: `{"success":true,"markdown":"THIS DOMAIN\nis for\tuse in documentation examples."}`},
		{name: "markdown heading", mode: "md", response: `{"success":true,"markdown":"# Example Domain"}`},
		{name: "markdown failure", mode: "md", response: `{"success":false,"markdown":"# Example Domain"}`, wantErr: "service reported an unsuccessful crawl"},
		{name: "markdown empty", mode: "md", response: `{"success":true,"markdown":" \n\t"}`, wantErr: "response contained empty Markdown"},
		{name: "markdown wrong page", mode: "md", response: `{"success":true,"markdown":"Access denied"}`, wantErr: "Markdown did not contain the example.com title or explanatory text"},
		{name: "html body", mode: "crawl", response: `{"success":true,"results":[{"success":true,"html":"<p>This domain is for use in documentation examples.</p>"}]}`},
		{name: "crawl failure", mode: "crawl", response: `{"success":false}`, wantErr: "service reported an unsuccessful crawl"},
		{name: "crawl missing result", mode: "crawl", response: `{"success":true,"results":[]}`, wantErr: "expected 1 crawl result, got 0"},
		{name: "crawl extra result", mode: "crawl", response: `{"success":true,"results":[{},{}]}`, wantErr: "expected 1 crawl result, got 2"},
		{name: "crawl result failure", mode: "crawl", response: `{"success":true,"results":[{"success":false,"html":"<h1>Example Domain</h1>"}]}`, wantErr: "crawl result reported failure"},
		{name: "html empty", mode: "crawl", response: `{"success":true,"results":[{"success":true,"html":" \n\t"}]}`, wantErr: "response contained empty HTML"},
		{name: "html wrong page", mode: "crawl", response: `{"success":true,"results":[{"success":true,"html":"<h1>Access denied</h1>"}]}`, wantErr: "HTML did not contain the example.com title or explanatory text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			for _, filter := range []string{"fit", "raw"} {
				_, err := checkCrawl4AIService(context.Background(), config.Crawl4AIService{BaseURL: server.URL, Filter: filter}, tc.mode)
				if tc.wantErr == "" {
					if err != nil {
						t.Fatalf("filter=%s: %v", filter, err)
					}
				} else if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("filter=%s: error = %v, want %q", filter, err, tc.wantErr)
				}
			}
		})
	}
}

func TestCheckCrawl4AIIdentifiesFailingSourceAndMode(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/crawl" {
			_, _ = w.Write([]byte(`{"success":true,"results":[{"success":true,"html":"<h1>Example Domain</h1>"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"success":true,"markdown":""}`))
		}
	}))
	defer server.Close()
	mode := "crawl"
	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{BaseURL: server.URL}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
		Sources: []config.SourceConfig{
			{ID: "html-source", Enabled: true, Content: &config.ContentConfig{Type: "crawl4ai", Crawl4AI: config.Crawl4AIContentConfig{Mode: &mode}}},
			{ID: "duplicate-html-source", Enabled: true, Content: &config.ContentConfig{Type: "crawl4ai", Crawl4AI: config.Crawl4AIContentConfig{Mode: &mode}}},
			{ID: "markdown-source", Enabled: true},
		},
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != "source markdown-source mode=md filter=fit: response contained empty Markdown" {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"/crawl", "/md"}) {
		t.Fatalf("request paths = %v", paths)
	}
}

func TestCheckJinaRejectsMissingBaseURL(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "jina"}}, Sources: []config.SourceConfig{{Enabled: true}}}
	_, err := checkJina(context.Background(), cfg)
	if err == nil || err.Error() != "base_url is not configured" {
		t.Fatalf("checkJina() error = %v", err)
	}
}

func TestCheckCrawl4AIRejectsMissingBaseURL(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}}, Sources: []config.SourceConfig{{ID: "test", Enabled: true}}}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != "source test mode=md filter=fit: base_url is not configured" {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}

func TestCheckCrawl4AIRejectsUnsupportedMode(t *testing.T) {
	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
			BaseURL: "http://crawl4ai:11235", Mode: "browser",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
		Sources:  []config.SourceConfig{{ID: "test", Enabled: true}},
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != `source test mode=browser filter=fit: unsupported mode "browser"` {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}

func TestCheckCrawl4AIReportsHTTPStatusBeforeDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("not JSON"))
	}))
	defer server.Close()
	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{BaseURL: server.URL}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
		Sources:  []config.SourceConfig{{ID: "test", Enabled: true}},
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != "source test mode=md filter=fit: HTTP 502" {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}

func TestCheckCrawl4AIRejectsInvalidProxy(t *testing.T) {
	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
			BaseURL: "http://crawl4ai:11235", Proxy: "not-a-url",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
		Sources:  []config.SourceConfig{{ID: "test", Enabled: true}},
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != `source test mode=md filter=fit: invalid proxy URL "not-a-url"` {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}
