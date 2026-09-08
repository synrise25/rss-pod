package config

import (
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
)

// AdminConfig is environment-only; an empty secret disables all admin routes.
type AdminConfig struct {
	TOTPSecret string
	Origin     string
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
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return errors.New("RSS_POD_ADMIN_ORIGIN must be an exact origin without a path, query or credentials")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("RSS_POD_ADMIN_ORIGIN requires HTTPS (HTTP is allowed only on loopback for development)")
	}
	return nil
}
