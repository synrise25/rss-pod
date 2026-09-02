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
			BaseURL: server.URL, APIToken: "health-token", Format: "markdown",
		}}},
		Defaults: config.DefaultsConfig{Content: config.ContentConfig{Type: "crawl4ai"}},
	}
	detail, err := checkCrawl4AI(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail != "markdown/fit content OK via direct connection" {
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
