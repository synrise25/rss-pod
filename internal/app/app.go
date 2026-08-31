package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/database"
	"github.com/synrise25/rss-pod/internal/httpapi"
	"github.com/synrise25/rss-pod/internal/jobs"
	"github.com/synrise25/rss-pod/internal/storage"
)

func Migrate(ctx context.Context, cfg *config.Config) error {
	pool, err := database.Open(ctx, cfg.Runtime.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	return database.Migrate(ctx, pool)
}

func Serve(ctx context.Context, cfg *config.Config) error {
	pool, err := database.Open(ctx, cfg.Runtime.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := newRiverClient(cfg, pool, nil)
	if err != nil {
		return err
	}
	return httpapi.New(cfg, pool, client).Run(ctx)
}

func Worker(ctx context.Context, cfg *config.Config, queueNames []string) error {
	pool, err := database.Open(ctx, cfg.Runtime.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	client, err := newRiverClient(cfg, pool, queueNames)
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start River worker: %w", err)
	}
	slog.Info("worker started", "queues", queueNames)
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Stop(stopCtx)
}

func Run(ctx context.Context, cfg *config.Config) error {
	pool, err := database.Open(ctx, cfg.Runtime.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	queueNames := configuredQueues(cfg)
	client, err := newRiverClient(cfg, pool, queueNames)
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start River worker: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Stop(stopCtx); err != nil {
			slog.Error("stop River worker", "error", err)
		}
	}()

	return httpapi.New(cfg, pool, client).Run(ctx)
}

func newRiverClient(cfg *config.Config, pool *pgxpool.Pool, queueNames []string) (*river.Client[pgx.Tx], error) {
	jobTimeout, err := cfg.Runtime.Jobs.TimeoutDuration()
	if err != nil {
		return nil, fmt.Errorf("parse River job timeout: %w", err)
	}
	fetchPollInterval, err := cfg.Runtime.Jobs.FetchPollIntervalDuration()
	if err != nil {
		return nil, fmt.Errorf("parse River fetch poll interval: %w", err)
	}
	var storageClient *storage.Client
	if slices.Contains(queueNames, "tts") || slices.Contains(queueNames, "media") {
		var err error
		storageClient, err = storage.New(cfg.Runtime.Storage)
		if err != nil {
			return nil, err
		}
	}
	workers := river.NewWorkers()
	cleanupDataWorker := &jobs.CleanupDataWorker{Pool: pool}
	scheduleSourcesWorker := &jobs.ScheduleSourcesWorker{Pool: pool, Config: cfg}
	pollSourceWorker := &jobs.PollSourceWorker{Pool: pool, Config: cfg}
	resolveContentWorker := &jobs.ResolveContentWorker{Pool: pool, Config: cfg}
	generateScriptWorker := &jobs.GenerateScriptWorker{Pool: pool, Config: cfg}
	generateTTSWorker := &jobs.GenerateTTSWorker{Pool: pool, Config: cfg, Storage: storageClient}
	composeEpisodeWorker := &jobs.ComposeEpisodeWorker{Pool: pool, Storage: storageClient}
	river.AddWorker(workers, cleanupDataWorker)
	river.AddWorker(workers, scheduleSourcesWorker)
	river.AddWorker(workers, pollSourceWorker)
	river.AddWorker(workers, resolveContentWorker)
	river.AddWorker(workers, generateScriptWorker)
	river.AddWorker(workers, generateTTSWorker)
	river.AddWorker(workers, composeEpisodeWorker)
	riverConfig := &river.Config{
		Workers:           workers,
		JobTimeout:        jobTimeout,
		FetchPollInterval: fetchPollInterval,
	}
	if len(queueNames) > 0 {
		riverConfig.Queues = make(map[string]river.QueueConfig, len(queueNames))
		for _, name := range queueNames {
			queue, ok := cfg.Runtime.Jobs.Queues[name]
			if !ok {
				return nil, fmt.Errorf("unknown queue %q", name)
			}
			riverConfig.Queues[name] = river.QueueConfig{MaxWorkers: queue.Concurrency}
		}
		riverConfig.PeriodicJobs = []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(24*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.CleanupDataArgs{}, nil },
				&river.PeriodicJobOpts{ID: "data-cleanup", RunOnStart: true},
			),
			river.NewPeriodicJob(
				river.PeriodicInterval(time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.ScheduleSourcesArgs{}, nil },
				&river.PeriodicJobOpts{ID: "source-scheduler", RunOnStart: true},
			),
		}
	}
	client, err := river.NewClient(riverpgxv5.New(pool), riverConfig)
	if err != nil {
		return nil, fmt.Errorf("create River client: %w", err)
	}
	scheduleSourcesWorker.River = client
	pollSourceWorker.River = client
	resolveContentWorker.River = client
	generateScriptWorker.River = client
	generateTTSWorker.River = client
	return client, nil
}

func configuredQueues(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Runtime.Jobs.Queues))
	for name := range cfg.Runtime.Jobs.Queues {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func ParseQueues(cfg *config.Config, value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return configuredQueues(cfg), nil
	}
	seen := make(map[string]struct{})
	var queues []string
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if _, ok := cfg.Runtime.Jobs.Queues[name]; !ok {
			return nil, fmt.Errorf("unknown queue %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		queues = append(queues, name)
	}
	return queues, nil
}
