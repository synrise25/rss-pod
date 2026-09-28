package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/synrise25/rss-pod/internal/config"
)

func TestScreeningManagementVisibilityIntegration(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := pool.Exec(ctx, `WITH f AS (INSERT INTO feed_items (source_id,external_id,title) VALUES ('test','skip','推广帖') RETURNING id)
  INSERT INTO episodes (id,source_id,feed_item_id,title,status) SELECT $1,'test',id,'推广帖','skipped' FROM f`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_screenings (episode_id,input_hash,decision,reason,llm_service,model)
  VALUES ($1,'hash','skip','回复主要为领取码','cheap','test-model')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_dialogues (episode_id,profile_id,rate,volume,pitch) VALUES ($1,'test','','','')`, id); err != nil {
		t.Fatal(err)
	}
	server := &Server{pool: pool, config: &config.Config{}}
	mux := newManagementMux(server)
	for _, path := range []string{"/api/v1/episodes?status=skipped", "/api/v1/episodes/" + id} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != 200 || !json.Valid(rr.Body.Bytes()) || !strings.Contains(rr.Body.String(), `"decision":"skip"`) || !strings.Contains(rr.Body.String(), "回复主要为领取码") {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	admin := &adminServer{pool: pool}
	rr := httptest.NewRecorder()
	admin.listSkipped(rr, httptest.NewRequest("GET", "/api/v1/admin/skipped", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "回复主要为领取码") {
		t.Fatalf("admin: %d %s", rr.Code, rr.Body.String())
	}

	// Jev metadata is available to management without masquerading as an LLM.
	if _, err := pool.Exec(ctx, `UPDATE episode_screenings SET backend='jev',service='jev',llm_service='',model='jev-test',skip_probability=0.9 WHERE episode_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	admin.listSkipped(rr, httptest.NewRequest("GET", "/api/v1/admin/skipped", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"service":"jev"`) || !strings.Contains(rr.Body.String(), `"skip_probability":0.9`) {
		t.Fatalf("Jev admin metadata: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/episodes/"+id, nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"backend": "jev"`) && !strings.Contains(rr.Body.String(), `"backend":"jev"`) {
		t.Fatalf("Jev detail metadata: %d %s", rr.Code, rr.Body.String())
	}
	// The public player must never expose screening reasons or skipped content.
	player := newPlayerServer(&config.Config{}, pool)
	rr = httptest.NewRecorder()
	player.listEpisodes(rr, httptest.NewRequest("GET", "/api/v1/player/episodes", nil))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "推广帖") || strings.Contains(rr.Body.String(), "领取码") {
		t.Fatalf("public: %d %s", rr.Code, rr.Body.String())
	}
}
