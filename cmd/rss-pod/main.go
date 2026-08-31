package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/synrise25/rss-pod/internal/app"
	"github.com/synrise25/rss-pod/internal/checker"
	"github.com/synrise25/rss-pod/internal/config"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		return errors.New("missing command")
	}

	switch os.Args[1] {
	case "check":
		return runCheck(ctx, os.Args[2:])
	case "migrate":
		return runMigrate(ctx, os.Args[2:])
	case "poll":
		return runPoll(ctx, os.Args[2:])
	case "serve":
		return runServe(ctx, os.Args[2:])
	case "worker":
		return runWorker(ctx, os.Args[2:])
	case "run":
		return runCombined(ctx, os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func loadConfig(args []string, command string) (*config.Config, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "configuration file")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return config.Load(*configPath)
}

func runMigrate(ctx context.Context, args []string) error {
	cfg, err := loadConfig(args, "migrate")
	if err != nil {
		return err
	}
	if err := app.Migrate(ctx, cfg); err != nil {
		return err
	}
	slog.Info("database migrations complete")
	return nil
}

func runServe(ctx context.Context, args []string) error {
	cfg, err := loadConfig(args, "serve")
	if err != nil {
		return err
	}
	return app.Serve(ctx, cfg)
}

func runWorker(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "configuration file")
	queuesValue := flags.String("queues", "", "comma-separated queues; defaults to all configured queues")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	queues, err := app.ParseQueues(cfg, *queuesValue)
	if err != nil {
		return err
	}
	return app.Worker(ctx, cfg, queues)
}

func runCombined(ctx context.Context, args []string) error {
	cfg, err := loadConfig(args, "run")
	if err != nil {
		return err
	}
	return app.Run(ctx, cfg)
}

func runPoll(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("poll", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "configuration file")
	sourcesValue := flags.String("sources", "", "comma-separated source IDs, or all")
	times := flags.Int("times", 1, "number of polls to enqueue per source")
	limit := flags.Int("limit", 0, "maximum feed items per poll; zero uses each source configuration")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	sources, err := app.ParsePollSources(cfg, *sourcesValue)
	if err != nil {
		return err
	}
	queued, err := app.EnqueuePolls(ctx, cfg, sources, *times, *limit)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(queued)
	}
	for _, poll := range queued {
		fmt.Printf("queued source=%s number=%d run_id=%s job_id=%d\n", poll.SourceID, poll.Number, poll.RunID, poll.JobID)
	}
	return nil
}

func runCheck(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "configuration file")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	results := checker.Run(ctx, cfg)
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			status := "OK"
			if !result.OK {
				status = "FAIL"
			}
			fmt.Printf("%-8s %-5s %s (%s)\n", result.Name, status, result.Detail, result.Duration.Round(1_000_000))
		}
	}
	if !checker.AllOK(results) {
		return errors.New("one or more infrastructure checks failed")
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `rss-pod commands:
  check    validate configuration and external services
  migrate  apply River and application database migrations
  poll     explicitly enqueue one or more source polls
  serve    run the HTTP service only
  worker   run selected River queues only
  run      run the HTTP service and all River queues`)
}
