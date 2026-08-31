package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	webui "github.com/synrise25/rss-pod/web"
)

func playerWebHandler() http.Handler {
	files, err := fs.Sub(webui.Files, ".")
	if err != nil {
		slog.Error("prepare embedded player UI", "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "player UI unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(files))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
