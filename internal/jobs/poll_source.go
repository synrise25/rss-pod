package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmcdole/gofeed"
	"github.com/riverqueue/river"

	"github.com/synrise25/rss-pod/internal/config"
)

type PollSourceArgs struct {
	SourceID string `json:"source_id" river:"unique"`
	RunID    string `json:"run_id"`
	Limit    int    `json:"limit,omitempty"`
}

func (PollSourceArgs) Kind() string { return "poll_source" }

func (PollSourceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "source", MaxAttempts: 5}
}

type PollSourceWorker struct {
	river.WorkerDefaults[PollSourceArgs]
	Pool   *pgxpool.Pool
	Config *config.Config
	River  *river.Client[pgx.Tx]
}

func (w *PollSourceWorker) Work(ctx context.Context, job *river.Job[PollSourceArgs]) error {
	if _, err := w.Pool.Exec(ctx, `
		UPDATE source_runs
		SET status = 'running', started_at = COALESCE(started_at, now()), error = ''
		WHERE id = $1
	`, job.Args.RunID); err != nil {
		return fmt.Errorf("mark source run running: %w", err)
	}

	if err := w.poll(ctx, job.Args); err != nil {
		status := "retrying"
		if job.Attempt >= job.MaxAttempts {
			status = "failed"
		}
		_, updateErr := w.Pool.Exec(ctx, `
			UPDATE source_runs
			SET status = $2, error = $3,
			    completed_at = CASE WHEN $2 = 'failed' THEN now() ELSE NULL END
			WHERE id = $1
		`, job.Args.RunID, status, err.Error())
		if updateErr != nil {
			return fmt.Errorf("%v; update run failure: %w", err, updateErr)
		}
		return err
	}
	return nil
}

func (w *PollSourceWorker) poll(ctx context.Context, args PollSourceArgs) error {
	source, ok := w.Config.Source(args.SourceID)
	if !ok {
		return fmt.Errorf("unknown source %q", args.SourceID)
	}
	if !source.Enabled {
		return fmt.Errorf("source %q is disabled", args.SourceID)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	parser := gofeed.NewParser()
	parser.Client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	feed, err := parser.ParseURLWithContext(source.Feed.URL, ctx)
	if err != nil {
		return fmt.Errorf("fetch feed: %w", err)
	}
	configuredLimit := w.Config.EffectiveLimits(source).MaxFeedItemsPerRun
	if args.Limit > 0 && args.Limit < configuredLimit {
		configuredLimit = args.Limit
	}
	limit := min(len(feed.Items), configuredLimit)
	generation := w.Config.EffectiveGeneration(source)
	dialogue := w.Config.DialogueProfiles[generation.DialogueProfile]

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	itemsNew := 0
	itemsExisting := 0
	for _, item := range feed.Items[:limit] {
		externalID := feedItemID(source.ID, item)
		var publishedAt *time.Time
		if item.PublishedParsed != nil {
			publishedAt = item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			publishedAt = item.UpdatedParsed
		}
		var feedItemID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO feed_items (
			    source_id, external_id, title, link, description, content, published_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (source_id, external_id) DO NOTHING
			RETURNING id
		`, source.ID, externalID, item.Title, item.Link, item.Description, item.Content, publishedAt).Scan(&feedItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			itemsExisting++
			err = tx.QueryRow(ctx, `
				UPDATE feed_items
				SET title = $3,
				    link = $4,
				    description = $5,
				    content = $6,
				    published_at = $7
				WHERE source_id = $1 AND external_id = $2
				RETURNING id
			`, source.ID, externalID, item.Title, item.Link, item.Description, item.Content, publishedAt).Scan(&feedItemID)
		} else if err == nil {
			itemsNew++
		}
		if err != nil {
			return fmt.Errorf("store feed item: %w", err)
		}

		episodeID := uuid.New()
		var insertedEpisodeID string
		err = tx.QueryRow(ctx, `
			INSERT INTO episodes (id, source_id, feed_item_id, title, status)
			VALUES ($1, $2, $3, $4, 'queued')
			ON CONFLICT (feed_item_id) DO NOTHING
			RETURNING id::text
		`, episodeID, source.ID, feedItemID, item.Title).Scan(&insertedEpisodeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("create episode: %w", err)
		}
		if err == nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO episode_dialogues (episode_id, profile_id, rate, volume, pitch)
				VALUES ($1, $2, $3, $4, $5)
			`, insertedEpisodeID, generation.DialogueProfile,
				dialogue.Rate, dialogue.Volume, dialogue.Pitch); err != nil {
				return fmt.Errorf("store episode dialogue: %w", err)
			}
			for position, speaker := range dialogue.Speakers {
				voice, err := config.ParseSpeakerVoice(speaker.Voice)
				if err != nil {
					return fmt.Errorf("resolve episode speaker %s voice: %w", speaker.ID, err)
				}
				providerVoice := voice.Voice
				if voice.Talker != "" {
					providerVoice += ":" + voice.Talker
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO episode_speakers (episode_id, position, speaker_id, name, role, tts_service, voice)
					VALUES ($1, $2, $3, $4, $5, $6, $7)
				`, insertedEpisodeID, position, speaker.ID, speaker.Name, speaker.Role, voice.Service, providerVoice); err != nil {
					return fmt.Errorf("store episode speaker: %w", err)
				}
			}
			if _, err := w.River.InsertTx(ctx, tx, ResolveContentArgs{EpisodeID: insertedEpisodeID}, nil); err != nil {
				return fmt.Errorf("enqueue content resolution: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE source_runs
		SET status = 'completed', items_found = $2, items_new = $3,
		    items_existing = $4, error = '', completed_at = now()
		WHERE id = $1
	`, args.RunID, limit, itemsNew, itemsExisting); err != nil {
		return fmt.Errorf("complete source run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit source run: %w", err)
	}
	return nil
}

func feedItemID(sourceID string, item *gofeed.Item) string {
	if item.GUID != "" {
		return item.GUID
	}
	if item.Link != "" {
		return item.Link
	}
	sum := sha256.Sum256([]byte(sourceID + "\x00" + item.Title + "\x00" + item.Published))
	return "sha256:" + hex.EncodeToString(sum[:])
}
