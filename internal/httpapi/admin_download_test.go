package httpapi

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/synrise25/rss-pod/internal/config"
)

func TestAudioDownloadFilename(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"通勤播客", "通勤播客.mp3"},
		{" ../a\\b: \r\n\"test\"? ", "_a_b_ ___test__.mp3"},
		{" . ", "episode-id.mp3"},
		{strings.Repeat("播", 90), strings.Repeat("播", 80) + ".mp3"},
	} {
		if got := audioDownloadFilename(tc.title, "episode-id"); got != tc.want {
			t.Errorf("filename for %q = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestAdminDownloadIntegration(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	const audio = "ID3-test-audio"
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" {
			t.Error("admin credentials forwarded to media host")
		}
		if r.URL.Path == "/missing.mp3" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(audio))
	}))
	defer media.Close()
	admin := newAdminServer(config.AdminConfig{TOTPSecret: testAdminSecret}, pool, &playerServer{})
	mux := newPlayerMux(&playerServer{}, admin)
	token := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO admin_sessions(token_hash,key_hash,expires_at)
        VALUES($1,$2,now()+interval '30 minutes')`, tokenHash(token), admin.keyHash); err != nil {
		t.Fatal(err)
	}
	insert := func(status, audioURL string, hidden bool) string {
		t.Helper()
		id := uuid.NewString()
		_, err := pool.Exec(ctx, `WITH f AS (
            INSERT INTO feed_items(source_id,external_id,title) VALUES('test',$1,'测试：播客') RETURNING id
        ) INSERT INTO episodes(id,source_id,feed_item_id,title,status,audio_url,hidden_at)
        SELECT $1::uuid,'test',id,'测试：播客',$2,$3,CASE WHEN $4 THEN now() END FROM f`, id, status, audioURL, hidden)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		name, id string
		status   int
	}{
		{"published", insert("published", media.URL+"/audio.mp3", false), http.StatusOK},
		{"hidden", insert("published", media.URL+"/audio.mp3", true), http.StatusOK},
		{"unpublished", insert("failed", media.URL+"/audio.mp3", false), http.StatusNotFound},
		{"no audio", insert("published", "", false), http.StatusNotFound},
		{"missing media", insert("published", media.URL+"/missing.mp3", false), http.StatusBadGateway},
		{"unreachable media", insert("published", ":invalid", false), http.StatusBadGateway},
		{"missing episode", uuid.NewString(), http.StatusNotFound},
		{"invalid ID", "invalid", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/episodes/%s/download", tc.id), nil)
			r.AddCookie(&http.Cookie{Name: adminCookie, Value: token})
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("download must not be cached")
			}
			if tc.status != http.StatusOK {
				if rr.Header().Get("Content-Disposition") != "" {
					t.Fatal("error response offered as an audio download")
				}
				return
			}
			kind, params, err := mime.ParseMediaType(rr.Header().Get("Content-Disposition"))
			if err != nil || kind != "attachment" || params["filename"] != "测试：播客.mp3" {
				t.Fatalf("invalid attachment: %s (%v)", rr.Header().Get("Content-Disposition"), err)
			}
			if rr.Body.String() != audio || rr.Header().Get("Content-Type") != "audio/mpeg" {
				t.Fatalf("incorrect audio response: %s %q", rr.Header().Get("Content-Type"), rr.Body.String())
			}
		})
	}
}
