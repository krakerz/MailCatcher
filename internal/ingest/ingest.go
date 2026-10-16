// Package ingest receives raw mail from the Cloudflare Worker (POST /internal/ingest).
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/address"
	"mailcatcher/internal/mailparse"
	"mailcatcher/internal/sig"
	"mailcatcher/internal/store"
)

const (
	HeaderEnvelopeFrom = "X-MC-Envelope-From"
	HeaderEnvelopeTo   = "X-MC-Envelope-To"
	// Comma-separated destinations the Worker auto-forwarded this mail to.
	HeaderAutoForward = "X-MC-Auto-Forward"
	// Cloudflare caps inbound at 25 MiB; leave headroom for its added headers.
	MaxSize = 30 << 20
)

type Handler struct {
	Store  *store.Store
	Secret string
	Domain string // main catch-all domain, for inboxes without their own
	// OnStored is called after a new (non-duplicate) message is saved.
	OnStored func(m *store.Message)
	Now      func() time.Time
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxSize))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if err := sig.Verify(h.Secret, r.Header, raw, now()); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	forwarded := strings.Split(r.Header.Get(HeaderAutoForward), ",")
	m, dup, err := h.Accept(r.Context(), raw, r.Header.Get(HeaderEnvelopeFrom), r.Header.Get(HeaderEnvelopeTo), forwarded, now())
	if errors.Is(err, ErrUnparseable) {
		// 422 tells the Worker not to stash: the bytes will never parse.
		http.Error(w, "unparseable message", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"id":` + strconv.FormatInt(m.ID, 10) + `,"duplicate":` + strconv.FormatBool(dup) + "}\n"))
}

var ErrUnparseable = errors.New("unparseable message")

// Accept routes and stores one inbound mail (from the Worker, live or drained
// from KV) and notifies OnStored for new messages.
func (h *Handler) Accept(ctx context.Context, raw []byte, envFrom, envTo string, forwarded []string, receivedAt time.Time) (*store.Message, bool, error) {
	m, atts, err := Build(raw, envFrom, envTo)
	if err != nil {
		slog.Warn("ingest: unparseable message", "err", err, "size", len(raw))
		return nil, false, fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	inboxes, err := h.Store.ListInboxes(ctx)
	if err != nil {
		slog.Error("ingest: inboxes", "err", err)
		return nil, false, err
	}
	m.InboxID, m.DecodedTo = address.Route(inboxes, h.Domain, m.EnvelopeTo)
	m.ReceivedAt = receivedAt
	for _, d := range forwarded {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			m.AutoForwarded = append(m.AutoForwarded, d)
		}
	}
	dup, err := h.Store.InsertMessage(ctx, m, raw, atts)
	if err != nil {
		slog.Error("ingest: store", "err", err)
		return nil, false, err
	}
	slog.Info("ingest", "id", m.ID, "dup", dup, "inbox", m.InboxID, "from", m.EnvelopeFrom, "to", m.EnvelopeTo, "decoded", m.DecodedTo, "size", m.Size)
	if !dup && h.OnStored != nil {
		h.OnStored(m)
	}
	return m, dup, nil
}

// Build parses raw MIME into an inbound Message. Shared with the KV drain job.
func Build(raw []byte, envFrom, envTo string) (*store.Message, []mailparse.Attachment, error) {
	p, err := mailparse.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	m := store.FromParsed(p)
	m.Direction = "in"
	m.EnvelopeFrom = strings.ToLower(strings.TrimSpace(envFrom))
	m.EnvelopeTo = strings.ToLower(strings.TrimSpace(envTo))
	return m, p.Attachments, nil
}
