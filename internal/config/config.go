package config

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen     string
	DataDir    string
	Domain     string
	HMACSecret string
	// First admin's password; empty = generate one and log it (first run only).
	AdminPassword string
	// Cloudflare API access for managing destination addresses; both empty disables it.
	CFAPIToken  string
	CFAccountID string
	// Public https URL of this app (the tunnel hostname); pushed to the Worker as its ingest target.
	PublicURL string
	// Worker URL for outbound mail (POST /send); empty disables sending.
	WorkerURL string
	// Messages older than this many days are deleted; 0 keeps everything.
	RetentionDays int
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
		WorkerURL:     os.Getenv("MC_WORKER_URL"),
		PublicURL:     strings.TrimRight(os.Getenv("MC_PUBLIC_URL"), "/"),
		CFAPIToken:    os.Getenv("MC_CF_API_TOKEN"),
		CFAccountID:   os.Getenv("MC_CF_ACCOUNT_ID"),
		CookieSecure:  env("MC_COOKIE_SECURE", "true") != "false",
	}
	days, err := strconv.Atoi(env("MC_RETENTION_DAYS", "30"))
	if err != nil || days < 0 {
		return c, errors.New("MC_RETENTION_DAYS must be a whole number of days (0 = keep forever)")
	}
	c.RetentionDays = days
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return c, errors.New("MC_PUBLIC_URL must look like https://inbox.example.com")
		}
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
