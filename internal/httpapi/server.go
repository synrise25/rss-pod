package httpapi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/synrise25/rss-pod/internal/config"
	"github.com/synrise25/rss-pod/internal/jobs"
)

type Server struct {
	config         *config.Config
	pool           *pgxpool.Pool
	river          *river.Client[pgx.Tx]
	playerHTTP     *http.Server
	managementHTTP *http.Server
}

func New(cfg *config.Config, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *Server {
	s := &Server{config: cfg, pool: pool, river: riverClient}
	player := newPlayerServer(cfg, pool)
	s.playerHTTP = newHTTPServer(cfg.Runtime.HTTP.Listen, newPlayerMux(player))
	s.managementHTTP = newHTTPServer(cfg.Runtime.HTTP.ManagementAddress(), newManagementMux(s))
	return s
}

func newPlayerMux(player *playerServer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/player/sources", player.listSources)
	mux.HandleFunc("GET /api/v1/player/episodes", player.listEpisodes)
	mux.HandleFunc("GET /api/v1/player/notice", player.notice)
	mux.Handle("GET /", playerWebHandler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return mux
}

func newManagementMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/sources", s.listSources)
	mux.HandleFunc("POST /api/v1/sources/{sourceID}/poll", s.pollSource)
	mux.HandleFunc("GET /api/v1/sources/{sourceID}/podcast.xml", s.podcastFeed)
	mux.HandleFunc("GET /api/v1/runs/{runID}", s.getRun)
	mux.HandleFunc("GET /api/v1/feed-items", s.listFeedItems)
	mux.HandleFunc("GET /api/v1/episodes", s.listEpisodes)
	mux.HandleFunc("GET /api/v1/episodes/{episodeID}", s.getEpisode)
	mux.HandleFunc("POST /api/v1/episodes/{episodeID}/retry", s.retryEpisode)
	return mux
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           requestLogger(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func (s *Server) Run(ctx context.Context) error {
	servers := []struct {
		name string
		http *http.Server
	}{
		{name: "player", http: s.playerHTTP},
		{name: "management", http: s.managementHTTP},
	}
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func() {
			slog.Info("HTTP server listening", "role", server.name, "address", server.http.Addr)
			err := server.http.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			if err != nil {
				err = fmt.Errorf("%s HTTP server: %w", server.name, err)
			}
			errCh <- err
		}()
	}

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		return errors.Join(err, s.shutdown())
	}
}

func (s *Server) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(
		s.playerHTTP.Shutdown(shutdownCtx),
		s.managementHTTP.Shutdown(shutdownCtx),
	)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) listSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.config.Sources})
}

func (s *Server) pollSource(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("sourceID")
	source, ok := s.config.Source(sourceID)
	if !ok {
		writeError(w, http.StatusNotFound, "source not found")
		return
	}
	if !source.Enabled {
		writeError(w, http.StatusConflict, "source is disabled")
		return
	}
	limit := 0
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		maxLimit := s.config.EffectiveLimits(source).MaxFeedItemsPerRun
		if err != nil || parsed < 1 || parsed > maxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
			return
		}
		limit = parsed
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	queued, err := jobs.EnqueuePoll(r.Context(), tx, s.river, sourceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": queued.RunID, "job_id": queued.JobID, "status": "queued"})
}

type episode struct {
	ID                   uuid.UUID  `json:"id"`
	SourceID             string     `json:"source_id"`
	FeedItemID           int64      `json:"feed_item_id"`
	Title                string     `json:"title"`
	Status               string     `json:"status"`
	LLMService           string     `json:"llm_service,omitempty"`
	AudioURL             string     `json:"audio_url,omitempty"`
	AudioByteSize        int64      `json:"audio_byte_size,omitempty"`
	AudioDurationSeconds int64      `json:"audio_duration_seconds,omitempty"`
	Error                string     `json:"error,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	PublishedAt          *time.Time `json:"published_at,omitempty"`
}

func (s *Server) listEpisodes(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	sourceID := r.URL.Query().Get("source_id")
	status := r.URL.Query().Get("status")
	var since any
	if value := r.URL.Query().Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		since = parsed
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, source_id, feed_item_id, title, status, llm_service,
		       audio_url, audio_byte_size, audio_duration_seconds, error, created_at, updated_at, published_at
		FROM episodes
		WHERE ($1 = '' OR source_id = $1)
		  AND ($2 = '' OR status = $2)
		  AND ($3::timestamptz IS NULL OR created_at >= $3)
		ORDER BY created_at DESC LIMIT $4
	`, sourceID, status, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	episodes := make([]episode, 0)
	for rows.Next() {
		var value episode
		if err := rows.Scan(&value.ID, &value.SourceID, &value.FeedItemID, &value.Title, &value.Status,
			&value.LLMService, &value.AudioURL, &value.AudioByteSize, &value.AudioDurationSeconds, &value.Error,
			&value.CreatedAt, &value.UpdatedAt, &value.PublishedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		episodes = append(episodes, value)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": episodes})
}

func (s *Server) getEpisode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("episodeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid episode ID")
		return
	}
	var value episode
	err = s.pool.QueryRow(r.Context(), `
		SELECT id, source_id, feed_item_id, title, status, llm_service,
		       audio_url, audio_byte_size, audio_duration_seconds, error, created_at, updated_at, published_at
		FROM episodes WHERE id = $1
	`, id).Scan(&value.ID, &value.SourceID, &value.FeedItemID, &value.Title, &value.Status,
		&value.LLMService, &value.AudioURL, &value.AudioByteSize, &value.AudioDurationSeconds, &value.Error,
		&value.CreatedAt, &value.UpdatedAt, &value.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "episode not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var profileID, rate, volume, pitch string
	err = s.pool.QueryRow(r.Context(), `
		SELECT profile_id, rate, volume, pitch
		FROM episode_dialogues WHERE episode_id = $1
	`, id).Scan(&profileID, &rate, &volume, &pitch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dialogue := map[string]any{
		"profile_id": profileID, "rate": rate, "volume": volume, "pitch": pitch,
	}
	speakerRows, err := s.pool.Query(r.Context(), `
		SELECT position, speaker_id, name, role, tts_service, voice
		FROM episode_speakers WHERE episode_id = $1 ORDER BY position
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	speakers := make([]map[string]any, 0)
	for speakerRows.Next() {
		var position int
		var speakerID, name, role, ttsService, voice string
		if err := speakerRows.Scan(&position, &speakerID, &name, &role, &ttsService, &voice); err != nil {
			speakerRows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		speakers = append(speakers, map[string]any{
			"position": position, "id": speakerID, "name": name, "role": role,
			"tts_service": ttsService, "voice": voice,
		})
	}
	if err := speakerRows.Err(); err != nil {
		speakerRows.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	speakerRows.Close()
	rows, err := s.pool.Query(r.Context(), `
		SELECT position, speaker_id, text FROM script_turns WHERE episode_id = $1 ORDER BY position
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	turns := make([]map[string]any, 0)
	for rows.Next() {
		var position int
		var speakerID, text string
		if err := rows.Scan(&position, &speakerID, &text); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		turns = append(turns, map[string]any{"position": position, "speaker_id": speakerID, "text": text})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"episode": value, "dialogue": dialogue, "speakers": speakers, "turns": turns,
	})
}

func (s *Server) retryEpisode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("episodeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid episode ID")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var status string
	var documents, turns int
	err = tx.QueryRow(r.Context(), `
		SELECT e.status,
		       (SELECT count(*) FROM documents d WHERE d.episode_id = e.id),
		       (SELECT count(*) FROM script_turns t WHERE t.episode_id = e.id)
		FROM episodes e WHERE e.id = $1 FOR UPDATE
	`, id).Scan(&status, &documents, &turns)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "episode not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "failed" {
		writeError(w, http.StatusConflict, "only failed episodes can be retried")
		return
	}

	var args river.JobArgs
	switch {
	case documents == 0:
		args = jobs.ResolveContentArgs{EpisodeID: id.String()}
	case turns == 0:
		args = jobs.GenerateScriptArgs{EpisodeID: id.String()}
	default:
		args = jobs.GenerateTTSArgs{EpisodeID: id.String()}
	}
	inserted, err := s.river.InsertTx(r.Context(), tx, args, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE episodes SET status = 'queued', error = '', updated_at = now() WHERE id = $1
	`, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"episode_id": id,
		"job_id":     inserted.Job.ID,
		"job_kind":   args.Kind(),
		"status":     "queued",
	})
}

type podcastRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Channel podcastChannel `xml:"channel"`
}

type podcastChannel struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	Description string        `xml:"description"`
	Items       []podcastItem `xml:"item"`
}

type podcastItem struct {
	Title       string           `xml:"title"`
	GUID        podcastGUID      `xml:"guid"`
	PubDate     string           `xml:"pubDate"`
	Description string           `xml:"description"`
	Enclosure   podcastEnclosure `xml:"enclosure"`
}

type podcastGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type podcastEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

func (s *Server) podcastFeed(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("sourceID")
	source, ok := s.config.Source(sourceID)
	if !ok {
		writeError(w, http.StatusNotFound, "source not found")
		return
	}
	maxAge, _ := time.ParseDuration(s.config.EffectivePodcast(source).MaxAge)
	cutoff := time.Now().Add(-maxAge)
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, title, audio_url, audio_byte_size, published_at
		FROM episodes
		WHERE source_id = $1 AND status = 'published' AND audio_url <> ''
		  AND published_at >= $2
		ORDER BY published_at DESC NULLS LAST
		LIMIT 100
	`, sourceID, cutoff)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := make([]podcastItem, 0)
	for rows.Next() {
		var id uuid.UUID
		var title, audioURL string
		var byteSize int64
		var publishedAt *time.Time
		if err := rows.Scan(&id, &title, &audioURL, &byteSize, &publishedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		pubDate := ""
		if publishedAt != nil {
			pubDate = publishedAt.Format(time.RFC1123Z)
		}
		items = append(items, podcastItem{
			Title:       title,
			GUID:        podcastGUID{IsPermaLink: "false", Value: id.String()},
			PubDate:     pubDate,
			Description: title,
			Enclosure:   podcastEnclosure{URL: audioURL, Length: byteSize, Type: "audio/mpeg"},
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}
	feedURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.Path)
	feed := podcastRSS{
		Version: "2.0",
		Channel: podcastChannel{
			Title:       source.Name,
			Link:        feedURL,
			Description: source.Name + " 自动生成的播客",
			Items:       items,
		},
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		slog.Error("write podcast XML header", "error", err)
		return
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(feed); err != nil {
		slog.Error("write podcast XML", "error", err)
	}
}

type sourceRun struct {
	ID            uuid.UUID  `json:"id"`
	SourceID      string     `json:"source_id"`
	Status        string     `json:"status"`
	ItemsFound    int        `json:"items_found"`
	ItemsNew      int        `json:"items_new"`
	ItemsExisting int        `json:"items_existing"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}
	var run sourceRun
	err = s.pool.QueryRow(r.Context(), `
		SELECT id, source_id, status, items_found, items_new, items_existing,
		       error, created_at, started_at, completed_at
		FROM source_runs WHERE id = $1
	`, id).Scan(
		&run.ID, &run.SourceID, &run.Status, &run.ItemsFound, &run.ItemsNew,
		&run.ItemsExisting, &run.Error, &run.CreatedAt, &run.StartedAt, &run.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

type feedItem struct {
	ID           int64      `json:"id"`
	SourceID     string     `json:"source_id"`
	ExternalID   string     `json:"external_id"`
	Title        string     `json:"title"`
	Link         string     `json:"link"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	DiscoveredAt time.Time  `json:"discovered_at"`
}

func (s *Server) listFeedItems(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	sourceID := r.URL.Query().Get("source_id")
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, source_id, external_id, title, link, published_at, discovered_at
		FROM feed_items
		WHERE ($1 = '' OR source_id = $1)
		ORDER BY published_at DESC NULLS LAST, discovered_at DESC
		LIMIT $2
	`, sourceID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := make([]feedItem, 0)
	for rows.Next() {
		var item feedItem
		if err := rows.Scan(&item.ID, &item.SourceID, &item.ExternalID, &item.Title, &item.Link, &item.PublishedAt, &item.DiscoveredAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write response", "error", err)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("HTTP request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}
