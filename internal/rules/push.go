package rules

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"mailcatcher/internal/outbound"
	"mailcatcher/internal/store"
)

// Re-push periodically so a failed push or a lost KV value heals by itself.
const PushEvery = 10 * time.Minute

// Pusher keeps the Worker's KV copy of the rules (and the app's ingest URL) in sync.
type Pusher struct {
	Store  *store.Store
	Sender *outbound.Sender
	Domain string
	// PublicURL (MC_PUBLIC_URL) is pushed as the Worker's ingest target when set.
	PublicURL string

	mu      sync.Mutex
	last    time.Time
	lastErr error
	count   int
}

// Compiled returns the Worker rules for the current database state.
func (p *Pusher) Compiled(ctx context.Context) ([]WorkerRule, error) {
	list, err := p.Store.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	inboxes, err := p.Store.ListInboxes(ctx)
	if err != nil {
		return nil, err
	}
	return Compile(list, inboxes, p.Domain), nil
}

func (p *Pusher) Push(ctx context.Context) error {
	var err error
	if p.PublicURL != "" {
		err = p.Sender.PutIngestURL(ctx, strings.TrimRight(p.PublicURL, "/")+"/internal/ingest")
	}
	compiled, cerr := p.Compiled(ctx)
	if err == nil {
		err = cerr
	}
	if cerr == nil {
		if perr := p.Sender.PutRules(ctx, compiled); err == nil {
			err = perr
		}
	}
	p.mu.Lock()
	p.last, p.lastErr, p.count = time.Now(), err, len(compiled)
	p.mu.Unlock()
	if err != nil {
		slog.Warn("push forward rules", "err", err)
	}
	return err
}

// PushSoon pushes in the background (after an inbox or rule change).
func (p *Pusher) PushSoon() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p.Push(ctx)
	}()
}

// Status reports the last push for the UI.
func (p *Pusher) Status() (at time.Time, rules int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last, p.count, p.lastErr
}

func (p *Pusher) Run(ctx context.Context) {
	t := time.NewTicker(PushEvery)
	defer t.Stop()
	for {
		if p.Sender.WorkerURL != "" {
			p.Push(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
