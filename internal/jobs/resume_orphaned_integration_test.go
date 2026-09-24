package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/synrise25/rss-pod/internal/config"
)

func TestResumeOrphanedStagesIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	ctx := context.Background()
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status                                 string
		documents, turns, screening, cancelled bool
		want                                   string
	}{
		{status: "queued", want: "resolve_content"},
		{status: "resolving_content", want: "resolve_content"},
		{status: "content_ready", documents: true, screening: true, want: "screen_content"},
		{status: "content_ready", documents: true, cancelled: true, want: "generate_script"},
		{status: "screening_content", documents: true, screening: true, want: "screen_content"},
		{status: "generating_script", documents: true, screening: true, want: "screen_content"},
		{status: "script_ready", documents: true, turns: true, screening: true, want: "generate_tts"},
		{status: "script_ready", documents: true, turns: true, cancelled: true, want: "generate_tts"},
		{status: "generating_tts", documents: true, turns: true, want: "generate_tts"},
		{status: "composing", documents: true, turns: true, want: "generate_tts"},
		{status: "failed", documents: true, want: "generate_script"},
		{status: "retrying", documents: true, turns: true, want: "generate_tts"},
		{status: "published", documents: true, turns: true},
		{status: "skipped", documents: true},
	} {
		t.Run(tc.status+"/"+tc.want, func(t *testing.T) {
			id := seedRetryingEpisode(t, pool, t.Name())
			old, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: id}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE episodes SET status=$2,active_job_id=$3 WHERE id=$1`, id, tc.status, old.Job.ID); err != nil {
				t.Fatal(err)
			}
			if tc.cancelled {
				if _, err := client.JobCancel(ctx, old.Job.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := pool.Exec(ctx, `DELETE FROM river_job WHERE id=$1`, old.Job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.documents {
				if _, err := pool.Exec(ctx, `INSERT INTO documents(episode_id,position,content) VALUES($1,0,'saved content')`, id); err != nil {
					t.Fatal(err)
				}
			}
			if tc.turns {
				if _, err := pool.Exec(ctx, `INSERT INTO script_turns(episode_id,position,speaker_id,text) VALUES($1,0,'host','saved script')`, id); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{Defaults: config.DefaultsConfig{Screening: config.ScreeningConfig{Enabled: &tc.screening}}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			got, err := ResumeEpisode(ctx, tx, client, id, cfg)
			if tc.want == "" {
				if !errors.Is(err, ErrEpisodeNotRetryable) {
					t.Fatalf("terminal status resumed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.JobKind != tc.want || got.JobID == old.Job.ID {
				t.Fatalf("resume=%+v, want fresh %s", got, tc.want)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var status string
			var owner int64
			if err := pool.QueryRow(ctx, `SELECT status,active_job_id FROM episodes WHERE id=$1`, id).Scan(&status, &owner); err != nil {
				t.Fatal(err)
			}
			if status != "queued" || owner != got.JobID {
				t.Fatalf("status=%s owner=%d", status, owner)
			}
			// Repeated manual polls must not enqueue a second job after recovery.
			tx2, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx2.Rollback(ctx)
			if _, err := ResumeEpisode(ctx, tx2, client, id, cfg); !errors.Is(err, ErrEpisodeJobActive) {
				t.Fatalf("second resume=%v", err)
			}
		})
	}
}

func TestIntermediateStagesWithActiveJobsCannotResumeIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	ctx := context.Background()
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"available", "pending", "retryable", "running", "scheduled"} {
		t.Run(state, func(t *testing.T) {
			id := seedRetryingEpisode(t, pool, state)
			job, err := client.Insert(ctx, GenerateScriptArgs{EpisodeID: id}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE river_job SET state=$2,attempt=1,attempted_at=now() WHERE id=$1`, job.Job.ID, state); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE episodes SET status='content_ready',active_job_id=$2 WHERE id=$1`, id, job.Job.ID); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := ResumeEpisode(ctx, tx, client, id, &config.Config{}); !errors.Is(err, ErrEpisodeJobActive) {
				t.Fatalf("resume with %s job: %v", state, err)
			}
		})
	}
}
