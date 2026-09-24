package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/synrise25/rss-pod/internal/config"
)

var (
	ErrEpisodeNotFound     = errors.New("episode not found")
	ErrEpisodeNotRetryable = errors.New("episode is not recoverable")
	ErrEpisodeJobActive    = errors.New("episode already has an active job")
)

type ResumedEpisode struct {
	JobID   int64
	JobKind string
}

// ResumeEpisode locks an incomplete episode, verifies that River has no active
// job for it, and resumes from the first missing durable pipeline artifact.
func ResumeEpisode(
	ctx context.Context,
	tx pgx.Tx,
	client *river.Client[pgx.Tx],
	episodeID string,
	cfg *config.Config,
) (ResumedEpisode, error) {
	var status, sourceID string
	var documents, turns int
	err := tx.QueryRow(ctx, `
		SELECT e.status, e.source_id,
		       (SELECT count(*) FROM documents d WHERE d.episode_id = e.id),
		       (SELECT count(*) FROM script_turns t WHERE t.episode_id = e.id)
		FROM episodes e WHERE e.id = $1 FOR UPDATE
	`, episodeID).Scan(&status, &sourceID, &documents, &turns)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResumedEpisode{}, ErrEpisodeNotFound
	}
	if err != nil {
		return ResumedEpisode{}, fmt.Errorf("load episode for resume: %w", err)
	}
	var activeJob bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM river_job
			WHERE args->>'episode_id' = $1
			  AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
		)
	`, episodeID).Scan(&activeJob); err != nil {
		return ResumedEpisode{}, fmt.Errorf("check active episode jobs: %w", err)
	}
	if activeJob {
		return ResumedEpisode{}, ErrEpisodeJobActive
	}
	// Jobs can disappear or be cancelled between stage handoffs, leaving the
	// business state unchanged. Once no active River job remains, all known
	// incomplete states can resume from durable artifacts. Keep terminal and
	// unknown states ineligible, including successful editorial skips.
	switch status {
	case "queued", "resolving_content", "content_ready", "screening_content",
		"generating_script", "script_ready", "generating_tts", "composing",
		"retrying", "failed":
	default:
		return ResumedEpisode{}, ErrEpisodeNotRetryable
	}

	var args river.JobArgs
	switch {
	case documents == 0:
		args = ResolveContentArgs{EpisodeID: episodeID}
	case turns == 0:
		args = GenerateScriptArgs{EpisodeID: episodeID}
		source, _ := cfg.Source(sourceID)
		if cfg.EffectiveScreening(source).IsEnabled() {
			args = ScreenContentArgs{EpisodeID: episodeID}
		}
	default:
		args = GenerateTTSArgs{EpisodeID: episodeID}
	}
	inserted, err := client.InsertTx(ctx, tx, args, nil)
	if err != nil {
		return ResumedEpisode{}, fmt.Errorf("enqueue episode resume: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE episodes
		SET status = 'queued', error = '', active_job_id = $2, updated_at = now()
		WHERE id = $1
	`, episodeID, inserted.Job.ID); err != nil {
		return ResumedEpisode{}, fmt.Errorf("mark episode queued: %w", err)
	}
	return ResumedEpisode{JobID: inserted.Job.ID, JobKind: args.Kind()}, nil
}
