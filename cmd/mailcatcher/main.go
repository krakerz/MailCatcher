package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mailcatcher/internal/api"
	"mailcatcher/internal/cloudflare"
	"mailcatcher/internal/config"
	"mailcatcher/internal/destinations"
	"mailcatcher/internal/events"
	"mailcatcher/internal/ingest"
	"mailcatcher/internal/outbound"
	"mailcatcher/internal/store"
	"mailcatcher/internal/version"
	"mailcatcher/internal/web"
)

func main() {
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
	ui.Mailer, ui.Dest = mailer, dest
	if !dest.Enabled() {
		slog.Warn("MC_CF_API_TOKEN / MC_CF_ACCOUNT_ID not set: destination addresses cannot be managed from the app")
	}
	if cfg.WorkerURL == "" {
		slog.Warn("MC_WORKER_URL is not set: reply, compose and forward are disabled")
	}

	mux := http.NewServeMux()
	mux.Handle("POST /internal/ingest", &ingest.Handler{Store: st, Secret: cfg.HMACSecret, Domain: cfg.Domain,
		OnStored: func(m *store.Message) { broker.Publish(events.Event{MessageID: m.ID, InboxID: m.InboxID}) }})
	(&api.API{Store: st, Broker: broker, Mailer: mailer}).Register(mux)
	ui.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok " + version.Version + "\n"))
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.SecurityHeaders(mux),
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

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
