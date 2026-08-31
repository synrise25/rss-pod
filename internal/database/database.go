package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/synrise25/rss-pod/internal/config"
)

func Open(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, cfg.URL())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate River: %w", err)
	}
	if _, err := pool.Exec(ctx, applicationSchema); err != nil {
		return fmt.Errorf("migrate application schema: %w", err)
	}
	return nil
}

const applicationSchema = `
CREATE TABLE IF NOT EXISTS source_runs (
    id          uuid PRIMARY KEY,
    source_id   text NOT NULL,
    status      text NOT NULL CHECK (status IN ('queued', 'running', 'retrying', 'completed', 'failed')),
    items_found integer NOT NULL DEFAULT 0,
    items_new integer NOT NULL DEFAULT 0,
    items_existing integer NOT NULL DEFAULT 0,
    error       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    completed_at timestamptz,
    scheduled_for timestamptz
);

CREATE INDEX IF NOT EXISTS source_runs_source_created_idx
    ON source_runs (source_id, created_at DESC);

CREATE INDEX IF NOT EXISTS source_runs_created_idx
    ON source_runs (created_at, id);

CREATE UNIQUE INDEX IF NOT EXISTS source_runs_source_scheduled_idx
    ON source_runs (source_id, scheduled_for) WHERE scheduled_for IS NOT NULL;

CREATE TABLE IF NOT EXISTS scheduler_state (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    last_checked_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS feed_items (
    id           bigserial PRIMARY KEY,
    source_id    text NOT NULL,
    external_id  text NOT NULL,
    title        text NOT NULL DEFAULT '',
    link         text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    content      text NOT NULL DEFAULT '',
    published_at timestamptz,
    discovered_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, external_id)
);

CREATE INDEX IF NOT EXISTS feed_items_source_published_idx
    ON feed_items (source_id, published_at DESC NULLS LAST, discovered_at DESC);

CREATE INDEX IF NOT EXISTS feed_items_discovered_idx
    ON feed_items (discovered_at, id);

CREATE TABLE IF NOT EXISTS episodes (
    id             uuid PRIMARY KEY,
    source_id      text NOT NULL,
    feed_item_id   bigint NOT NULL UNIQUE REFERENCES feed_items(id) ON DELETE CASCADE,
    title          text NOT NULL DEFAULT '',
    status         text NOT NULL CHECK (status IN (
                       'queued', 'resolving_content', 'content_ready',
                       'generating_script', 'script_ready', 'generating_tts',
                       'composing', 'published', 'retrying', 'failed'
    )),
    llm_service    text NOT NULL DEFAULT '',
    audio_object_key text NOT NULL DEFAULT '',
    audio_url      text NOT NULL DEFAULT '',
    audio_byte_size bigint NOT NULL DEFAULT 0,
    audio_duration_seconds integer NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz
);

CREATE INDEX IF NOT EXISTS episodes_source_created_idx
    ON episodes (source_id, created_at DESC);

CREATE TABLE IF NOT EXISTS episode_dialogues (
    episode_id       uuid PRIMARY KEY REFERENCES episodes(id) ON DELETE CASCADE,
    profile_id       text NOT NULL,
    rate             text NOT NULL,
    volume           text NOT NULL,
    pitch            text NOT NULL
);

CREATE TABLE IF NOT EXISTS episode_speakers (
    episode_id  uuid NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position    integer NOT NULL,
    speaker_id  text NOT NULL,
    name        text NOT NULL,
    role        text NOT NULL,
    tts_service text NOT NULL,
    voice       text NOT NULL,
    PRIMARY KEY (episode_id, speaker_id),
    UNIQUE (episode_id, position)
);

CREATE TABLE IF NOT EXISTS documents (
    id          bigserial PRIMARY KEY,
    episode_id  uuid NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position    integer NOT NULL,
    title       text NOT NULL DEFAULT '',
    source_url  text NOT NULL DEFAULT '',
    content     text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (episode_id, position)
);

CREATE TABLE IF NOT EXISTS episode_scripts (
    episode_id   uuid PRIMARY KEY REFERENCES episodes(id) ON DELETE CASCADE,
    llm_service  text NOT NULL,
    title        text NOT NULL DEFAULT '',
    raw_response jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS script_turns (
    id          bigserial PRIMARY KEY,
    episode_id  uuid NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position    integer NOT NULL,
    speaker_id  text NOT NULL,
    text        text NOT NULL,
    UNIQUE (episode_id, position)
);

CREATE TABLE IF NOT EXISTS audio_segments (
    id          bigserial PRIMARY KEY,
    episode_id  uuid NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position    integer NOT NULL,
    tts_service text NOT NULL,
    object_key  text NOT NULL,
    byte_size   bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (episode_id, position)
);

`
