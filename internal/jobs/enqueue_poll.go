package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type EnqueuedPoll struct {
	SourceID string    `json:"source_id"`
	RunID    uuid.UUID `json:"run_id"`
	JobID    int64     `json:"job_id"`
}

func EnqueuePoll(
	ctx context.Context,
	tx pgx.Tx,
	client *river.Client[pgx.Tx],
	sourceID string,
	limit int,
) (EnqueuedPoll, error) {
	runID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_runs (id, source_id, status) VALUES ($1, $2, 'queued')
	`, runID, sourceID); err != nil {
		return EnqueuedPoll{}, fmt.Errorf("create source run: %w", err)
	}
	inserted, err := client.InsertTx(ctx, tx, PollSourceArgs{
		SourceID: sourceID,
		RunID:    runID.String(),
		Limit:    limit,
	}, nil)
	if err != nil {
		return EnqueuedPoll{}, fmt.Errorf("enqueue source poll: %w", err)
	}
	return EnqueuedPoll{SourceID: sourceID, RunID: runID, JobID: inserted.Job.ID}, nil
}
