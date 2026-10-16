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
	"mailcatcher/internal/config"
	"mailcatcher/internal/events"
	"mailcatcher/internal/ingest"
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
	ui, err := web.New(st, broker, cfg.CookieSecure)
	if err != nil {
		slog.Error("templates", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /internal/ingest", &ingest.Handler{Store: st, Secret: cfg.HMACSecret,
		OnStored: func(m *store.Message) { broker.Publish(events.Event{MessageID: m.ID, InboxID: m.InboxID}) }})
	(&api.API{Store: st, Broker: broker}).Register(mux)
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

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
