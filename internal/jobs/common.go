package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

type permanentError struct{ err error }

const episodeFailureUpdateTimeout = 5 * time.Second

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(format string, args ...any) error {
	return &permanentError{err: fmt.Errorf(format, args...)}
}

func finishEpisodeAttempt(ctx context.Context, pool *pgxpool.Pool, episodeID string, jobID int64, attempt, maxAttempts int, workErr error) error {
	var permanentErr *permanentError
	isPermanent := errors.As(workErr, &permanentErr)
	status := episodeFailureStatus(ctx, isPermanent, attempt, maxAttempts)
	updateCtx, cancel := episodeFailureUpdateContext(ctx)
	defer cancel()
	tx, err := pool.Begin(updateCtx)
	if err != nil {
		return fmt.Errorf("%v; begin episode failure update: %w", workErr, err)
	}
	defer tx.Rollback(updateCtx)
	var lockedID string
	if err := tx.QueryRow(updateCtx, `
		SELECT id::text FROM episodes WHERE id = $1 FOR UPDATE
	`, episodeID).Scan(&lockedID); err != nil {
		return fmt.Errorf("%v; lock episode for failure update: %w", workErr, err)
	}
	var replacementActive bool
	if err := tx.QueryRow(updateCtx, `
		SELECT EXISTS (
			SELECT 1 FROM river_job
			WHERE args->>'episode_id' = $1
			  AND id <> $2
			  AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
		)
	`, episodeID, jobID).Scan(&replacementActive); err != nil {
		return fmt.Errorf("%v; check replacement episode job: %w", workErr, err)
	}
	if !replacementActive {
		if _, err := tx.Exec(updateCtx, `
			UPDATE episodes SET status = $2, error = $3, updated_at = now() WHERE id = $1
		`, episodeID, status, workErr.Error()); err != nil {
			return fmt.Errorf("%v; update episode failure: %w", workErr, err)
		}
	}
	if err := tx.Commit(updateCtx); err != nil {
		return fmt.Errorf("%v; update episode failure: %w", workErr, err)
	}
	if isPermanent {
		return river.JobCancel(workErr)
	}
	return workErr
}

func episodeFailureStatus(ctx context.Context, isPermanent bool, attempt, maxAttempts int) string {
	// River permanently cancels a remotely cancelled job instead of scheduling
	// another attempt. Keep the business state consistent with that terminal
	// queue state so the episode can be retried through the management API.
	if isPermanent || attempt >= maxAttempts || errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely) {
		return "failed"
	}
	return "retrying"
}

func episodeFailureUpdateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), episodeFailureUpdateTimeout)
}

type ResolveContentArgs struct {
	EpisodeID string `json:"episode_id" river:"unique"`
}

func (ResolveContentArgs) Kind() string { return "resolve_content" }
func (ResolveContentArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "content", MaxAttempts: 5}
}

type GenerateScriptArgs struct {
	EpisodeID string `json:"episode_id" river:"unique"`
}

func (GenerateScriptArgs) Kind() string { return "generate_script" }
func (GenerateScriptArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "llm", MaxAttempts: 4}
}

type GenerateTTSArgs struct {
	EpisodeID string `json:"episode_id" river:"unique"`
}

func (GenerateTTSArgs) Kind() string { return "generate_tts" }
func (GenerateTTSArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "tts", MaxAttempts: 5}
}

type ComposeEpisodeArgs struct {
	EpisodeID string `json:"episode_id" river:"unique"`
}

func (ComposeEpisodeArgs) Kind() string { return "compose_episode" }
func (ComposeEpisodeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "media", MaxAttempts: 5}
}
