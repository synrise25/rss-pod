package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const (
	dataRetention    = 10 * 24 * time.Hour
	cleanupBatchSize = 1000
)

type CleanupDataArgs struct{}

func (CleanupDataArgs) Kind() string { return "cleanup_data" }
func (CleanupDataArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "source", MaxAttempts: 5}
}

type CleanupDataWorker struct {
	river.WorkerDefaults[CleanupDataArgs]
	Pool *pgxpool.Pool
}

func (w *CleanupDataWorker) Work(ctx context.Context, _ *river.Job[CleanupDataArgs]) error {
	cutoff := cleanupCutoff(time.Now())
	feedItems, err := w.deleteExpiredFeedItems(ctx, cutoff)
	if err != nil {
		return err
	}
	sourceRuns, err := w.deleteExpiredSourceRuns(ctx, cutoff)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "expired data cleaned", "cutoff", cutoff, "feed_items", feedItems, "source_runs", sourceRuns)
	return nil
}

func cleanupCutoff(now time.Time) time.Time {
	return now.UTC().Add(-dataRetention)
}

func (w *CleanupDataWorker) deleteExpiredFeedItems(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := w.Pool.Exec(ctx, `
			WITH expired AS (
				SELECT id
				FROM feed_items
				WHERE discovered_at < $1
				  AND (published_at IS NULL OR published_at < $1)
				ORDER BY discovered_at, id
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM feed_items AS item
			USING expired
			WHERE item.id = expired.id
		`, cutoff, cleanupBatchSize)
		if err != nil {
			return total, fmt.Errorf("delete expired feed items: %w", err)
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted < cleanupBatchSize {
			return total, nil
		}
	}
}

func (w *CleanupDataWorker) deleteExpiredSourceRuns(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := w.Pool.Exec(ctx, `
			WITH expired AS (
				SELECT id
				FROM source_runs
				WHERE created_at < $1
				ORDER BY created_at, id
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM source_runs AS run
			USING expired
			WHERE run.id = expired.id
		`, cutoff, cleanupBatchSize)
		if err != nil {
			return total, fmt.Errorf("delete expired source runs: %w", err)
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted < cleanupBatchSize {
			return total, nil
		}
	}
}
