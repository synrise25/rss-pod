package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestPlayerEditionDatesIntegration(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	for _, row := range []struct{ title, edition, created, published string }{
		{"overnight", "2026-09-26", "2026-09-27T00:10:00+08:00", "2026-09-27T01:00:00+08:00"},
		{"legacy", "", "2026-09-26T23:40:00+08:00", "2026-09-27T02:00:00+08:00"},
		{"today", "2026-09-27", "2026-09-27T00:00:00+08:00", "2026-09-27T00:30:00+08:00"},
	} {
		_, err := pool.Exec(ctx, `WITH f AS (INSERT INTO feed_items(source_id,external_id) VALUES('test',$2) RETURNING id)
   INSERT INTO episodes(id,source_id,feed_item_id,title,status,audio_url,edition_date,created_at,published_at)
   SELECT $1,'test',id,$2,'published','https://example.com/audio.mp3',NULLIF($3,'')::date,$4::timestamptz,$5::timestamptz FROM f`, uuid.New(), row.title, row.edition, row.created, row.published)
		if err != nil {
			t.Fatal(err)
		}
	}
	server := &playerServer{pool: pool, timezone: "Asia/Shanghai"}
	for _, tc := range []struct {
		query  string
		titles []string
	}{
		{"since=2026-09-26&before=2026-09-27", []string{"legacy", "overnight"}},
		{"since=2026-09-27&before=2026-09-28", []string{"today"}},
		{"since=2026-09-25T16:00:00Z&before=2026-09-26T16:00:00Z", []string{"legacy", "overnight"}},
		{"", []string{"today", "legacy", "overnight"}},
	} {
		response := httptest.NewRecorder()
		server.listEpisodes(response, httptest.NewRequest("GET", "/api/v1/player/episodes?"+tc.query, nil))
		if response.Code != 200 {
			t.Fatalf("%d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Episodes []playerEpisode `json:"episodes"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Episodes) != len(tc.titles) {
			t.Fatalf("%s: %s", tc.query, response.Body.String())
		}
		for i, e := range body.Episodes {
			if e.Title != tc.titles[i] || e.EditionDate == "" {
				t.Fatalf("unexpected episode: %+v", e)
			}
		}
	}
}
