package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
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

		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Vary", "Accept-Language")
			target := preferredPlayerPath(r.Header.Get("Accept-Language"))
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if r.URL.Path == "/zh-cn" || r.URL.Path == "/zh-cn/" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if r.URL.Path == "/en" || r.URL.Path == "/en/" {
			w.Header().Set("Content-Language", "en")
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func preferredPlayerPath(acceptLanguage string) string {
	preferredPath := "/en"
	preferredQuality := -1.0

	for _, languageRange := range strings.Split(acceptLanguage, ",") {
		parts := strings.Split(languageRange, ";")
		languageTag := strings.ToLower(strings.TrimSpace(parts[0]))

		path := ""
		switch {
		case languageTag == "zh" || strings.HasPrefix(languageTag, "zh-"):
			path = "/zh-cn"
		case languageTag == "en" || strings.HasPrefix(languageTag, "en-"):
			path = "/en"
		default:
			continue
		}

		quality := 1.0
		for _, parameter := range parts[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(name, "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || parsed < 0 || parsed > 1 {
				quality = 0
			} else {
				quality = parsed
			}
		}

		if quality > 0 && quality > preferredQuality {
			preferredPath = path
			preferredQuality = quality
		}
	}

	return preferredPath
}
