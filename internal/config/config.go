package config

import (
	"errors"
	"os"
)

type Config struct {
	Listen     string
	DataDir    string
	Domain     string
	HMACSecret string
	// First admin's password; empty = generate one and log it (first run only).
	AdminPassword string
	// Off only for plain-http local testing; the tunnel serves https.
	CookieSecure bool
}

func Load() (Config, error) {
	c := Config{
		Listen:        env("MC_LISTEN", ":8080"),
		DataDir:       env("MC_DATA_DIR", "/data"),
		Domain:        os.Getenv("MC_DOMAIN"),
		HMACSecret:    os.Getenv("MC_HMAC_SECRET"),
		AdminPassword: os.Getenv("MC_ADMIN_PASSWORD"),
		CookieSecure:  env("MC_COOKIE_SECURE", "true") != "false",
	}
	if c.Domain == "" {
		return c, errors.New("MC_DOMAIN is required")
	}
	if len(c.HMACSecret) < 32 {
		return c, errors.New("MC_HMAC_SECRET must be at least 32 characters")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
