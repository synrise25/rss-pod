package edgetts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func resetClockSkew(t *testing.T) {
	t.Helper()
	clockSkewMu.Lock()
	previous := clockSkew
	clockSkew = 0
	clockSkewMu.Unlock()
	t.Cleanup(func() {
		clockSkewMu.Lock()
		clockSkew = previous
		clockSkewMu.Unlock()
	})
}

func TestConnectV2AndClockCorrection(t *testing.T) {
	resetClockSkew(t)
	serverTime := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	var attempts atomic.Int32
	var firstToken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("Sec-MS-GEC-Version") != "2-143.0.3650.75" {
			t.Error("client did not request the v2 handshake")
		}
		if !regexp.MustCompile(`^[A-F0-9]{16}-[A-F0-9]{64}$`).MatchString(query.Get("Sec-MS-GEC")) {
			t.Error("invalid v2 token format")
		}
		if cookie, err := r.Cookie("muid"); err != nil || len(cookie.Value) != 32 {
			t.Error("missing MUID cookie")
		}
		if query.Get("TrustedClientToken") != trustedClientToken || len(query.Get("ConnectionId")) != 32 {
			t.Error("missing handshake identifiers")
		}
		if attempts.Add(1) == 1 {
			firstToken.Store(query.Get("Sec-MS-GEC"))
			w.Header().Set("Date", serverTime.Format(http.TimeFormat))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if query.Get("Sec-MS-GEC") == firstToken.Load() || query.Get("Sec-MS-GEC") != secMSGECAt(serverTime) {
			t.Error("retry did not regenerate the token using the server clock")
		}
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for _, path := range []string{"speech.config", "ssml"} {
			_, message, err := conn.ReadMessage()
			if err != nil || !strings.Contains(string(message), "Path:"+path) {
				t.Errorf("expected %s frame, got %q: %v", path, message, err)
			}
		}
	}))
	defer server.Close()
	client, err := NewCommunicate("服务检查。", DefaultConfig(), WithConnectTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.connectEndpoint(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := client.sendSpeechConfig(conn); err != nil {
		t.Fatal(err)
	}
	if err := client.sendSSML(conn, "服务检查。"); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestConnectRejectionReportsHTTPStatusAndStops(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			resetClockSkew(t)
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := NewCommunicate("检查。", DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.connectEndpoint(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
			if !errors.Is(err, ErrWebSocket) || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
				t.Fatalf("missing handshake HTTP diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), "Sec-MS-GEC=") {
				t.Fatal("diagnostic leaked the handshake URL")
			}
			want := int32(1)
			if status == http.StatusForbidden {
				want = 2
			}
			if attempts.Load() != want {
				t.Fatalf("attempts = %d, want %d", attempts.Load(), want)
			}
		})
	}
}
