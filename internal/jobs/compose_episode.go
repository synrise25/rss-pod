package jobs

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/synrise25/rss-pod/internal/audioinfo"
	"github.com/synrise25/rss-pod/internal/storage"
)

type ComposeEpisodeWorker struct {
	river.WorkerDefaults[ComposeEpisodeArgs]
	Pool    *pgxpool.Pool
	Storage *storage.Client
}

func (w *ComposeEpisodeWorker) Work(ctx context.Context, job *river.Job[ComposeEpisodeArgs]) error {
	if err := w.compose(ctx, job.Args.EpisodeID); err != nil {
		return finishEpisodeAttempt(ctx, w.Pool, job.Args.EpisodeID, job.Attempt, job.MaxAttempts, err)
	}
	return nil
}

func (w *ComposeEpisodeWorker) compose(ctx context.Context, episodeID string) error {
	var sourceID string
	if err := w.Pool.QueryRow(ctx, `
		SELECT e.source_id
		FROM episodes e WHERE e.id = $1
	`, episodeID).Scan(&sourceID); err != nil {
		return fmt.Errorf("load episode composition input: %w", err)
	}
	rows, err := w.Pool.Query(ctx, `
		SELECT position, object_key FROM audio_segments WHERE episode_id = $1 ORDER BY position
	`, episodeID)
	if err != nil {
		return fmt.Errorf("query audio segments: %w", err)
	}
	defer rows.Close()
	type segment struct {
		position int
		key      string
	}
	var segments []segment
	for rows.Next() {
		var value segment
		if err := rows.Scan(&value.position, &value.key); err != nil {
			return err
		}
		segments = append(segments, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("episode has no audio segments")
	}

	var audio bytes.Buffer
	var audioDuration time.Duration
	for expected, segment := range segments {
		if segment.position != expected {
			return fmt.Errorf("audio segment position gap at %d", expected)
		}
		data, err := w.Storage.GetPrivate(ctx, segment.key)
		if err != nil {
			return err
		}
		duration, err := audioinfo.MP3Duration(data)
		if err != nil {
			return fmt.Errorf("read duration of audio segment %s: %w", segment.key, err)
		}
		audioDuration += duration
		if _, err := audio.Write(data); err != nil {
			return err
		}
	}
	durationSeconds := int64(math.Round(audioDuration.Seconds()))
	if durationSeconds < 1 {
		return fmt.Errorf("composed audio duration is invalid: %s", audioDuration)
	}
	if audio.Len() == 0 {
		return fmt.Errorf("composed audio is empty")
	}
	key := fmt.Sprintf("sources/%s/episodes/%s.mp3", sourceID, episodeID)
	if err := w.Storage.PutMedia(ctx, key, "audio/mpeg", audio.Bytes()); err != nil {
		return err
	}
	publicURL := w.Storage.PublicURL(key)
	if _, err := w.Pool.Exec(ctx, `
		UPDATE episodes
		SET status = 'published', audio_object_key = $2, audio_url = $3, audio_byte_size = $4,
		    audio_duration_seconds = $5, error = '', updated_at = now(), published_at = now()
		WHERE id = $1
	`, episodeID, key, publicURL, audio.Len(), durationSeconds); err != nil {
		return fmt.Errorf("publish episode: %w", err)
	}
	return nil
}
