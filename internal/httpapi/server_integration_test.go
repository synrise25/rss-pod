package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/jobs"
)

func TestRetryEpisodeRecoversOrphanedRetryingIntegration(t *testing.T) {
	pool := adminTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{config: &config.Config{}, pool: pool, river: client}
	mux := newManagementMux(server)
	ctx := context.Background()

	insertRetryingEpisode := func(externalID string) string {
		t.Helper()
		id := uuid.NewString()
		_, err := pool.Exec(ctx, `
			WITH item AS (
				INSERT INTO feed_items (source_id, external_id, title, content)
				VALUES ('test', $2, 'Episode', 'content') RETURNING id
			)
			INSERT INTO episodes (id, source_id, feed_item_id, title, status, error)
			SELECT $1, 'test', id, 'Episode', 'retrying', 'job cancelled remotely' FROM item
		`, id, externalID)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	retry := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/episodes/"+id+"/retry", nil))
		return response
	}

	orphanedID := insertRetryingEpisode("orphaned")
	response := retry(orphanedID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("orphaned retry status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM episodes WHERE id=$1`, orphanedID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("orphaned episode status = %q, want queued", status)
	}
	var jobsQueued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM river_job
		WHERE args->>'episode_id'=$1 AND kind='resolve_content' AND state='available'
	`, orphanedID).Scan(&jobsQueued); err != nil {
		t.Fatal(err)
	}
	if jobsQueued != 1 {
		t.Fatalf("queued jobs = %d, want 1", jobsQueued)
	}

	activeID := insertRetryingEpisode("active")
	if _, err := client.Insert(ctx, jobs.ResolveContentArgs{EpisodeID: activeID}, nil); err != nil {
		t.Fatal(err)
	}
	response = retry(activeID)
	if response.Code != http.StatusConflict {
		t.Fatalf("active retry status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	if response.Body.String() != "{\"error\":\"episode already has an active job\"}\n" {
		t.Fatalf("active retry response = %s", response.Body.String())
	}
}
