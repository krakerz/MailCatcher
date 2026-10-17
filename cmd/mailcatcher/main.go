package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mailcatcher/internal/api"
	"mailcatcher/internal/cloudflare"
	"mailcatcher/internal/config"
	"mailcatcher/internal/destinations"
	"mailcatcher/internal/events"
	"mailcatcher/internal/ingest"
	"mailcatcher/internal/outbound"
	"mailcatcher/internal/rules"
	"mailcatcher/internal/store"
	"mailcatcher/internal/version"
	"mailcatcher/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		slog.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := web.Bootstrap(context.Background(), st, cfg.AdminPassword); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	broker := events.NewBroker()
	ui, err := web.New(st, broker, cfg.Domain, cfg.CookieSecure)
	if err != nil {
		slog.Error("templates", "err", err)
		os.Exit(1)
	}
	mailer := &outbound.Mailer{Store: st, Domain: cfg.Domain, Sender: &outbound.Sender{WorkerURL: cfg.WorkerURL, Secret: cfg.HMACSecret}}
	dest := &destinations.Service{Store: st, CF: &cloudflare.Client{Token: cfg.CFAPIToken, AccountID: cfg.CFAccountID}}
	mailer.Verified = dest.Verified
	pusher := &rules.Pusher{Store: st, Sender: mailer.Sender, Domain: cfg.Domain, PublicURL: cfg.PublicURL}
	ui.Mailer, ui.Dest, ui.Rules = mailer, dest, pusher
	if !dest.Enabled() {
		slog.Warn("MC_CF_API_TOKEN / MC_CF_ACCOUNT_ID not set: destination addresses cannot be managed from the app")
	}
	if cfg.WorkerURL == "" {
		slog.Warn("MC_WORKER_URL is not set: reply, compose and forward are disabled")
	}

	mux := http.NewServeMux()
	ingestHandler := &ingest.Handler{Store: st, Secret: cfg.HMACSecret, Domain: cfg.Domain,
		OnStored: func(m *store.Message) { broker.Publish(events.Event{MessageID: m.ID, InboxID: m.InboxID}) }}
	mux.Handle("POST /internal/ingest", ingestHandler)
	(&api.API{Store: st, Broker: broker, Mailer: mailer}).Register(mux)
	ui.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok " + version.Version + "\n"))
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.SecurityHeaders(web.Compress(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", cfg.Listen, "version", version.Version, "domain", cfg.Domain)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	}()

	go dest.Run(ctx)
	drainer := &ingest.Drainer{Handler: ingestHandler, Worker: mailer.Sender}
	if cfg.WorkerURL != "" {
		ui.Drain = drainer.Drain
	}
	go drainer.Run(ctx)
	go retention(ctx, st, cfg.RetentionDays)
	go pusher.Run(ctx)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

// retention deletes old mail hourly; days == 0 keeps everything.
func retention(ctx context.Context, st *store.Store, days int) {
	if days == 0 {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := st.Purge(ctx, time.Now().AddDate(0, 0, -days)); err != nil && ctx.Err() == nil {
			slog.Warn("retention", "err", err)
		} else if n > 0 {
			slog.Info("retention: deleted old messages", "count", n, "older_than_days", days)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// healthcheck is the Docker HEALTHCHECK (the image has no shell or curl).
func healthcheck() int {
	addr := os.Getenv("MC_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
