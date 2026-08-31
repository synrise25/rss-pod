package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/config"
)

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
