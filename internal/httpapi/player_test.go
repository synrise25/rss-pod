package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestPlayerNoticeDisabled(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	(&playerServer{}).notice(response, httptest.NewRequest(http.MethodGet, "/api/v1/player/notice", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
}

func TestPlayerNoticeMissingFile(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	server := &playerServer{noticeFile: filepath.Join(t.TempDir(), "missing.md")}
	server.notice(response, httptest.NewRequest(http.MethodGet, "/api/v1/player/notice", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestPlayerNoticeRendersMarkdownAndReloadsFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notice.md")
	if err := os.WriteFile(path, []byte("**First**\n\n- one\n- two\n\n[bad](javascript:alert(1))\n\n<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &playerServer{noticeFile: path}

	response := httptest.NewRecorder()
	server.notice(response, httptest.NewRequest(http.MethodGet, "/api/v1/player/notice", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "<strong>First</strong>") || !strings.Contains(body, "<li>one</li>") {
		t.Fatalf("Markdown was not rendered: %s", body)
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "javascript:") {
		t.Fatalf("unsafe Markdown was rendered: %s", body)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}

	if err := os.WriteFile(path, []byte("Updated"), 0o600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.notice(response, httptest.NewRequest(http.MethodGet, "/api/v1/player/notice", nil))
	if !strings.Contains(response.Body.String(), "Updated") {
		t.Fatalf("updated file was not read: %s", response.Body.String())
	}
}

func TestPlayerNoticeRejectsOversizedFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notice.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxNoticeBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	(&playerServer{noticeFile: path}).notice(response, httptest.NewRequest(http.MethodGet, "/api/v1/player/notice", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestListPlayerSourcesReturnsOnlyPublicFields(t *testing.T) {
	t.Parallel()

	server := newPlayerServer(&config.Config{Sources: []config.SourceConfig{
		{ID: "enabled", Name: "Enabled source", Enabled: true, Feed: config.FeedConfig{URL: "https://private.example/feed"}},
		{ID: "disabled", Name: "Disabled source", Enabled: false},
	}}, nil)
	response := httptest.NewRecorder()
	server.listSources(response, httptest.NewRequest("GET", "/api/v1/player/sources", nil))

	body := response.Body.String()
	if !strings.Contains(body, `"id":"enabled"`) || !strings.Contains(body, `"name":"Enabled source"`) {
		t.Fatalf("response does not contain enabled source: %s", body)
	}
	if strings.Contains(body, "disabled") || strings.Contains(body, "private.example") || strings.Contains(body, "feed") {
		t.Fatalf("response exposes non-public source configuration: %s", body)
	}
}

func TestPlayerMuxDoesNotExposeManagementRoutes(t *testing.T) {
	t.Parallel()

	mux := newPlayerMux(&playerServer{})
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/healthz"},
		{method: http.MethodGet, path: "/readyz"},
		{method: http.MethodGet, path: "/api/v1/sources"},
		{method: http.MethodPost, path: "/api/v1/sources/example/poll"},
		{method: http.MethodGet, path: "/api/v1/sources/example/podcast.xml"},
		{method: http.MethodPost, path: "/api/v1/episodes/example/retry"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", response.Code)
			}
		})
	}
}

func TestManagementMuxDoesNotExposePlayerRoutes(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	newManagementMux(&Server{}).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/player/sources", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestParseOptionalRFC3339(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	parsed, ok := parseOptionalRFC3339(w, "2026-08-23T00:00:00+08:00", "since")
	if !ok {
		t.Fatalf("expected valid timestamp, response body: %s", w.Body.String())
	}
	want := time.Date(2026, time.August, 23, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if !parsed.Equal(want) {
		t.Fatalf("parsed time = %s, want %s", parsed, want)
	}
}

func TestParseOptionalRFC3339RejectsInvalidValue(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	parsed, ok := parseOptionalRFC3339(w, "today", "since")
	if ok || parsed != nil {
		t.Fatal("expected invalid timestamp to be rejected")
	}
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
