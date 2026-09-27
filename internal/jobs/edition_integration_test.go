package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/synrise25/rss-pod/internal/config"
)

func TestPollKeepsOriginalEditionIntegration(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		pool := jobsTestPool(t)
		ctx := context.Background()
		feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Test</title><link>https://example.com</link><description>Test</description><item><guid>one</guid><title>One</title><description>Content</description></item></channel></rss>`))
		}))
		defer feed.Close()
		cfg := &config.Config{Sources: []config.SourceConfig{{ID: "test", Enabled: true, Feed: config.FeedConfig{URL: feed.URL}}}, Defaults: config.DefaultsConfig{Schedule: config.ScheduleConfig{Timezone: "Asia/Shanghai"}, Limits: config.LimitsConfig{MaxFeedItemsPerRun: 10}}}
		client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
		if err != nil {
			t.Fatal(err)
		}
		runID := uuid.NewString()
		_, err = pool.Exec(ctx, `INSERT INTO source_runs(id,source_id,status,created_at,scheduled_for) VALUES($1,'test','queued','2026-09-26T23:30:00+08:00',CASE WHEN $2 THEN '2026-09-25T23:30:00+08:00'::timestamptz END)`, runID, scheduled)
		if err != nil {
			t.Fatal(err)
		}
		worker := &PollSourceWorker{Pool: pool, Config: cfg, River: client}
		args := PollSourceArgs{SourceID: "test", RunID: runID}
		if err := worker.poll(ctx, args); err != nil {
			t.Fatal(err)
		}
		want := "2026-09-26"
		if scheduled {
			want = "2026-09-25"
		}
		// Re-polling an existing episode must not replace its original edition.
		_, err = pool.Exec(ctx, `UPDATE source_runs SET created_at=now(),scheduled_for=NULL WHERE id=$1`, runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.poll(ctx, args); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT to_char(edition_date,'YYYY-MM-DD') FROM episodes`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("edition=%s want=%s", got, want)
		}
	}
}
