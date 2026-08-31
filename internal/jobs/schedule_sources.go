package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"github.com/synrise25/rss-pod/internal/config"
)

type ScheduleSourcesArgs struct{}

func (ScheduleSourcesArgs) Kind() string { return "schedule_sources" }
func (ScheduleSourcesArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "source", MaxAttempts: 5}
}

type ScheduleSourcesWorker struct {
	river.WorkerDefaults[ScheduleSourcesArgs]
	Pool   *pgxpool.Pool
	Config *config.Config
	River  *river.Client[pgx.Tx]
}

func (w *ScheduleSourcesWorker) Work(ctx context.Context, _ *river.Job[ScheduleSourcesArgs]) error {
	now := time.Now().UTC().Truncate(time.Minute)
	dueSources, err := dueSourcesAt(w.Config, now)
	if err != nil {
		return permanent("resolve due sources: %v", err)
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, source := range dueSources {
		runID := uuid.New()
		var insertedID string
		err := tx.QueryRow(ctx, `
			INSERT INTO source_runs (id, source_id, status, scheduled_for)
			VALUES ($1, $2, 'queued', $3)
			ON CONFLICT (source_id, scheduled_for) WHERE scheduled_for IS NOT NULL DO NOTHING
			RETURNING id::text
		`, runID, source.ID, now).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create scheduled source run: %w", err)
		}
		if _, err := w.River.InsertTx(ctx, tx, PollSourceArgs{SourceID: source.ID, RunID: insertedID}, nil); err != nil {
			return fmt.Errorf("enqueue scheduled source poll: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduler_state (id, last_checked_at)
		VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET last_checked_at = EXCLUDED.last_checked_at
	`, now); err != nil {
		return fmt.Errorf("record scheduler tick: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scheduler tick: %w", err)
	}
	return nil
}

func dueSourcesAt(cfg *config.Config, now time.Time) ([]config.SourceConfig, error) {
	location, err := time.LoadLocation(cfg.Defaults.Schedule.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load scheduler timezone: %w", err)
	}
	localNow := now.UTC().Truncate(time.Minute).In(location)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	due := make([]config.SourceConfig, 0)
	for _, source := range cfg.Sources {
		if !source.Enabled {
			continue
		}
		schedule, err := parser.Parse(source.Schedule.Cron)
		if err != nil {
			return nil, fmt.Errorf("source %s cron: %w", source.ID, err)
		}
		if schedule.Next(localNow.Add(-time.Second)).Equal(localNow) {
			due = append(due, source)
		}
	}
	return due, nil
}
