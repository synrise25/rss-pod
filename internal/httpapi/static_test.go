package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlayerWebHandlerServesEmbeddedUI(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	playerWebHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Commute Podcasts") {
		t.Fatal("embedded index does not contain the player title")
	}
	if language := response.Header().Get("Content-Language"); language != "en" {
		t.Fatalf("Content-Language = %q, want en", language)
	}
}

func TestPlayerWebHandlerServesChineseRoute(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/zh-cn", nil)
	response := httptest.NewRecorder()
	playerWebHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if !strings.Contains(response.Body.String(), `src="/app.js"`) {
		t.Fatal("Chinese route does not serve the player application")
	}
	if values, exists := response.Header()["Content-Language"]; exists {
		t.Fatalf("Content-Language = %q, want header omitted for the English HTML shell", values)
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
