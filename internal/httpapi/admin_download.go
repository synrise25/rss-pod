package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const adminDownloadTimeout = 5 * time.Minute

var adminDownloadClient = &http.Client{Timeout: adminDownloadTimeout}

func (s *adminServer) downloadEpisode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("episodeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid episode ID")
		return
	}
	var title, audioURL string
	err = s.pool.QueryRow(r.Context(), `SELECT title, audio_url FROM episodes
        WHERE id=$1 AND status='published' AND audio_url <> ''`, id).Scan(&title, &audioURL)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "published episode not found")
		return
	}
	if err != nil {
		s.unavailable(w, err)
		return
	}
	// Large downloads need more time than the normal API's 30-second write limit.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(adminDownloadTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.unavailable(w, err)
		return
	}
	// Proxy the stored media URL so cross-origin audio downloads do not depend
	// on the media host's CORS or Content-Disposition configuration.
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, audioURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "audio unavailable")
		return
	}
	response, err := adminDownloadClient.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "audio unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "audio unavailable")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": audioDownloadFilename(title, id.String()),
	}))
	if response.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		slog.Warn("stream admin audio download", "episode_id", id, "error", err)
	}
}

func audioDownloadFilename(title, fallback string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, title)
	name = strings.Trim(name, " .")
	runes := []rune(name)
	if len(runes) > 80 {
		name = strings.TrimRight(string(runes[:80]), " .")
	}
	if name == "" {
		name = fallback
	}
	return name + ".mp3"
}
