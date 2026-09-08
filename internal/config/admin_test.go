package config

import "testing"

func TestAdminConfig(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		valid        bool
	}{
		{"disabled", "", true},
		{"secret only", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", true},
		{"lowercase", "gezdgnbvgy3tqojqgezdgnbvgy3tqojq", true},
		{"short", "MZXW6", false},
		{"malformed", "not-a-secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (AdminConfig{TOTPSecret: tc.secret}).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, valid=%v", err, tc.valid)
			}
		})
	}
}
