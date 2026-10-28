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

const sourceScheduleCatchUpWindow = time.Hour

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
	// Persist the first baseline separately: a failed first enqueue must not
	// erase it and exclude that minute when the job retries in a later minute.
	if _, err := w.Pool.Exec(ctx, `
		INSERT INTO scheduler_state (id, last_checked_at)
		VALUES (true, date_trunc('minute', clock_timestamp()) - interval '1 minute')
		ON CONFLICT (id) DO NOTHING
	`); err != nil {
		return fmt.Errorf("initialize scheduler checkpoint: %w", err)
	}
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock the shared checkpoint so concurrent workers and retries cover the
	// same interval atomically. First startup evaluates only the current minute.
	var lastChecked, now time.Time
	if err := tx.QueryRow(ctx, `
		SELECT last_checked_at FROM scheduler_state WHERE id = true FOR UPDATE
	`).Scan(&lastChecked); err != nil {
		return fmt.Errorf("lock scheduler checkpoint: %w", err)
	}
	// Read the database clock after acquiring the lock. Transaction start time
	// can be stale after waiting, and a worker's system clock may differ.
	if err := tx.QueryRow(ctx, `SELECT date_trunc('minute', clock_timestamp())`).Scan(&now); err != nil {
		return fmt.Errorf("read scheduler time: %w", err)
	}
	duePolls, err := dueSourcePolls(w.Config, lastChecked, now)
	if err != nil {
		return permanent("resolve due sources: %v", err)
	}
	for _, poll := range duePolls {
		runID := uuid.New()
		var insertedID string
		err := tx.QueryRow(ctx, `
			INSERT INTO source_runs (id, source_id, status, scheduled_for)
			VALUES ($1, $2, 'queued', $3)
			ON CONFLICT (source_id, scheduled_for) WHERE scheduled_for IS NOT NULL DO NOTHING
			RETURNING id::text
		`, runID, poll.SourceID, poll.ScheduledFor).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create scheduled source run: %w", err)
		}
		if _, err := w.River.InsertTx(ctx, tx, PollSourceArgs{SourceID: poll.SourceID, RunID: insertedID}, nil); err != nil {
			return fmt.Errorf("enqueue scheduled source poll: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE scheduler_state SET last_checked_at = GREATEST(last_checked_at, $1)
		WHERE id = true
	`, now); err != nil {
		return fmt.Errorf("record scheduler tick: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scheduler tick: %w", err)
	}
	return nil
}

type scheduledSourcePoll struct {
	SourceID     string
	ScheduledFor time.Time
}

// dueSourcePolls returns cron occurrences in (lastChecked, now], bounded to
// the most recent hour. Keep their original timestamps for edition dates.
func dueSourcePolls(cfg *config.Config, lastChecked, now time.Time) ([]scheduledSourcePoll, error) {
	location, err := time.LoadLocation(cfg.Defaults.Schedule.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load scheduler timezone: %w", err)
	}
	now = now.UTC().Truncate(time.Minute)
	lastChecked = lastChecked.UTC().Truncate(time.Minute)
	if earliest := now.Add(-sourceScheduleCatchUpWindow); lastChecked.Before(earliest) {
		lastChecked = earliest
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	var due []scheduledSourcePoll
	for _, source := range cfg.Sources {
		if !source.Enabled {
			continue
		}
		schedule, err := parser.Parse(source.Schedule.Cron)
		if err != nil {
			return nil, fmt.Errorf("source %s cron: %w", source.ID, err)
		}
		for scheduled := schedule.Next(lastChecked.In(location)); !scheduled.IsZero() && !scheduled.After(now); scheduled = schedule.Next(scheduled) {
			due = append(due, scheduledSourcePoll{SourceID: source.ID, ScheduledFor: scheduled.UTC()})
		}
	}
	return due, nil
}
