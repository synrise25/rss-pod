package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/synrise25/rss-pod/internal/config"
)

func TestSchedulerCatchUpConcurrentIntegration(t *testing.T) {
	worker, now := schedulerTestWorker(t)
	ctx := context.Background()
	start, scheduled := now.Add(-5*time.Minute), now.Add(-2*time.Minute)
	worker.Config.Sources[0].Schedule.Cron = fmt.Sprintf("%d %d * * *", scheduled.Minute(), scheduled.Hour())
	if _, err := worker.Pool.Exec(ctx, `INSERT INTO scheduler_state(id,last_checked_at) VALUES(true,$1)`, start); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 6)
	for range 6 {
		go func() { results <- worker.Work(ctx, nil) }()
	}
	for range 6 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertSchedulerPoll(t, worker, scheduled)
	var checkpoint, databaseNow time.Time
	if err := worker.Pool.QueryRow(ctx, `SELECT last_checked_at,date_trunc('minute',clock_timestamp()) FROM scheduler_state`).Scan(&checkpoint, &databaseNow); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Before(now) || checkpoint.After(databaseNow) {
		t.Fatalf("checkpoint=%s outside database interval [%s,%s]", checkpoint, now, databaseNow)
	}
	// A stale checkpoint must not enqueue a duplicate run or job. A new worker
	// also resumes from persisted state rather than from its own process clock.
	if _, err := worker.Pool.Exec(ctx, `UPDATE scheduler_state SET last_checked_at=$1`, start); err != nil {
		t.Fatal(err)
	}
	restarted := &ScheduleSourcesWorker{Pool: worker.Pool, River: worker.River, Config: worker.Config}
	if err := restarted.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertSchedulerPoll(t, restarted, scheduled)
}

func TestSchedulerRollbackDoesNotLoseMissedPollIntegration(t *testing.T) {
	worker, now := schedulerTestWorker(t)
	ctx := context.Background()
	start, scheduled := now.Add(-5*time.Minute), now.Add(-2*time.Minute)
	worker.Config.Sources[0].Schedule.Cron = fmt.Sprintf("%d %d * * *", scheduled.Minute(), scheduled.Hour())
	if _, err := worker.Pool.Exec(ctx, `INSERT INTO scheduler_state(id,last_checked_at) VALUES(true,$1)`, start); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Pool.Exec(ctx, `ALTER TABLE river_job ADD CONSTRAINT reject_poll CHECK(kind <> 'poll_source')`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err == nil {
		t.Fatal("scheduler succeeded despite rejected job insertion")
	}
	var checkpoint time.Time
	var count int
	if err := worker.Pool.QueryRow(ctx, `SELECT last_checked_at,(SELECT count(*) FROM source_runs) FROM scheduler_state`).Scan(&checkpoint, &count); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.Equal(start) || count != 0 {
		t.Fatalf("failed transaction left checkpoint=%s runs=%d, want %s/0", checkpoint, count, start)
	}
	if _, err := worker.Pool.Exec(ctx, `ALTER TABLE river_job DROP CONSTRAINT reject_poll`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertSchedulerPoll(t, worker, scheduled)
}

func TestSchedulerFirstEnqueueFailureKeepsBaselineIntegration(t *testing.T) {
	worker, _ := schedulerTestWorker(t)
	ctx := context.Background()
	if _, err := worker.Pool.Exec(ctx, `ALTER TABLE river_job ADD CONSTRAINT reject_poll CHECK(kind <> 'poll_source')`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err == nil {
		t.Fatal("initial enqueue unexpectedly succeeded")
	}
	var baseline time.Time
	var count int
	if err := worker.Pool.QueryRow(ctx, `SELECT last_checked_at,(SELECT count(*) FROM source_runs) FROM scheduler_state`).Scan(&baseline, &count); err != nil {
		t.Fatalf("initial baseline must survive a failed enqueue: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed initial enqueue left %d source runs", count)
	}
	// Retry a daily occurrence at the original first-start minute. The persisted
	// baseline keeps it eligible even if this retry crosses a minute boundary.
	scheduled := baseline.UTC().Add(time.Minute)
	worker.Config.Sources[0].Schedule.Cron = fmt.Sprintf("%d %d * * *", scheduled.Minute(), scheduled.Hour())
	if _, err := worker.Pool.Exec(ctx, `ALTER TABLE river_job DROP CONSTRAINT reject_poll`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertSchedulerPoll(t, worker, scheduled)
}

func TestSchedulerFirstStartAndClockRegressionIntegration(t *testing.T) {
	worker, _ := schedulerTestWorker(t)
	ctx := context.Background()
	// Capture the actual initialization boundary. The first tick can correctly
	// cover two minutes if initialization and the locked clock read straddle one.
	if _, err := worker.Pool.Exec(ctx, `
		CREATE TABLE scheduler_initial_baseline(last_checked_at timestamptz NOT NULL);
		CREATE FUNCTION record_scheduler_baseline() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		    INSERT INTO scheduler_initial_baseline VALUES (NEW.last_checked_at);
		    RETURN NEW;
		END;
		$$;
		CREATE TRIGGER record_scheduler_baseline AFTER INSERT ON scheduler_state
		FOR EACH ROW EXECUTE FUNCTION record_scheduler_baseline();
	`); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	var checkpoint, baseline time.Time
	if err := worker.Pool.QueryRow(ctx, `SELECT s.last_checked_at,b.last_checked_at,(SELECT count(*) FROM source_runs)
		FROM scheduler_state s CROSS JOIN scheduler_initial_baseline b`).Scan(&checkpoint, &baseline, &count); err != nil {
		t.Fatal(err)
	}
	expected := int(checkpoint.Sub(baseline) / time.Minute)
	if expected < 1 || count != expected {
		t.Fatalf("first start created %d polls, want %d in (%s,%s]", count, expected, baseline, checkpoint)
	}
	assertSchedulerPollInterval(t, worker, baseline, checkpoint, expected)
	initialCheckpoint := checkpoint
	future := checkpoint.Add(10 * time.Minute)
	if _, err := worker.Pool.Exec(ctx, `UPDATE scheduler_state SET last_checked_at=$1`, future); err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.Pool.QueryRow(ctx, `SELECT last_checked_at FROM scheduler_state`).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.Equal(future) {
		t.Fatalf("checkpoint regressed to %s, want %s", checkpoint, future)
	}
	assertSchedulerPollInterval(t, worker, baseline, initialCheckpoint, expected)
}

func assertSchedulerPollInterval(t *testing.T, worker *ScheduleSourcesWorker, start, end time.Time, expected int) {
	t.Helper()
	var runs, jobs, matched int
	if err := worker.Pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM source_runs),
		       (SELECT count(*) FROM river_job WHERE kind='poll_source'),
		       (SELECT count(*) FROM generate_series($1::timestamptz+interval '1 minute',$2,interval '1 minute') occurrence
		        JOIN source_runs r ON r.scheduled_for=occurrence AND r.source_id='test'
		        JOIN river_job j ON j.args->>'run_id'=r.id::text AND j.kind='poll_source')
	`, start, end).Scan(&runs, &jobs, &matched); err != nil {
		t.Fatal(err)
	}
	if runs != expected || jobs != expected || matched != expected {
		t.Fatalf("runs=%d jobs=%d matching occurrences=%d, want %d each", runs, jobs, matched, expected)
	}
}

func schedulerTestWorker(t *testing.T) (*ScheduleSourcesWorker, time.Time) {
	t.Helper()
	pool := jobsTestPool(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT date_trunc('minute',clock_timestamp())`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Schedule: config.ScheduleConfig{Timezone: "UTC"}},
		Sources: []config.SourceConfig{
			{ID: "test", Enabled: true, Schedule: config.ScheduleConfig{Cron: "* * * * *"}},
			{ID: "disabled", Schedule: config.ScheduleConfig{Cron: "* * * * *"}},
		},
	}
	return &ScheduleSourcesWorker{Pool: pool, Config: cfg, River: client}, now.UTC()
}

func assertSchedulerPoll(t *testing.T, worker *ScheduleSourcesWorker, scheduled time.Time) {
	t.Helper()
	var runs, jobs, matched int
	err := worker.Pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM source_runs),
		       (SELECT count(*) FROM river_job WHERE kind='poll_source'),
		       (SELECT count(*) FROM source_runs r JOIN river_job j ON j.args->>'run_id'=r.id::text
		        WHERE r.source_id='test' AND r.scheduled_for=$1 AND r.status='queued'
		          AND j.kind='poll_source' AND j.args->>'source_id'=r.source_id)
	`, scheduled).Scan(&runs, &jobs, &matched)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || jobs != 1 || matched != 1 {
		t.Fatalf("runs=%d jobs=%d matching schedule=%d, want 1/1/1 at %s", runs, jobs, matched, scheduled)
	}
}
