package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/synrise25/rss-pod/internal/config"
	webui "github.com/synrise25/rss-pod/web"
)

const adminCookie = "rss_pod_admin"
const adminSessionTTL = 30 * time.Minute

type adminServer struct {
	player      *playerServer
	pool        *pgxpool.Pool
	secret      []byte
	keyHash     string
	crossOrigin http.CrossOriginProtection
}

func newAdminServer(cfg config.AdminConfig, pool *pgxpool.Pool, player *playerServer) *adminServer {
	if cfg.TOTPSecret == "" || cfg.Validate() != nil {
		return nil
	}
	secret, _ := cfg.SecretBytes()
	return &adminServer{pool: pool, player: player, secret: secret, keyHash: tokenHash(string(secret))}
}

func (s *adminServer) register(mux *http.ServeMux) {
	for _, path := range []string{"/admin", "/admin/en", "/admin/zh-cn"} {
		mux.HandleFunc("GET "+path, s.page)
		mux.HandleFunc("GET "+path+"/{$}", func(w http.ResponseWriter, r *http.Request) {
			target := path
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		})
	}
	mux.HandleFunc("POST /api/v1/admin/login", s.guard(false, s.login))
	mux.HandleFunc("GET /api/v1/admin/session", s.guard(true, s.session))
	mux.HandleFunc("POST /api/v1/admin/logout", s.guard(true, s.logout))
	mux.HandleFunc("PATCH /api/v1/admin/episodes/{episodeID}/visibility", s.guard(true, s.setVisibility))
	mux.HandleFunc("GET /api/v1/admin/episodes", s.guard(true, s.listEpisodes))
}

func (s *adminServer) page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self' https: http:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
	// Serve the same player shell; the admin bootstrap shows login before loading cards.
	data, err := webui.Files.ReadFile("index.html")
	if err != nil {
		http.Error(w, "admin page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(data)
}

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func csrfToken(cookie string) string { return tokenHash("rss-pod csrf\x00" + cookie) }

func (s *adminServer) guard(auth bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Derive the site's origin from the browser request. Reverse proxies must
		// preserve Host; forwarded host/proto headers are never trusted here.
		if r.Method != http.MethodGet && (!validAdminOrigin(r) || s.crossOrigin.Check(r) != nil) {
			writeError(w, http.StatusForbidden, "invalid origin")
			return
		}
		if auth {
			cookie, err := r.Cookie(adminCookie)
			if err != nil || len(cookie.Value) != 64 {
				writeError(w, http.StatusUnauthorized, "login required")
				return
			}
			var valid bool
			err = s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM admin_sessions WHERE token_hash=$1 AND key_hash=$2 AND expires_at > now())`, tokenHash(cookie.Value), s.keyHash).Scan(&valid)
			if err != nil {
				s.unavailable(w, err)
				return
			}
			if !valid {
				writeError(w, http.StatusUnauthorized, "login required")
				return
			}
			if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrfToken(cookie.Value))) != 1 {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		next(w, r)
	}
}

// HTTPS is required except for local development. Comparing the actual Host
// also works behind TLS termination without a configured public URL.
func validAdminOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" || origin.Host != r.Host || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery {
		return false
	}
	if origin.Scheme == "https" {
		return true
	}
	local := origin.Hostname() == "localhost" || origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1"
	return origin.Scheme == "http" && local
}

// RFC 6238, SHA-1, six digits and 30 second steps (authenticator defaults).
func totpCode(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
func matchTOTPStep(secret []byte, code string, now time.Time) int64 {
	if len(code) != 6 {
		return -1
	}
	matched := int64(-1)
	for step := now.Unix()/30 - 1; step <= now.Unix()/30+1; step++ {
		if subtle.ConstantTimeCompare([]byte(code), []byte(totpCode(secret, step))) == 1 {
			matched = step
		}
	}
	return matched
}

func (s *adminServer) login(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "JSON required")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid login request")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.unavailable(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO admin_auth_state (key_hash) VALUES ($1) ON CONFLICT DO NOTHING`, s.keyHash); err != nil {
		s.unavailable(w, err)
		return
	}
	var lastStep int64
	var attempts int
	var window time.Time
	if err = tx.QueryRow(r.Context(), `SELECT last_step, attempts, window_started FROM admin_auth_state WHERE key_hash=$1 FOR UPDATE`, s.keyHash).Scan(&lastStep, &attempts, &window); err != nil {
		s.unavailable(w, err)
		return
	}
	now := time.Now()
	if now.Sub(window) >= time.Minute {
		attempts = 0
		window = now
	}
	if attempts >= 5 {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many attempts; wait one minute")
		return
	}
	step := matchTOTPStep(s.secret, input.Code, now)
	valid := step > lastStep
	if valid {
		lastStep = step
	}
	if _, err = tx.Exec(r.Context(), `UPDATE admin_auth_state SET attempts=$2, window_started=$3, last_step=$4 WHERE key_hash=$1`, s.keyHash, attempts+1, window, lastStep); err != nil {
		s.unavailable(w, err)
		return
	}
	token := ""
	if valid {
		token = randomToken()
		if _, err = tx.Exec(r.Context(), `DELETE FROM admin_sessions WHERE expires_at <= now() OR key_hash <> $1`, s.keyHash); err != nil {
			s.unavailable(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO admin_sessions (token_hash, key_hash, expires_at) VALUES ($1,$2,$3)`, tokenHash(token), s.keyHash, now.Add(adminSessionTTL)); err != nil {
			s.unavailable(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.unavailable(w, err)
		return
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, "invalid or already used code")
		return
	}
	s.setCookie(w, r, token, int(adminSessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrfToken(token)})
}
func randomToken() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
func (s *adminServer) setCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: token, Path: "/api/v1/admin", MaxAge: maxAge, HttpOnly: true, Secure: !strings.HasPrefix(r.Header.Get("Origin"), "http://"), SameSite: http.SameSiteStrictMode})
}
func (s *adminServer) session(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(adminCookie)
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrfToken(cookie.Value)})
}
func (s *adminServer) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(adminCookie)
	if _, err := s.pool.Exec(r.Context(), `DELETE FROM admin_sessions WHERE token_hash=$1`, tokenHash(cookie.Value)); err != nil {
		s.unavailable(w, err)
		return
	}
	s.setCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func (s *adminServer) unavailable(w http.ResponseWriter, err error) {
	slog.Error("admin operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "admin operation unavailable")
}

func (s *adminServer) listEpisodes(w http.ResponseWriter, r *http.Request) {
	s.player.episodes(w, r, true)
}

func (s *adminServer) setVisibility(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("episodeID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid episode ID")
		return
	}
	var input struct {
		Hidden *bool `json:"hidden"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&input); err != nil || input.Hidden == nil {
		writeError(w, http.StatusBadRequest, "hidden must be a boolean")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `UPDATE episodes
        SET hidden_at=CASE WHEN $2 THEN COALESCE(hidden_at,now()) ELSE NULL END
        WHERE id=$1 AND status='published'`, id, *input.Hidden)
	if err != nil {
		s.unavailable(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "published episode not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"hidden": *input.Hidden})
}
