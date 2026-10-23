package edgetts

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestSecMSGECMatchesUpstreamV2(t *testing.T) {
	// Fixtures are generated with rany2/edge-tts 7.3.0, not this Go port.
	data, err := os.ReadFile("testdata/gec_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Unix  int64  `json:"unix"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		now := time.Unix(vector.Unix, 0)
		t.Run(now.UTC().Format(time.RFC3339), func(t *testing.T) {
			if got := secMSGECAt(now); got != vector.Token {
				t.Fatalf("token = %s, want %s", got, vector.Token)
			}
		})
	}
}
