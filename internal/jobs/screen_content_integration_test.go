package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/database"
)

func TestScreeningPipelineIntegration(t *testing.T) {
	for _, decision := range []string{"allow", "skip", "disabled", "invalid"} {
		t.Run(decision, func(t *testing.T) {
			pool := jobsTestPool(t)
			ctx := context.Background()
			// Migrations must remain repeatable after upgrading the status constraint.
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := `{"decision":"` + decision + `","reason":"test reason"}`
				if decision == "invalid" {
					content = `{broken`
				}
				writeScreeningCompletion(w, content)
			}))
			defer server.Close()
			cfg := screeningTestConfig(server.URL)
			cfg.Defaults.Content = config.ContentConfig{Type: "rss-item"}
			if decision == "disabled" {
				disabled := false
				cfg.Sources[0].Screening = &config.ScreeningConfig{Enabled: &disabled}
			}
			client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
			if err != nil {
				t.Fatal(err)
			}
			episodeID := seedRetryingEpisode(t, pool, decision)
			resolveJob, err := client.Insert(ctx, ResolveContentArgs{EpisodeID: episodeID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			resolver := &ResolveContentWorker{Pool: pool, Config: cfg, River: client}
			if err := resolver.Work(ctx, &river.Job[ResolveContentArgs]{JobRow: resolveJob.Job, Args: ResolveContentArgs{EpisodeID: episodeID}}); err != nil {
				t.Fatal(err)
			}
			var activeID int64
			var kind string
			if err := pool.QueryRow(ctx, `SELECT e.active_job_id,j.kind FROM episodes e JOIN river_job j ON j.id=e.active_job_id WHERE e.id=$1`, episodeID).Scan(&activeID, &kind); err != nil {
				t.Fatal(err)
			}
			if decision == "disabled" {
				if kind != "generate_script" || calls != 0 {
					t.Fatalf("disabled kind=%s calls=%d", kind, calls)
				}
				return
			}
			if kind != "screen_content" {
				t.Fatalf("next kind=%s", kind)
			}
			worker := &ScreenContentWorker{Pool: pool, Config: cfg, River: client}
			job := &river.Job[ScreenContentArgs]{JobRow: &rivertype.JobRow{ID: activeID, Attempt: 4, MaxAttempts: 4}, Args: ScreenContentArgs{EpisodeID: episodeID}}
			err = worker.Work(ctx, job)
			if decision == "invalid" {
				if err == nil {
					t.Fatal("invalid response succeeded")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var status string
			var scripts, screenings int
			if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM river_job WHERE kind='generate_script' AND args->>'episode_id'=$1::text),
    (SELECT count(*) FROM episode_screenings WHERE episode_id=$1::uuid) FROM episodes WHERE id=$1::uuid`, episodeID).Scan(&status, &scripts, &screenings); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantScripts, wantScreenings := "content_ready", 1, 1
			if decision == "skip" {
				wantStatus, wantScripts = "skipped", 0
			}
			if decision == "invalid" {
				wantStatus, wantScripts, wantScreenings = "failed", 0, 0
			}
			if status != wantStatus || scripts != wantScripts || screenings != wantScreenings || calls != 1 {
				t.Fatalf("status=%s scripts=%d screenings=%d calls=%d", status, scripts, screenings, calls)
			}
			// Model queue cleanup before manual recovery.
			if _, err := pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'episode_id'=$1`, episodeID); err != nil {
				t.Fatal(err)
			}
			if decision == "allow" {
				if _, err := pool.Exec(ctx, `UPDATE episodes SET status='failed' WHERE id=$1`, episodeID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			resumed, err := ResumeEpisode(ctx, tx, client, episodeID, cfg)
			if decision == "skip" {
				if !errors.Is(err, ErrEpisodeNotRetryable) {
					t.Fatalf("skip resume err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if resumed.JobKind != "screen_content" {
				t.Fatalf("resume bypassed screening: %s", resumed.JobKind)
			}
			if decision == "allow" {
				if err := worker.Work(ctx, &river.Job[ScreenContentArgs]{JobRow: &rivertype.JobRow{ID: resumed.JobID, Attempt: 1, MaxAttempts: 4}, Args: job.Args}); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("successful screening was billed again on retry")
				}
			}
		})
	}
}

func TestQueuedScriptCannotBypassScreeningIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	ctx := context.Background()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeScreeningCompletion(w, `{"decision":"allow","reason":"substantive discussion"}`)
	}))
	defer llm.Close()
	cfg := screeningTestConfig(llm.URL)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := seedRetryingEpisode(t, pool, "queued-script")
	if _, err := pool.Exec(ctx, `INSERT INTO documents (episode_id,position,content) VALUES ($1,0,'content')`, id); err != nil {
		t.Fatal(err)
	}
	inserted, err := client.Insert(ctx, GenerateScriptArgs{EpisodeID: id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the routing script job running while screening finishes. River's
	// struct tags do not enable uniqueness without nonzero UniqueOpts.
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state='running', attempt=1, attempted_at=now() WHERE id=$1`, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	worker := &GenerateScriptWorker{Pool: pool, Config: cfg, River: client}
	if err := worker.Work(ctx, &river.Job[GenerateScriptArgs]{JobRow: inserted.Job, Args: GenerateScriptArgs{EpisodeID: id}}); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := pool.QueryRow(ctx, `SELECT j.kind FROM episodes e JOIN river_job j ON j.id=e.active_job_id WHERE e.id=$1`, id).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "screen_content" {
		t.Fatalf("kind=%s", kind)
	}
	var screeningJobID int64
	if err := pool.QueryRow(ctx, `SELECT active_job_id FROM episodes WHERE id=$1`, id).Scan(&screeningJobID); err != nil {
		t.Fatal(err)
	}
	screeningWorker := &ScreenContentWorker{Pool: pool, Config: cfg, River: client}
	if err := screeningWorker.Work(ctx, &river.Job[ScreenContentArgs]{JobRow: &rivertype.JobRow{ID: screeningJobID, Attempt: 1, MaxAttempts: 4}, Args: ScreenContentArgs{EpisodeID: id}}); err != nil {
		t.Fatal(err)
	}
	var nextJobID int64
	var nextState, oldState string
	if err := pool.QueryRow(ctx, `SELECT j.id, j.kind, j.state FROM episodes e JOIN river_job j ON j.id=e.active_job_id WHERE e.id=$1`, id).Scan(&nextJobID, &kind, &nextState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, inserted.Job.ID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if nextJobID == inserted.Job.ID || kind != "generate_script" || nextState != "available" || oldState != "running" {
		t.Fatalf("handoff reused running job: next=%d kind=%s state=%s old=%d/%s", nextJobID, kind, nextState, inserted.Job.ID, oldState)
	}
	// The original script attempt must no longer own the episode.
	if err := worker.Work(ctx, &river.Job[GenerateScriptArgs]{JobRow: inserted.Job, Args: GenerateScriptArgs{EpisodeID: id}}); !errors.Is(err, errEpisodeAttemptSuperseded) {
		t.Fatalf("stale script err=%v", err)
	}
}

func TestScreeningMigratesExistingEpisodeConstraintIntegration(t *testing.T) {
	pool := jobsTestPool(t)
	ctx := context.Background()
	id := seedRetryingEpisode(t, pool, "old-schema")
	if _, err := pool.Exec(ctx, `ALTER TABLE episodes DROP CONSTRAINT episodes_status_check;
 ALTER TABLE episodes ADD CONSTRAINT episodes_status_check CHECK (status IN (
 'queued', 'resolving_content', 'content_ready', 'generating_script', 'script_ready',
 'generating_tts', 'composing', 'published', 'retrying', 'failed'))`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE episodes SET status='skipped' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestJevPersistenceAndRoutingIntegration(t *testing.T) {
	for _, mode := range []string{"allow", "skip", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			pool := jobsTestPool(t)
			ctx := context.Background()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/systemone" {
					if mode == "fallback" {
						w.WriteHeader(400)
						w.Write([]byte(`{"detail":{"error_type":"max_tokens_exceeded"}}`))
						return
					}
					probability := "0.1"
					if mode == "skip" {
						probability = "0.9"
					}
					w.Write([]byte(`{"model":"jev-resolved","answers":{"skip":{"type":"noul","noul":` + probability + `}}}`))
					return
				}
				writeScreeningCompletion(w, `{"decision":"allow","reason":"fallback"}`)
			}))
			defer server.Close()
			cfg := screeningTestConfig(server.URL)
			cfg.Defaults.Screening.Services = []string{"jev", "llm.cheap"}
			cfg.Services.Jev = config.LLMService{BaseURL: server.URL, Model: "jev-configured", Timeout: "1s"}
			client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
			if err != nil {
				t.Fatal(err)
			}
			id := seedRetryingEpisode(t, pool, "jev-"+mode)
			if _, err := pool.Exec(ctx, `INSERT INTO documents (episode_id,position,content) VALUES ($1,0,'substantive content')`, id); err != nil {
				t.Fatal(err)
			}
			job, err := client.Insert(ctx, ScreenContentArgs{EpisodeID: id}, nil)
			if err != nil {
				t.Fatal(err)
			}
			worker := &ScreenContentWorker{Pool: pool, Config: cfg, River: client}
			if err := worker.Work(ctx, &river.Job[ScreenContentArgs]{JobRow: job.Job, Args: ScreenContentArgs{EpisodeID: id}}); err != nil {
				t.Fatal(err)
			}
			var backend, service, model, status string
			var probability *float64
			if err := pool.QueryRow(ctx, `SELECT sc.backend,sc.service,sc.model,sc.skip_probability,e.status FROM episode_screenings sc JOIN episodes e ON e.id=sc.episode_id WHERE e.id=$1`, id).Scan(&backend, &service, &model, &probability, &status); err != nil {
				t.Fatal(err)
			}
			if mode == "fallback" {
				if backend != "llm" || service != "llm.cheap" || probability != nil || calls != 2 {
					t.Fatalf("fallback backend=%s service=%s probability=%v calls=%d", backend, service, probability, calls)
				}
			} else if backend != "jev" || service != "jev" || model != "jev-resolved" || probability == nil {
				t.Fatalf("metadata %s %s %s %v", backend, service, model, probability)
			}
			if mode == "skip" && status != "skipped" || mode != "skip" && status != "content_ready" {
				t.Fatalf("status=%s", status)
			}
			input := makeScreeningInput(cfg, cfg.Sources[0], []llmDocument{{Position: 0, Content: "substantive content"}})
			cached, _, _, err := loadScreening(ctx, pool, id, input.Hash)
			if err != nil || cached.Service != service || cached.Model != model {
				t.Fatalf("cache=%+v err=%v", cached, err)
			}
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
		})
	}
}
