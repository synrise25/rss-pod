package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/synrise25/rss-pod/internal/config"
)

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
			BaseURL: server.URL, APIToken: "health-token", Proxy: "   ",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
	}
	detail, err := checkCrawl4AI(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail != "fit Markdown content OK via direct connection" {
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

func TestCheckCrawl4AIRejectsMissingBaseURL(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}}}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != "base_url is not configured" {
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
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != "HTTP 502" {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}

func TestCheckCrawl4AIRejectsInvalidProxy(t *testing.T) {
	cfg := &config.Config{
		Services: config.ServicesConfig{Content: config.ContentServices{Crawl4AI: config.Crawl4AIService{
			BaseURL: "http://crawl4ai:11235", Proxy: "not-a-url",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
	}
	_, err := checkCrawl4AI(context.Background(), cfg)
	if err == nil || err.Error() != `invalid proxy URL "not-a-url"` {
		t.Fatalf("checkCrawl4AI() error = %v", err)
	}
}
