package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

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

func TestPollResumeIncompleteFlagIntegration(t *testing.T) {
	pool := adminTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume_%t", want), func(t *testing.T) {
			tx, err := pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			queued, err := jobs.EnqueuePoll(context.Background(), tx, client, "test", 12, want)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			var got bool
			if err := pool.QueryRow(context.Background(), `
				SELECT COALESCE((args->>'resume_incomplete')::boolean, false)
				FROM river_job WHERE id=$1
			`, queued.JobID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("resume_incomplete = %t, want %t", got, want)
			}
		})
	}
}

func TestOnlyManualPollResumesIncompleteEpisodeIntegration(t *testing.T) {
	for _, test := range []struct {
		name             string
		resumeIncomplete bool
		wantStatus       string
		wantJobs         int
	}{
		{name: "manual", resumeIncomplete: true, wantStatus: "queued", wantJobs: 1},
		{name: "scheduled", resumeIncomplete: false, wantStatus: "retrying", wantJobs: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := adminTestPool(t)
			client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
			if err != nil {
				t.Fatal(err)
			}
			feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/rss+xml")
				_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Test</title><link>https://example.com</link><description>Test</description>
<item><guid>existing-guid</guid><title>Existing</title><link>https://example.com/existing</link><description>Existing content</description></item>
</channel></rss>`)
			}))
			defer feed.Close()

			ctx := context.Background()
			runID := uuid.NewString()
			episodeID := uuid.NewString()
			if _, err = pool.Exec(ctx, `
				INSERT INTO source_runs (id, source_id, status) VALUES ($1, 'test', 'queued')
			`, runID); err != nil {
				t.Fatal(err)
			}
			_, err = pool.Exec(ctx, `
				WITH item AS (
					INSERT INTO feed_items (source_id, external_id, title, content)
					VALUES ('test', 'existing-guid', 'Existing', 'old content') RETURNING id
				)
				INSERT INTO episodes (id, source_id, feed_item_id, title, status, error)
				SELECT $1, 'test', id, 'Existing', 'retrying', 'job cancelled remotely' FROM item
			`, episodeID)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{
				Defaults: config.DefaultsConfig{Limits: config.LimitsConfig{MaxFeedItemsPerRun: 10}},
				Sources:  []config.SourceConfig{{ID: "test", Enabled: true, Feed: config.FeedConfig{URL: feed.URL}}},
			}
			worker := &jobs.PollSourceWorker{Pool: pool, Config: cfg, River: client}
			job := &river.Job[jobs.PollSourceArgs]{
				JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5},
				Args: jobs.PollSourceArgs{
					SourceID:         "test",
					RunID:            runID,
					ResumeIncomplete: test.resumeIncomplete,
				},
			}
			if err := worker.Work(ctx, job); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM episodes WHERE id=$1`, episodeID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != test.wantStatus {
				t.Fatalf("episode status = %q, want %q", status, test.wantStatus)
			}
			var jobsQueued int
			if err := pool.QueryRow(ctx, `
				SELECT count(*) FROM river_job
				WHERE args->>'episode_id'=$1 AND kind='resolve_content' AND state='available'
			`, episodeID).Scan(&jobsQueued); err != nil {
				t.Fatal(err)
			}
			if jobsQueued != test.wantJobs {
				t.Fatalf("queued jobs = %d, want %d", jobsQueued, test.wantJobs)
			}
		})
	}
}
