package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/database"
	"github.com/synrise25/rss-pod/internal/jobs"
)

const MaxManualPollTimes = 100

type QueuedPoll struct {
	SourceID string    `json:"source_id"`
	Number   int       `json:"number"`
	RunID    uuid.UUID `json:"run_id"`
	JobID    int64     `json:"job_id"`
}

func ParsePollSources(cfg *config.Config, value string) ([]config.SourceConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("--sources is required")
	}
	if value == "all" {
		sources := make([]config.SourceConfig, 0, len(cfg.Sources))
		for _, source := range cfg.Sources {
			if source.Enabled {
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("configuration has no enabled sources")
		}
		return sources, nil
	}

	seen := make(map[string]struct{})
	sources := make([]config.SourceConfig, 0)
	for _, sourceID := range strings.Split(value, ",") {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "all" {
			return nil, fmt.Errorf("source %q cannot be combined with other source IDs", sourceID)
		}
		source, ok := cfg.Source(sourceID)
		if !ok {
			return nil, fmt.Errorf("unknown source %q", sourceID)
		}
		if !source.Enabled {
			return nil, fmt.Errorf("source %q is disabled", sourceID)
		}
		if _, duplicate := seen[sourceID]; duplicate {
			continue
		}
		seen[sourceID] = struct{}{}
		sources = append(sources, source)
	}
	return sources, nil
}

func EnqueuePolls(
	ctx context.Context,
	cfg *config.Config,
	sources []config.SourceConfig,
	times int,
	limit int,
) ([]QueuedPoll, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("at least one source is required")
	}
	if times < 1 || times > MaxManualPollTimes {
		return nil, fmt.Errorf("times must be between 1 and %d", MaxManualPollTimes)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit must be zero or positive")
	}
	for _, source := range sources {
		maxLimit := cfg.EffectiveLimits(source).MaxFeedItemsPerRun
		if limit > maxLimit {
			return nil, fmt.Errorf("limit for source %q must be between 1 and %d, or zero to use the configured maximum", source.ID, maxLimit)
		}
	}

	pool, err := database.Open(ctx, cfg.Runtime.Database)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	client, err := newRiverClient(cfg, pool, nil)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin poll batch: %w", err)
	}
	defer tx.Rollback(ctx)

	queued := make([]QueuedPoll, 0, len(sources)*times)
	for _, source := range sources {
		for number := 1; number <= times; number++ {
			result, err := jobs.EnqueuePoll(ctx, tx, client, source.ID, limit)
			if err != nil {
				return nil, fmt.Errorf("source %s poll %d: %w", source.ID, number, err)
			}
			queued = append(queued, QueuedPoll{
				SourceID: result.SourceID,
				Number:   number,
				RunID:    result.RunID,
				JobID:    result.JobID,
			})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit poll batch: %w", err)
	}
	return queued, nil
}
