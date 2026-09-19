package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/synrise25/rss-pod/internal/database"
)

func jobsTestPool(t *testing.T) *pgxpool.Pool {
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
	schema := "jobs_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	return pool
}

func seedRetryingEpisode(t *testing.T, pool *pgxpool.Pool, externalID string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := pool.Exec(context.Background(), `
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

func TestStaleCancelledWorkerDoesNotOverwriteCompletedResumedEpisodeIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "stale-worker")
	oldJob, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: episodeID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.JobCancel(ctx, oldJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeEpisode(ctx, tx, client, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Model the replacement finishing and its River row later being cleaned up.
	// The persisted episode owner must still reject the stale worker's write.
	if _, err := pool.Exec(ctx, `DELETE FROM river_job WHERE id = $1`, resumed.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE episodes SET status = 'published' WHERE id = $1`, episodeID); err != nil {
		t.Fatal(err)
	}

	cancelledCtx, cancel := context.WithCancelCause(ctx)
	cancel(river.ErrJobCancelledRemotely)
	workErr := errors.New("cancelled work returned late")
	if err := finishEpisodeAttempt(cancelledCtx, pool, episodeID, oldJob.Job.ID, 1, 5, workErr); !errors.Is(err, workErr) {
		t.Fatalf("finishEpisodeAttempt() error = %v, want wrapped work error", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM episodes WHERE id=$1`, episodeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "published" {
		t.Fatalf("episode status = %q, want published", status)
	}
}

func TestSupersededWorkerCannotOverwriteResumedQueuedStatusIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "superseded-setup")
	oldJob, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: episodeID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE episodes SET status = 'failed', active_job_id = $2 WHERE id = $1
	`, episodeID, oldJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.JobCancel(ctx, oldJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	resumed, err := ResumeEpisode(ctx, tx, client, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	job := &river.Job[ResolveContentArgs]{
		JobRow: &rivertype.JobRow{ID: oldJob.Job.ID, Attempt: 1, MaxAttempts: 5},
		Args:   ResolveContentArgs{EpisodeID: episodeID},
	}
	worker := &ResolveContentWorker{Pool: pool}
	if err := worker.Work(ctx, job); !errors.Is(err, errEpisodeAttemptSuperseded) {
		t.Fatalf("stale worker error = %v, want superseded", err)
	}
	var status string
	var activeJobID int64
	if err := pool.QueryRow(ctx, `
		SELECT status, active_job_id FROM episodes WHERE id = $1
	`, episodeID).Scan(&status, &activeJobID); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("episode status = %q, want queued", status)
	}
	if activeJobID != resumed.JobID {
		t.Fatalf("active job ID = %d, want %d", activeJobID, resumed.JobID)
	}
}

func TestSupersededTTSWorkerCannotPublishSegmentIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "superseded-segment")
	const currentJobID int64 = 200
	if _, err := pool.Exec(ctx, `
		UPDATE episodes SET status = 'generating_tts', active_job_id = $2 WHERE id = $1
	`, episodeID, currentJobID); err != nil {
		t.Fatal(err)
	}

	staleKey := audioSegmentObjectKey(episodeID, currentJobID-1, 0)
	err := storeAudioSegment(ctx, pool, episodeID, currentJobID-1, 0, "test", staleKey, 5)
	if !errors.Is(err, errEpisodeAttemptSuperseded) {
		t.Fatalf("stale segment publication error = %v, want superseded", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audio_segments WHERE episode_id = $1`, episodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored %d stale segments, want 0", count)
	}

	currentKey := audioSegmentObjectKey(episodeID, currentJobID, 0)
	if err := storeAudioSegment(ctx, pool, episodeID, currentJobID, 0, "test", currentKey, 5); err != nil {
		t.Fatal(err)
	}
	var storedKey string
	if err := pool.QueryRow(ctx, `
		SELECT object_key FROM audio_segments WHERE episode_id = $1 AND position = 0
	`, episodeID).Scan(&storedKey); err != nil {
		t.Fatal(err)
	}
	if storedKey != currentKey {
		t.Fatalf("stored segment key = %q, want %q", storedKey, currentKey)
	}
}

func TestCancelledQueuedEpisodeCanBeResumedIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "cancelled-before-work")
	queuedJob, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: episodeID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE episodes SET status = 'queued', active_job_id = $2 WHERE id = $1
	`, episodeID, queuedJob.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.JobCancel(ctx, queuedJob.Job.ID); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	resumed, err := ResumeEpisode(ctx, tx, client, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if resumed.JobID == queuedJob.Job.ID {
		t.Fatalf("resume reused cancelled job ID %d", resumed.JobID)
	}
	var status string
	var activeJobID int64
	if err := pool.QueryRow(ctx, `
		SELECT status, active_job_id FROM episodes WHERE id = $1
	`, episodeID).Scan(&status, &activeJobID); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || activeJobID != resumed.JobID {
		t.Fatalf("resumed episode = status %q active job %d, want queued/%d", status, activeJobID, resumed.JobID)
	}
}

func TestResumeAndStageHandoffUseSameLockOrderIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "handoff-lock-order")
	const handoffJobID int64 = 300
	if _, err := pool.Exec(ctx, `
		UPDATE episodes SET status = 'generating_script', active_job_id = $2 WHERE id = $1
	`, episodeID, handoffJobID); err != nil {
		t.Fatal(err)
	}

	handoffTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer handoffTx.Rollback(ctx)
	if err := lockEpisodeAttempt(ctx, handoffTx, episodeID, handoffJobID); err != nil {
		t.Fatal(err)
	}

	resumeStarted := make(chan struct{})
	resumeResult := make(chan error, 1)
	go func() {
		resumeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tx, err := pool.Begin(resumeCtx)
		if err != nil {
			resumeResult <- err
			return
		}
		defer tx.Rollback(resumeCtx)
		close(resumeStarted)
		_, err = ResumeEpisode(resumeCtx, tx, client, episodeID)
		resumeResult <- err
	}()
	<-resumeStarted

	inserted, err := client.InsertTx(ctx, handoffTx, GenerateTTSArgs{EpisodeID: episodeID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handoffTx.Exec(ctx, `
		UPDATE episodes SET status = 'script_ready', active_job_id = $2 WHERE id = $1
	`, episodeID, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := handoffTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-resumeResult:
		if !errors.Is(err, ErrEpisodeJobActive) {
			t.Fatalf("concurrent resume error = %v, want active job", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent resume deadlocked with stage handoff")
	}
}

func TestCancelledSetupErrorFinalizesEpisodeIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episodeID := seedRetryingEpisode(t, pool, "setup-error")
	inserted, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: episodeID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelledCtx, cancel := context.WithCancelCause(ctx)
	cancel(river.ErrJobCancelledRemotely)
	job := &river.Job[ResolveContentArgs]{
		JobRow: &rivertype.JobRow{ID: inserted.Job.ID, Attempt: 1, MaxAttempts: 5},
		Args:   ResolveContentArgs{EpisodeID: episodeID},
	}
	worker := &ResolveContentWorker{Pool: pool}
	if err := worker.Work(cancelledCtx, job); err == nil {
		t.Fatal("cancelled setup unexpectedly succeeded")
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM episodes WHERE id=$1`, episodeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("episode status = %q, want failed", status)
	}
}
