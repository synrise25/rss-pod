package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	timezone   string
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
		timezone:   cfg.Defaults.Schedule.Timezone,
		sources:    sources,
		noticeFile: strings.TrimSpace(cfg.Runtime.HTTP.NoticeFile),
	}
}

func (s *playerServer) listSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.sources, "timezone": s.dateTimezone()})
}

func (s *playerServer) notice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.noticeFile == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	file, err := os.Open(s.noticeFile)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		slog.Error("open player notice", "error", err)
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

	noticeHash := sha256.Sum256(rendered.Bytes())
	noticeID := hex.EncodeToString(noticeHash[:])
	etag := `"` + noticeID + `"`
	w.Header().Set("X-Notice-ID", noticeID)
	w.Header().Set("ETag", etag)
	if matchesETag(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rendered.Bytes())
}

func matchesETag(headerValues []string, current string) bool {
	current = strings.TrimPrefix(current, "W/")
	for _, headerValue := range headerValues {
		for _, candidate := range strings.Split(headerValue, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == current {
				return true
			}
		}
	}
	return false
}

type playerEpisode struct {
	EditionDate          string     `json:"edition_date"`
	Hidden               bool       `json:"hidden,omitempty"`
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
	s.episodes(w, r, false)
}

func (s *playerServer) episodes(w http.ResponseWriter, r *http.Request, includeHidden bool) {
	limit := 200
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}

	since, ok := s.parseEditionBoundary(w, r.URL.Query().Get("since"), "since")
	if !ok {
		return
	}
	before, ok := s.parseEditionBoundary(w, r.URL.Query().Get("before"), "before")
	if !ok {
		return
	}
	if since != "" && before != "" && since >= before {
		writeError(w, http.StatusBadRequest, "since must be before before")
		return
	}

	sourceID := r.URL.Query().Get("source_id")
	rows, err := s.pool.Query(r.Context(), `
		WITH dated_episodes AS (
		    -- Temporary compatibility for episodes created before edition dates.
		    SELECT *, COALESCE(edition_date, (created_at AT TIME ZONE $6)::date) AS display_date
		    FROM episodes
		)
		SELECT e.id, e.source_id, e.title, e.audio_url, e.audio_byte_size, e.audio_duration_seconds,
		       e.published_at, f.published_at, e.hidden_at IS NOT NULL, to_char(e.display_date, 'YYYY-MM-DD')
		FROM dated_episodes e
		JOIN feed_items f ON f.id = e.feed_item_id
		WHERE e.status = 'published' AND e.audio_url <> ''
		  AND ($5 OR e.hidden_at IS NULL)
		  AND ($1 = '' OR e.source_id = $1)
		  AND (NULLIF($2, '')::date IS NULL OR e.display_date >= NULLIF($2, '')::date)
		  AND (NULLIF($3, '')::date IS NULL OR e.display_date < NULLIF($3, '')::date)
		ORDER BY e.display_date DESC, e.published_at DESC NULLS LAST, e.id
		LIMIT $4
	`, sourceID, since, before, limit, includeHidden, s.dateTimezone())
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
			&episode.Hidden,
			&episode.EditionDate,
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

func (s *playerServer) dateTimezone() string {
	if s.timezone == "" {
		return "UTC"
	}
	return s.timezone
}

// Date-only boundaries use edition days; RFC3339 remains accepted for API clients.
func (s *playerServer) parseEditionBoundary(w http.ResponseWriter, value, field string) (string, bool) {
	if value == "" {
		return "", true
	}
	if date, err := time.Parse("2006-01-02", value); err == nil {
		return date.Format("2006-01-02"), true
	}
	stamp, ok := parseOptionalRFC3339(w, value, field)
	if !ok {
		return "", false
	}
	location, err := time.LoadLocation(s.dateTimezone())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invalid player timezone")
		return "", false
	}
	return stamp.In(location).Format("2006-01-02"), true
}
