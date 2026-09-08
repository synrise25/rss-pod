package config

import (
	"encoding/base32"
	"errors"
	"strings"
)

// AdminConfig is environment-only; an empty secret disables all admin routes.
type AdminConfig struct {
	TOTPSecret string
}

func (c AdminConfig) SecretBytes() ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(strings.ToUpper(strings.TrimSpace(c.TOTPSecret)), "="))
}

func (c AdminConfig) Validate() error {
	if c.TOTPSecret == "" {
		return nil
	}
	secret, err := c.SecretBytes()
	if err != nil || len(secret) < 20 {
		return errors.New("RSS_POD_ADMIN_TOTP_SECRET must be a Base32 secret of at least 20 bytes")
	}
	return nil
}
