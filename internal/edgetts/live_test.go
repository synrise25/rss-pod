package edgetts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synrise25/rss-pod/internal/audioinfo"
)

// Live synthesis is opt-in so normal tests never contact external services.
func TestLiveSynthesis(t *testing.T) {
	if os.Getenv("RSS_POD_EDGE_TTS_LIVE_TEST") != "1" {
		t.Skip("set RSS_POD_EDGE_TTS_LIVE_TEST=1 to test the public Edge service")
	}
	for _, voice := range []string{"zh-CN-YunxiaNeural", "zh-CN-YunjianNeural"} {
		t.Run(voice, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Voice = voice
			// Yunxia also exercises UTF-8 and XML entity splitting across requests.
			text := "播客语音服务检查，新的连接握手已经恢复。"
			if voice == "zh-CN-YunxiaNeural" {
				text = strings.Repeat("这是一段中文与 A&B 的合成验证。", 100)
			}
			client, err := NewCommunicate(text, cfg,
				WithProxy(os.Getenv("EDGE_TTS_PROXY")),
				WithConnectTimeout(20*time.Second), WithReceiveTimeout(120*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			var audio bytes.Buffer
			if err := client.Stream(ctx, func(chunk TTSChunk) error {
				if chunk.Type == ChunkTypeAudio {
					audio.Write(chunk.Data)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			duration, err := audioinfo.MP3Duration(audio.Bytes())
			if err != nil || duration <= 0 {
				t.Fatalf("invalid MP3 output (%d bytes): duration=%s err=%v", audio.Len(), duration, err)
			}
			if dir := os.Getenv("RSS_POD_EDGE_TTS_AUDIO_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, voice+".mp3"), audio.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("valid MP3: %d bytes, duration=%s, text=%d bytes", audio.Len(), duration, len(text))
		})
	}
}
