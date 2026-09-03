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

	for _, invalid := range []string{"not-a-url", "://broken"} {
		if _, err := contentHTTPClient(invalid, time.Second); err == nil {
			t.Errorf("contentHTTPClient(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestFetchDerivedRSSCompactsMetadataAndLimitedItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>测试&amp;频道</title>
  <link>https://example.com/channel</link>
  <description><![CDATA[<p>关注 <strong>AI</strong> 的每日动态。</p>]]></description>
  <item><title>空内容</title><link>https://example.com/empty</link></item>
  <item><title>第一篇</title><link>https://example.com/1</link><description>第一篇正文</description></item>
  <item><title>第二篇</title><link>https://example.com/2</link><description>第二篇正文</description></item>
</channel></rss>`))
	}))
	defer server.Close()

	worker := ResolveContentWorker{Config: &config.Config{
		Defaults: config.DefaultsConfig{Limits: config.LimitsConfig{MaxDocumentsPerItem: 1}},
	}}
	documents, err := worker.fetchDerivedRSS(
		context.Background(),
		config.SourceConfig{},
		config.ContentConfig{URL: config.URLMappingConfig{
			From: "item.link", Regex: `^https://example\.com/(?P<id>\d+)$`, Template: server.URL,
		}},
		"https://example.com/42",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 {
		t.Fatalf("document count = %d, want channel metadata plus one limited item", len(documents))
	}
	channel := documents[0]
	if channel.Title != "测试&频道" || channel.SourceURL != "" || channel.Content != "关注 AI 的每日动态。" {
		t.Errorf("channel document = %#v", channel)
	}
	if documents[1].Title != "" || documents[1].SourceURL != "" || documents[1].Content != "第一篇正文" {
		t.Errorf("item document = %#v", documents[1])
	}
}

func TestFetchDerivedRSSDoesNotUseChannelMetadataWithoutUsableItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel>
<title>只有频道信息</title><description>不能代替正文</description>
<item><title>空内容</title></item></channel></rss>`))
	}))
	defer server.Close()

	worker := ResolveContentWorker{Config: &config.Config{
		Defaults: config.DefaultsConfig{Limits: config.LimitsConfig{MaxDocumentsPerItem: 1}},
	}}
	documents, err := worker.fetchDerivedRSS(
		context.Background(),
		config.SourceConfig{},
		config.ContentConfig{URL: config.URLMappingConfig{From: "item.link", Regex: `.*`, Template: server.URL}},
		"https://example.com/42",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("documents = %#v, want none", documents)
	}
}

func TestFetchDerivedRSSCompactsAtomFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>讨论主题</title>
  <subtitle type="html">&lt;p&gt;社区背景&lt;/p&gt;</subtitle>
  <link rel="alternate" href="https://example.com/thread"/>
  <entry>
    <id>comment-1</id><title>作者甲 on 讨论主题</title>
    <link href="https://example.com/thread/1"/>
    <content type="html">&lt;p&gt;第一条评论&lt;/p&gt;</content>
  </entry>
  <entry>
    <id>comment-2</id><title>作者乙 on 讨论主题</title>
    <link href="https://example.com/thread/2"/>
    <summary type="html">&lt;p&gt;第二条摘要&lt;/p&gt;</summary>
  </entry>
</feed>`))
	}))
	defer server.Close()

	worker := ResolveContentWorker{Config: &config.Config{
		Defaults: config.DefaultsConfig{Limits: config.LimitsConfig{MaxDocumentsPerItem: 2}},
	}}
	documents, err := worker.fetchDerivedRSS(
		context.Background(),
		config.SourceConfig{},
		config.ContentConfig{URL: config.URLMappingConfig{From: "item.link", Regex: `.*`, Template: server.URL}},
		"https://example.com/thread",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 3 {
		t.Fatalf("document count = %d, want feed metadata plus two entries", len(documents))
	}
	if got := documents[0]; got.Title != "讨论主题" || got.SourceURL != "" || got.Content != "社区背景" {
		t.Errorf("feed document = %#v", got)
	}
	for index, want := range []string{"第一条评论", "第二条摘要"} {
		got := documents[index+1]
		if got.Title != "" || got.SourceURL != "" || got.Content != want {
			t.Errorf("entry document %d = %#v, want content %q without title/source", index, got, want)
		}
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
					BaseURL: "  " + server.URL + "  ", APIToken: "test-token", Filter: test.filter,
				}},
			}}}
			got, err := worker.fetchCrawl4AI(context.Background(), "https://example.com/article", config.ContentConfig{Type: "crawl4ai"})
			if err != nil {
				t.Fatal(err)
			}
			if got != "# Crawled article" {
				t.Fatalf("fetchCrawl4AI() = %q", got)
			}
		})
	}
}

func TestFetchCrawl4AIUsesSourceServiceOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/md" {
			t.Errorf("request = %s %s, want POST /md", r.Method, r.URL.Path)
		}
		var request struct {
			URL    string `json:"url"`
			Filter string `json:"f"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.URL != "https://example.com/article" || request.Filter != "raw" {
			t.Errorf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"markdown":"# Source override"}`))
	}))
	defer server.Close()

	filter := "raw"
	baseURL := server.URL
	worker := ResolveContentWorker{Config: &config.Config{Services: config.ServicesConfig{
		Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{BaseURL: "https://unused.example.com", Filter: "fit"}},
	}}}
	content := config.ContentConfig{
		Type: "crawl4ai",
		Crawl4AI: config.Crawl4AIContentConfig{
			BaseURL: &baseURL, Filter: &filter,
		},
	}
	got, err := worker.fetchCrawl4AI(context.Background(), "https://example.com/article", content)
	if err != nil {
		t.Fatal(err)
	}
	if got != "# Source override" {
		t.Fatalf("fetchCrawl4AI() = %q", got)
	}
}

func TestFetchV2EXTopicIncludesAllPagesAndReplyThanks(t *testing.T) {
	const page1 = `<!doctype html><html><body>
<h1>一个 V2EX 主题</h1>
<div class="topic_content"><p>原帖第一段</p><p>原帖第二段</p></div>
<a href="?p=1" class="page_current">1</a><a href="?p=2" class="page_normal">2</a>
<div id="r_101" class="cell"><a href="/member/alice">alice</a><span class="no">#1</span><div class="reply_content">第一条回复</div><span><img src="/static/img/heart_20250818.png"> 7</span></div>
<div id="r_102" class="cell"><a href="/member/bob">bob</a><span class="no">#2</span><div class="reply_content">第二条回复 <a href="https://www.v2ex.com/t/12345?p=100">用户链接</a></div></div>
</body></html>`
	const page2 = `<!doctype html><html><body>
<h1>一个 V2EX 主题</h1>
<div id="r_102" class="cell"><a href="/member/bob">bob</a><span class="no">#2</span><div class="reply_content">重复回复</div></div>
<div id="r_103" class="cell"><a href="/member/carol">carol</a><span class="no">#3</span><div class="reply_content">第三条回复</div><span><img alt="❤️"> 2</span></div>
</body></html>`

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/crawl" {
			t.Errorf("path = %q, want /crawl", r.URL.Path)
		}
		var request struct {
			URLs []string `json:"urls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var html string
		switch requests {
		case 1:
			if len(request.URLs) != 1 || request.URLs[0] != "https://www.v2ex.com/t/12345?p=1" {
				t.Errorf("first URLs = %#v", request.URLs)
			}
			html = page1
		case 2:
			if len(request.URLs) != 1 || request.URLs[0] != "https://www.v2ex.com/t/12345?p=2" {
				t.Errorf("second URLs = %#v", request.URLs)
			}
			html = page2
		default:
			t.Errorf("unexpected request %d", requests)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"results": []map[string]any{{"url": request.URLs[0], "success": true, "html": html, "markdown": map[string]string{}}},
		})
	}))
	defer server.Close()

	mode := "crawl"
	worker := ResolveContentWorker{Config: &config.Config{Services: config.ServicesConfig{
		Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{BaseURL: server.URL}},
	}}}
	content := config.ContentConfig{
		Type:      "crawl4ai",
		Crawl4AI:  config.Crawl4AIContentConfig{Mode: &mode},
		Transform: config.ContentTransformConfig{Type: "v2ex-topic"},
	}
	got, err := worker.fetchCrawl4AI(context.Background(), "https://www.v2ex.com/t/12345?p=2#reply3", content)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# 一个 V2EX 主题", "原帖第一段\n原帖第二段", "## 回复（共 3 条）", "### #1 · alice · 感谢 7", "第一条回复", "### #2 · bob", "第二条回复", "### #3 · carol · 感谢 2", "第三条回复"} {
		if !strings.Contains(got, want) {
			t.Errorf("content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "重复回复") || strings.Contains(got, "bob · 感谢") {
		t.Errorf("content retained duplicate or invented thanks:\n%s", got)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestV2EXTransformRejectsNonV2EXURL(t *testing.T) {
	_, _, err := normalizeV2EXTopicURL("https://example.com/t/123")
	if err == nil || !strings.Contains(err.Error(), "not v2ex.com") {
		t.Fatalf("normalizeV2EXTopicURL() error = %v", err)
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
			_, err := worker.fetchCrawl4AI(context.Background(), "https://example.com", config.ContentConfig{Type: "crawl4ai"})
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

func TestFetchCrawl4AIResultsIdentifiesFailedURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"results":[{"success":false,"error_message":"blocked"}]}`))
	}))
	defer server.Close()
	worker := ResolveContentWorker{}
	_, err := worker.fetchCrawl4AIResults(context.Background(), []string{"https://www.v2ex.com/t/123?p=2"}, config.Crawl4AIService{BaseURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "https://www.v2ex.com/t/123?p=2") {
		t.Fatalf("fetchCrawl4AIResults() error = %v", err)
	}
}

func TestFetchCrawl4AIRejectsMissingBaseURL(t *testing.T) {
	worker := ResolveContentWorker{Config: &config.Config{}}
	_, err := worker.fetchCrawl4AI(context.Background(), "https://example.com", config.ContentConfig{Type: "crawl4ai"})
	if err == nil || !strings.Contains(err.Error(), "base_url is not configured") {
		t.Fatalf("fetchCrawl4AI() error = %v", err)
	}
	var permanentErr *permanentError
	if !errors.As(err, &permanentErr) {
		t.Fatalf("fetchCrawl4AI() error is retryable: %v", err)
	}
}

func TestFetchJinaRejectsMissingBaseURL(t *testing.T) {
	worker := ResolveContentWorker{Config: &config.Config{}}
	_, err := worker.fetchJina(context.Background(), "https://example.com", config.ContentConfig{Type: "jina"})
	if err == nil || !strings.Contains(err.Error(), "base_url is not configured") {
		t.Fatalf("fetchJina() error = %v", err)
	}
	var permanentErr *permanentError
	if !errors.As(err, &permanentErr) {
		t.Fatalf("fetchJina() error is retryable: %v", err)
	}
}

func TestFetchCrawl4AIRejectsInvalidProxyAsPermanent(t *testing.T) {
	worker := ResolveContentWorker{Config: &config.Config{Services: config.ServicesConfig{
		Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
			BaseURL: "http://crawl4ai:11235", Proxy: "not-a-url",
		}},
	}}}
	_, err := worker.fetchCrawl4AI(context.Background(), "https://example.com", config.ContentConfig{Type: "crawl4ai"})
	if err == nil || !strings.Contains(err.Error(), "invalid Crawl4AI proxy") {
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

func TestRenderDocumentsReportsTruncation(t *testing.T) {
	documents := []llmDocument{{Position: 0, Title: "长文", SourceURL: "https://example.com", Content: strings.Repeat("字", 120_001)}}
	content, stats := renderDocuments(documents)
	if !stats.Truncated || stats.IncludedDocuments != 1 || stats.InputRunes <= stats.LimitRunes {
		t.Fatalf("stats = %#v", stats)
	}
	if got := len([]rune(content)); got > stats.LimitRunes {
		t.Fatalf("rendered runes = %d, limit = %d", got, stats.LimitRunes)
	}
}

func TestRenderDocumentsOmitsEmptyMetadataLines(t *testing.T) {
	documents := []llmDocument{{Position: 0, Content: "正文"}}
	content, stats := renderDocuments(documents)
	if want := "## 资料 1\n\n正文"; content != want {
		t.Fatalf("renderDocuments() = %q, want %q", content, want)
	}
	if stats.Truncated || stats.IncludedDocuments != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestRenderDocumentsTrimsMetadata(t *testing.T) {
	documents := []llmDocument{{
		Position: 0, Title: "  标题\n", SourceURL: " https://example.com/article \t", Content: "正文",
	}}
	content, _ := renderDocuments(documents)
	if want := "## 资料 1\n标题：标题\n来源：https://example.com/article\n\n正文"; content != want {
		t.Fatalf("renderDocuments() = %q, want %q", content, want)
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
