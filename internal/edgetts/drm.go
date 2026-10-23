// SPDX-License-Identifier: LGPL-3.0-only
// Adapted from github.com/foresturquhart/edge-tts v1.0.1. See NOTICE.

package edgetts

import (
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// clockSkew tracks the difference between local and server time.
var (
	clockSkew   time.Duration
	clockSkewMu sync.Mutex
)

// generateSecMSGEC generates the Sec-MS-GEC security token based on the current time.
func generateSecMSGEC() string {
	clockSkewMu.Lock()
	skew := clockSkew
	clockSkewMu.Unlock()

	return secMSGECAt(time.Now().Add(skew))
}

func secMSGECAt(now time.Time) string {
	// Match Edge's floating-point Windows ticks, including rounding at this
	// magnitude. Exact integer ticks produce different tokens in some windows.
	ticks := float64(now.Unix()) + float64(now.Nanosecond())/1e9 + 11644473600
	ticks -= math.Mod(ticks, 300)
	data := []byte(fmt.Sprintf("%.0f%s", ticks*1e7, trustedClientToken))
	digest := tokenSHA256(data)
	checksum := tokenCS64(data, digest)
	return fmt.Sprintf("%X-%X", checksum, digest)
}

// generateMUID generates a random Machine User ID (32-character hex string).
func generateMUID() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return fmt.Sprintf("%X", b)
}

// adjustClockSkew adjusts the local clock skew based on a server response.
// It returns an error if the server date cannot be parsed.
func adjustClockSkew(resp *http.Response) error {
	dateStr := resp.Header.Get("Date")
	if dateStr == "" {
		return fmt.Errorf("%w: no Date header in response", ErrSkewAdjustment)
	}

	serverTime, err := http.ParseTime(dateStr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSkewAdjustment, err)
	}

	clockSkewMu.Lock()
	clockSkew = time.Until(serverTime)
	clockSkewMu.Unlock()

	return nil
}
