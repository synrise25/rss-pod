package httpapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/synrise25/rss-pod/internal/config"
)

type playerServer struct {
	pool       *pgxpool.Pool
	sources    []playerSource
	noticeFile string
}

const maxNoticeBytes = 64 << 10

var noticeMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

type playerSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newPlayerServer(cfg *config.Config, pool *pgxpool.Pool) *playerServer {
	sources := make([]playerSource, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if !source.Enabled {
			continue
		}
		sources = append(sources, playerSource{ID: source.ID, Name: source.Name})
	}
	return &playerServer{
		pool:       pool,
		sources:    sources,
		noticeFile: strings.TrimSpace(cfg.Runtime.HTTP.NoticeFile),
	}
}

func (s *playerServer) listSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.sources})
}

func (s *playerServer) notice(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.noticeFile == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	file, err := os.Open(s.noticeFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("open player notice", "error", err)
		}
		http.Error(w, "player notice unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxNoticeBytes+1))
	if err != nil {
		slog.Error("read player notice", "error", err)
		http.Error(w, "player notice unavailable", http.StatusInternalServerError)
		return
	}
	if len(content) > maxNoticeBytes {
		http.Error(w, "player notice is too large", http.StatusInternalServerError)
		return
	}
	if len(bytes.TrimSpace(content)) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var rendered bytes.Buffer
	if err := noticeMarkdown.Convert(content, &rendered); err != nil {
		slog.Error("render player notice", "error", err)
		http.Error(w, "player notice unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rendered.Bytes())
}

type playerEpisode struct {
	ID                   uuid.UUID  `json:"id"`
	SourceID             string     `json:"source_id"`
	Title                string     `json:"title"`
	AudioURL             string     `json:"audio_url"`
	AudioByteSize        int64      `json:"audio_byte_size,omitempty"`
	AudioDurationSeconds int64      `json:"audio_duration_seconds,omitempty"`
	PublishedAt          *time.Time `json:"published_at,omitempty"`
	OriginalPublishedAt  *time.Time `json:"original_published_at,omitempty"`
}

func (s *playerServer) listEpisodes(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}

	since, ok := parseOptionalRFC3339(w, r.URL.Query().Get("since"), "since")
	if !ok {
		return
	}
	before, ok := parseOptionalRFC3339(w, r.URL.Query().Get("before"), "before")
	if !ok {
		return
	}
	if since != nil && before != nil && !since.Before(*before) {
		writeError(w, http.StatusBadRequest, "since must be before before")
		return
	}

	sourceID := r.URL.Query().Get("source_id")
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, e.source_id, e.title, e.audio_url, e.audio_byte_size, e.audio_duration_seconds,
		       e.published_at, f.published_at
		FROM episodes e
		JOIN feed_items f ON f.id = e.feed_item_id
		WHERE e.status = 'published' AND e.audio_url <> ''
		  AND ($1 = '' OR e.source_id = $1)
		  AND ($2::timestamptz IS NULL OR e.published_at >= $2)
		  AND ($3::timestamptz IS NULL OR e.published_at < $3)
		ORDER BY e.published_at DESC NULLS LAST
		LIMIT $4
	`, sourceID, since, before, limit)
	if err != nil {
		slog.Error("query player episodes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load episodes")
		return
	}
	defer rows.Close()

	episodes := make([]playerEpisode, 0)
	for rows.Next() {
		var episode playerEpisode
		if err := rows.Scan(
			&episode.ID,
			&episode.SourceID,
			&episode.Title,
			&episode.AudioURL,
			&episode.AudioByteSize,
			&episode.AudioDurationSeconds,
			&episode.PublishedAt,
			&episode.OriginalPublishedAt,
		); err != nil {
			slog.Error("scan player episode", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load episodes")
			return
		}
		episodes = append(episodes, episode)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterate player episodes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load episodes")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"episodes": episodes})
}

func parseOptionalRFC3339(w http.ResponseWriter, value, field string) (*time.Time, bool) {
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		writeError(w, http.StatusBadRequest, field+" must be an RFC3339 timestamp")
		return nil, false
	}
	return &parsed, true
}
