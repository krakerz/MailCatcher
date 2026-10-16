package ingest

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/outbound"
)

// DrainEvery is how often mail stashed in Workers KV (app was unreachable) is collected.
const DrainEvery = time.Minute

// Drainer moves mail the Worker stashed in KV into the store, then deletes it from KV.
type Drainer struct {
	Handler *Handler
	Worker  *outbound.Sender
}

// stashedAt reads the arrival time from a "pending:<unix ms>:<uuid>" key.
func stashedAt(key string) time.Time {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) == 3 {
		if ms, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			return time.UnixMilli(ms)
		}
	}
	return time.Now()
}

// Drain collects everything currently stashed and returns how many mails were taken.
// A mail is deleted from KV only after it is stored (or known to be unparseable).
func (d *Drainer) Drain(ctx context.Context) (int, error) {
	taken := 0
	for {
		list, err := d.Worker.ListPending(ctx)
		if err != nil {
			return taken, err
		}
		if len(list) == 0 {
			return taken, nil
		}
		progress := false
		for _, p := range list {
			raw, err := d.Worker.FetchPending(ctx, p.Key)
			if errors.Is(err, outbound.ErrGone) {
				continue
			}
			if err != nil {
				slog.Warn("drain: fetch", "key", p.Key, "err", err)
				continue
			}
			_, _, err = d.Handler.Accept(ctx, raw, p.From, p.To, p.Forwarded, stashedAt(p.Key))
			if err != nil && !errors.Is(err, ErrUnparseable) {
				return taken, err // storage trouble: keep it in KV, retry next round
			}
			if err := d.Worker.DeletePending(ctx, p.Key); err != nil {
				slog.Warn("drain: delete", "key", p.Key, "err", err)
				continue
			}
			taken++
			progress = true
		}
		// KV lists up to 50 keys and keeps listing deleted ones for a while, so only
		// a full page is worth listing again right away.
		if !progress || len(list) < 50 {
			return taken, nil
		}
	}
}

func (d *Drainer) Run(ctx context.Context) {
	if d.Worker.WorkerURL == "" {
		return
	}
	t := time.NewTicker(DrainEvery)
	defer t.Stop()
	for {
		if n, err := d.Drain(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("drain", "err", err)
		} else if n > 0 {
			slog.Info("drained mail stashed while the app was unreachable", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
