package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/database"
)

const testAdminSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
const testAdminOrigin = "https://podcasts.example.com"

func TestTOTPRFC6238(t *testing.T) {
	// RFC 6238 Appendix B SHA-1 vectors, truncated to the supported six digits.
	for _, tc := range []struct {
		timestamp int64
		code      string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		if got := totpCode([]byte("12345678901234567890"), tc.timestamp/30); got != tc.code {
			t.Fatalf("timestamp %d: %s, want %s", tc.timestamp, got, tc.code)
		}
	}
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	for _, offset := range []int64{-1, 0, 1} {
		step := now.Unix()/30 + offset
		if got := matchTOTPStep(secret, totpCode(secret, step), now); got != step {
			t.Fatalf("skew %d rejected", offset)
		}
	}
	for _, code := range []string{"", "12345", "1234567", "abcdef", totpCode(secret, now.Unix()/30-2)} {
		if matchTOTPStep(secret, code, now) != -1 {
			t.Fatalf("accepted invalid code %q", code)
		}
	}
}

func TestAdminDisabledAndGuard(t *testing.T) {
	mux := newPlayerMux(&playerServer{})
	for _, path := range []string{"/admin", "/admin/en", "/admin/zh-cn", "/admin/", "/admin/en/", "/admin/zh-cn/", "/api/v1/admin/session", "/api/v1/admin/episodes"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 404 {
			t.Fatalf("disabled %s: %d", path, rr.Code)
		}
	}
	s := newAdminServer(config.AdminConfig{TOTPSecret: testAdminSecret}, nil, &playerServer{})
	mux = newPlayerMux(&playerServer{}, s)
	for _, tc := range []struct {
		method, path, origin string
		status               int
	}{
		{"GET", "/admin", "", 200}, {"GET", "/admin/en", "", 200}, {"GET", "/admin/zh-cn", "", 200},
		{"GET", "/api/v1/admin/episodes", "", 401}, {"GET", "/api/v1/admin/session", "", 401},
		{"POST", "/api/v1/admin/login", "", 403}, {"POST", "/api/v1/admin/login", "https://evil.example", 403},
		{"PATCH", "/api/v1/admin/episodes/invalid/visibility", testAdminOrigin, 401},
	} {
		req := httptest.NewRequest(tc.method, testAdminOrigin+tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != tc.status {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, rr.Code, tc.status)
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("admin response must not be cached")
		}
	}
}

func TestAdminLoginContentType(t *testing.T) {
	s := &adminServer{}
	for _, tc := range []struct {
		contentType string
		status      int
	}{
		{"application/json", http.StatusBadRequest},
		{"application/json; charset=utf-8", http.StatusBadRequest},
		{"Application/JSON; Charset=UTF-8", http.StatusBadRequest},
		{"", http.StatusUnsupportedMediaType},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"application/json; charset", http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			// Invalid JSON reaches body validation only for supported media types.
			r := httptest.NewRequest("POST", "/api/v1/admin/login", strings.NewReader("{"))
			r.Header.Set("Content-Type", tc.contentType)
			rr := httptest.NewRecorder()
			s.login(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status %d, want %d", rr.Code, tc.status)
			}
		})
	}
}

// The optional URL must point to a disposable test database. Every test uses an
// isolated schema; no real configuration or credentials are loaded.
func adminTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("RSS_POD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RSS_POD_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "admin_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		root.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		root.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Schema migration must remain safe to rerun on an existing deployment.
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestAdminSessionAndVisibilityIntegration(t *testing.T) {
	pool := adminTestPool(t)
	cfg := &config.Config{Admin: config.AdminConfig{TOTPSecret: testAdminSecret}, Sources: []config.SourceConfig{{ID: "test", Name: "Test"}}}
	cfg.Defaults.Podcast.MaxAge = "240h"
	player := newPlayerServer(cfg, pool)
	admin := newAdminServer(cfg.Admin, pool, player)
	mux := newPlayerMux(player, admin)
	var cookie *http.Cookie
	var csrf string
	request := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, testAdminOrigin+path, strings.NewReader(body))
		r.Header.Set("Origin", testAdminOrigin)
		r.Header.Set("Content-Type", "application/json")
		if authorized && cookie != nil {
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, r)
		return rr
	}
	assertStatus := func(rr *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rr.Code != want {
			t.Fatalf("status %d want %d: %s", rr.Code, want, rr.Body.String())
		}
	}
	loginBody := fmt.Sprintf(`{"code":%q}`, totpCode(admin.secret, time.Now().Unix()/30))
	rr := request("POST", "/api/v1/admin/login", loginBody, false)
	assertStatus(rr, 200)
	cookie = rr.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 1800 {
		t.Fatalf("unsafe session cookie: %#v", cookie)
	}
	var payload map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	csrf = payload["csrf_token"]
	assertStatus(request("POST", "/api/v1/admin/login", loginBody, false), 401)
	// A second server instance shares replay protection and sessions.
	replica := newAdminServer(cfg.Admin, pool, player)
	replicaMux := newPlayerMux(player, replica)
	req := httptest.NewRequest("GET", "/api/v1/admin/session", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	replicaMux.ServeHTTP(rr, req)
	assertStatus(rr, 200)
	ctx := context.Background()
	id := uuid.NewString()
	_, err := pool.Exec(ctx, `WITH f AS (INSERT INTO feed_items (source_id,external_id,title,content) VALUES ('test','guid','Episode','original content') RETURNING id)
 INSERT INTO episodes (id,source_id,feed_item_id,title,status,audio_url,published_at) SELECT $1,'test',id,'Episode','published','https://media.example.com/test.mp3',now() FROM f`, id)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/episodes/" + id + "/visibility"
	assertStatus(request("PATCH", path, `{"hidden":true}`, false), 401)
	saved := csrf
	csrf = "wrong"
	assertStatus(request("PATCH", path, `{"hidden":true}`, true), 403)
	csrf = saved
	assertStatus(request("PATCH", path, `{}`, true), 400)
	assertStatus(request("PATCH", path, `{"hidden":"yes"}`, true), 400)
	assertStatus(request("PATCH", "/api/v1/admin/episodes/bad/visibility", `{"hidden":true}`, true), 400)
	assertStatus(request("PATCH", path, `{"hidden":true}`, true), 200)
	assertStatus(request("PATCH", path, `{"hidden":true}`, true), 200)
	rr = request("GET", "/api/v1/player/episodes?include_hidden=true", "", false)
	assertStatus(rr, 200)
	if strings.Contains(rr.Body.String(), id) {
		t.Fatal("public player leaked hidden episode")
	}
	rr = request("GET", "/api/v1/admin/episodes", "", true)
	assertStatus(rr, 200)
	if !strings.Contains(rr.Body.String(), id) || !strings.Contains(rr.Body.String(), `"hidden":true`) {
		t.Fatalf("admin cannot restore hidden episode: %s", rr.Body.String())
	}
	management := newManagementMux(&Server{config: cfg, pool: pool})
	feedRequest := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		management.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/sources/test/podcast.xml", nil))
		return rr
	}
	rr = feedRequest()
	assertStatus(rr, 200)
	if strings.Contains(rr.Body.String(), id) {
		t.Fatal("podcast feed leaked hidden episode")
	}
	var content string
	if err := pool.QueryRow(ctx, `SELECT f.content FROM feed_items f JOIN episodes e ON e.feed_item_id=f.id WHERE e.id=$1`, id).Scan(&content); err != nil || content != "original content" {
		t.Fatalf("hiding removed content: %q %v", content, err)
	}
	assertStatus(request("PATCH", path, `{"hidden":false}`, true), 200)
	rr = request("GET", "/api/v1/player/episodes", "", false)
	assertStatus(rr, 200)
	if !strings.Contains(rr.Body.String(), id) {
		t.Fatal("restored episode missing")
	}
	rr = feedRequest()
	assertStatus(rr, 200)
	if !strings.Contains(rr.Body.String(), id) {
		t.Fatal("restored feed item missing")
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_sessions SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(request("GET", "/api/v1/admin/session", "", true), 401)
	if _, err := pool.Exec(ctx, `UPDATE admin_sessions SET expires_at=now()+interval '30 minutes'`); err != nil {
		t.Fatal(err)
	}
	// A new environment secret immediately rejects sessions from the old key.
	rotated := newAdminServer(config.AdminConfig{TOTPSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"}, pool, player)
	rotatedMux := newPlayerMux(player, rotated)
	req = httptest.NewRequest("GET", "/api/v1/admin/session", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	rotatedMux.ServeHTTP(rr, req)
	assertStatus(rr, 401)
	assertStatus(request("POST", "/api/v1/admin/logout", "", true), 204)
	assertStatus(request("GET", "/api/v1/admin/session", "", true), 401)
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("logout did not revoke session: %d %v", sessions, err)
	}
}

func TestAdminRateLimitAndReplayIntegration(t *testing.T) {
	pool := adminTestPool(t)
	s := newAdminServer(config.AdminConfig{TOTPSecret: testAdminSecret}, pool, &playerServer{})
	login := func(code string) int {
		r := httptest.NewRequest("POST", "/api/v1/admin/login", strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
		r.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.login(rr, r)
		return rr.Code
	}
	for range 5 {
		if got := login("abcdef"); got != 401 {
			t.Fatalf("invalid code: %d", got)
		}
	}
	if got := login(totpCode(s.secret, time.Now().Unix()/30)); got != 429 {
		t.Fatalf("rate limit: %d", got)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE admin_auth_state SET window_started=now()-interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	code := totpCode(s.secret, time.Now().Unix()/30)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Go(func() { results <- login(code) })
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 401 {
			t.Fatalf("concurrent login: %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("same code accepted %d times", success)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE admin_sessions SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	// Replay state is durable even when all sessions expire.
	if got := login(code); got != 401 {
		t.Fatalf("replay after expiry: %d", got)
	}
}

func TestAdminAutomaticOriginAndCookieSecurity(t *testing.T) {
	s := newAdminServer(config.AdminConfig{TOTPSecret: testAdminSecret}, nil, &playerServer{})
	for _, tc := range []struct {
		name, host, origin, fetchSite string
		status                        int
		secure                        bool
	}{
		{"public site", "example.com", "https://example.com", "same-origin", 204, true},
		{"another domain without configuration", "podcasts.example.org", "https://podcasts.example.org", "", 204, true},
		{"custom port", "example.com:8443", "https://example.com:8443", "", 204, true},
		{"localhost HTTP", "localhost:8080", "http://localhost:8080", "", 204, false},
		{"loopback HTTP", "127.0.0.1:8080", "http://127.0.0.1:8080", "", 204, false},
		{"IPv6 HTTP", "[::1]:8080", "http://[::1]:8080", "", 204, false},
		{"localhost HTTPS", "localhost:8080", "https://localhost:8080", "", 204, true},
		{"remote HTTP", "example.com", "http://example.com", "", 403, false},
		{"other site", "example.com", "https://evil.example", "cross-site", 403, false},
		{"different port", "example.com:8443", "https://example.com", "", 403, false},
		{"missing origin", "example.com", "", "", 403, false},
		{"opaque origin", "example.com", "null", "", 403, false},
		{"origin path", "example.com", "https://example.com/admin", "", 403, false},
		{"untrusted forwarded host", "backend:8080", "https://example.com", "", 403, false},
		{"cross-site fetch metadata", "example.com", "https://example.com", "cross-site", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate TLS termination: the backend connection itself is plain HTTP.
			r := httptest.NewRequest("POST", "http://backend/api/v1/admin/login", nil)
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			r.Header.Set("X-Forwarded-Host", "example.com")
			r.Header.Set("X-Forwarded-Proto", "http")
			rr := httptest.NewRecorder()
			s.guard(false, func(w http.ResponseWriter, r *http.Request) {
				s.setCookie(w, r, "test-token", 1800)
				w.WriteHeader(204)
			})(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 204 {
				cookie := rr.Result().Cookies()[0]
				if cookie.Secure != tc.secure {
					t.Fatalf("Secure=%v want=%v", cookie.Secure, tc.secure)
				}
			} else if len(rr.Result().Cookies()) != 0 {
				t.Fatal("rejected request issued a cookie")
			}
		})
	}
}

func TestAdminTrailingSlashRedirects(t *testing.T) {
	admin := newAdminServer(config.AdminConfig{TOTPSecret: testAdminSecret}, nil, &playerServer{})
	mux := newPlayerMux(&playerServer{}, admin)
	for _, path := range []string{"/admin", "/admin/en", "/admin/zh-cn"} {
		for _, query := range []string{"", "?", "?source_id=a%2Fb&demo=1"} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest(method, path+"/"+query, nil))
				if rr.Code != http.StatusPermanentRedirect || rr.Header().Get("Location") != path+query {
					t.Fatalf("%s %s: status=%d location=%q", method, path+"/"+query, rr.Code, rr.Header().Get("Location"))
				}
				canonical := httptest.NewRecorder()
				mux.ServeHTTP(canonical, httptest.NewRequest(method, rr.Header().Get("Location"), nil))
				if canonical.Code != http.StatusOK {
					t.Fatalf("redirect target: %d", canonical.Code)
				}
			}
		}
	}
	for _, path := range []string{"/admin/unknown", "/admin/en/unknown", "/admin/zh-cn/unknown"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d, want 404", path, rr.Code)
		}
	}
}
