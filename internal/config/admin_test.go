package config

import "testing"

func TestAdminConfig(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		name, secret, origin string
		valid                bool
	}{
		{"disabled", "", "", true},
		{"https", secret, "https://podcasts.example.com", true},
		{"loopback", secret, "http://127.0.0.1:8080", true},
		{"ipv6", secret, "http://[::1]:8080", true},
		{"short", "MZXW6", "https://example.com", false},
		{"malformed", "not-a-secret", "https://example.com", false},
		{"missing origin", secret, "", false},
		{"insecure", secret, "http://example.com", false},
		{"path", secret, "https://example.com/admin", false},
		{"userinfo", secret, "https://admin@example.com", false},
		{"query", secret, "https://example.com?x=y", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (AdminConfig{TOTPSecret: tc.secret, Origin: tc.origin}).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, valid=%v", err, tc.valid)
			}
		})
	}
}
