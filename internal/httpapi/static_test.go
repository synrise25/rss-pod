package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlayerWebHandlerRedirectsRootByPreferredLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		acceptLanguage string
		wantLocation   string
	}{
		{name: "Chinese", acceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8", wantLocation: "/zh-cn?demo=1"},
		{name: "Traditional Chinese", acceptLanguage: "zh-Hant,en;q=0.8", wantLocation: "/zh-cn?demo=1"},
		{name: "English", acceptLanguage: "en-US,en;q=0.9,zh;q=0.8", wantLocation: "/en?demo=1"},
		{name: "quality", acceptLanguage: "zh-CN;q=0.4,en-US;q=0.9", wantLocation: "/en?demo=1"},
		{name: "fallback", acceptLanguage: "fr-FR", wantLocation: "/en?demo=1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, "/?demo=1", nil)
			request.Header.Set("Accept-Language", test.acceptLanguage)
			response := httptest.NewRecorder()
			playerWebHandler().ServeHTTP(response, request)

			if response.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", response.Code)
			}
			if location := response.Header().Get("Location"); location != test.wantLocation {
				t.Fatalf("Location = %q, want %q", location, test.wantLocation)
			}
			if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
			}
			if vary := response.Header().Get("Vary"); vary != "Accept-Language" {
				t.Fatalf("Vary = %q, want Accept-Language", vary)
			}
		})
	}
}

func TestPlayerWebHandlerServesEnglishRoute(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/en", "/en/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		playerWebHandler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, response.Code)
		}
		if !strings.Contains(response.Body.String(), "Commute Podcasts") {
			t.Fatalf("%s: embedded index does not contain the player title", path)
		}
		if !strings.Contains(response.Body.String(), `href="https://github.com/synrise25/rss-pod"`) {
			t.Fatalf("%s: embedded index does not contain the GitHub repository link", path)
		}
		if language := response.Header().Get("Content-Language"); language != "en" {
			t.Fatalf("%s: Content-Language = %q, want en", path, language)
		}
	}
}

func TestPlayerWebHandlerServesChineseRoute(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/zh-cn", "/zh-cn/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		playerWebHandler().ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, response.Code)
		}
		if !strings.Contains(response.Body.String(), `src="/app.js"`) {
			t.Fatalf("%s: Chinese route does not serve the player application", path)
		}
		if values, exists := response.Header()["Content-Language"]; exists {
			t.Fatalf("%s: Content-Language = %q, want header omitted for the English HTML shell", path, values)
		}
	}
}

func TestPlayerWebHandlerDoesNotCaptureUnknownAPIPaths(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	response := httptest.NewRecorder()
	playerWebHandler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestPlayerWebHandlerServesAppIcon(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/icons/favicon.png", nil)
	response := httptest.NewRecorder()
	playerWebHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", contentType)
	}
	if response.Body.Len() == 0 {
		t.Fatal("embedded favicon is empty")
	}
}

func TestPlayerWebHandlerServesGitHubIcon(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/icons/github.svg", nil)
	response := httptest.NewRecorder()
	playerWebHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/svg+xml" {
		t.Fatalf("Content-Type = %q, want image/svg+xml", contentType)
	}
	if response.Body.Len() == 0 {
		t.Fatal("embedded GitHub icon is empty")
	}
}
