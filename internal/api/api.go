// Package api is the test-automation API, authenticated per inbox with a Bearer key.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/events"
	"mailcatcher/internal/mailparse"
	"mailcatcher/internal/outbound"
	"mailcatcher/internal/store"
	"mailcatcher/internal/web"
)

// Cloudflare closes a proxied request after 100 s without a response.
const MaxWait = 90 * time.Second

type API struct {
	Store  *store.Store
	Broker *events.Broker
	Mailer *outbound.Mailer
}

func (a *API) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/inboxes/{id}/messages", a.key(a.list))
	mux.Handle("DELETE /api/inboxes/{id}/messages", a.key(a.deleteMessages))
	mux.Handle("GET /api/messages/{id}", a.key(a.get))
	mux.Handle("GET /api/messages/{id}/raw", a.key(a.raw))
	mux.Handle("GET /api/messages/{id}/attachments/{aid}", a.key(a.attachment))
	mux.Handle("POST /api/messages/{id}/reply", a.key(a.reply))
	mux.Handle("POST /api/messages/{id}/forward", a.key(a.forward))
}

type ctxKey struct{}

func inbox(r *http.Request) *store.Inbox { return r.Context().Value(ctxKey{}).(*store.Inbox) }

func (a *API) key(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			jsonErr(w, http.StatusUnauthorized, "missing Authorization: Bearer <inbox api key>")
			return
		}
		in, err := a.Store.InboxByAPIKeyHash(r.Context(), auth.HashToken(strings.TrimSpace(tok)))
		if err != nil {
			jsonErr(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, in)))
	})
}

// sameInbox rejects inbox ids other than the key's own.
func sameInbox(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("id") != strconv.FormatInt(inbox(r).ID, 10) {
		jsonErr(w, http.StatusForbidden, "api key belongs to another inbox")
		return false
	}
	return true
}

func filterFrom(r *http.Request, inboxID int64) (store.Filter, error) {
	q := r.URL.Query()
	f := store.Filter{InboxID: inboxID, To: q.Get("to"), From: q.Get("from"), Subject: q.Get("subject"), Direction: "in"}
	switch d := q.Get("direction"); d {
	case "", "in":
	case "out":
		f.Direction = d
	case "all":
		f.Direction = ""
	default:
		return f, errors.New("direction: in, out or all")
	}
	if s := q.Get("since"); s != "" {
		t, err := parseTime(s)
		if err != nil {
			return f, fmt.Errorf("since: %w", err)
		}
		f.Since = t
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return f, errors.New("limit: positive integer")
		}
		f.Limit = n
	}
	return f, nil
}

// parseTime accepts RFC 3339 or unix seconds/milliseconds.
func parseTime(s string) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}

// list returns matching messages, newest first. With wait=<duration> it blocks
// until at least one message matches or the wait expires (then returns []).
func (a *API) list(w http.ResponseWriter, r *http.Request) {
	if !sameInbox(w, r) {
		return
	}
	f, err := filterFrom(r, inbox(r).ID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var wait time.Duration
	if s := r.URL.Query().Get("wait"); s != "" {
		if wait, err = time.ParseDuration(s); err != nil || wait < 0 {
			jsonErr(w, http.StatusBadRequest, "wait: duration like 30s")
			return
		}
		wait = min(wait, MaxWait)
	}

	// Subscribe before the first query so a message arriving in between is not missed.
	ch, cancel := a.Broker.Subscribe()
	defer cancel()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		msgs, err := a.Store.ListMessages(r.Context(), f)
		if err != nil {
			slog.Error("api list", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if len(msgs) > 0 || wait == 0 {
			writeJSON(w, map[string]any{"messages": summaries(msgs)})
			return
		}
	waiting:
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline.C:
				// One last look: a message may have landed just before the deadline.
				msgs, err := a.Store.ListMessages(r.Context(), f)
				if err != nil {
					jsonErr(w, http.StatusInternalServerError, "internal error")
					return
				}
				writeJSON(w, map[string]any{"messages": summaries(msgs)})
				return
			case e := <-ch:
				if e.InboxID == f.InboxID {
					break waiting
				}
			}
		}
	}
}

func (a *API) deleteMessages(w http.ResponseWriter, r *http.Request) {
	if !sameInbox(w, r) {
		return
	}
	f, err := filterFrom(r, inbox(r).ID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Cleanup covers sent copies too unless a direction is asked for explicitly.
	if r.URL.Query().Get("direction") == "" {
		f.Direction = ""
	}
	n, err := a.Store.DeleteMessages(r.Context(), f)
	if err != nil {
		slog.Error("api delete", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, map[string]any{"deleted": n})
}

func (a *API) message(w http.ResponseWriter, r *http.Request) (*store.Message, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not found")
		return nil, false
	}
	m, err := a.Store.GetMessage(r.Context(), id)
	// Other inboxes' messages look nonexistent to this key.
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.InboxID != inbox(r).ID) {
		jsonErr(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if err != nil {
		slog.Error("api get", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	return m, true
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	m, ok := a.message(w, r)
	if !ok {
		return
	}
	atts, err := a.Store.Attachments(r.Context(), m.ID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := full(m)
	for _, at := range atts {
		out.Attachments = append(out.Attachments, attachmentJSON{
			ID: at.ID, Filename: at.Filename, ContentType: at.ContentType, Size: at.Size,
			ContentID: at.ContentID, Inline: at.Inline,
			URL: fmt.Sprintf("/api/messages/%d/attachments/%d", m.ID, at.ID),
		})
	}
	writeJSON(w, out)
}

func (a *API) raw(w http.ResponseWriter, r *http.Request) {
	m, ok := a.message(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	http.ServeFile(w, r, a.Store.Path(m.RawPath))
}

func (a *API) attachment(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.message(w, r); !ok {
		return
	}
	web.ServeAttachment(w, r, a.Store)
}

type summaryJSON struct {
	ID           int64               `json:"id"`
	InboxID      int64               `json:"inbox_id"`
	Direction    string              `json:"direction"`
	MessageID    string              `json:"message_id"`
	EnvelopeFrom string              `json:"envelope_from"`
	EnvelopeTo   string              `json:"envelope_to"`
	DecodedTo    string              `json:"decoded_to,omitempty"`
	From         mailparse.Address   `json:"from"`
	To           []mailparse.Address `json:"to"`
	Cc           []mailparse.Address `json:"cc"`
	Subject      string              `json:"subject"`
	Date         *time.Time          `json:"date"`
	ReceivedAt   time.Time           `json:"received_at"`
	Size         int64               `json:"size"`
	Links        []string            `json:"links"`
	Codes        []string            `json:"codes"`
	Attachments  int                 `json:"attachment_count"`
}

type attachmentJSON struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ContentID   string `json:"content_id,omitempty"`
	Inline      bool   `json:"inline"`
	URL         string `json:"url"`
}

type fullJSON struct {
	summaryJSON
	InReplyTo   string           `json:"in_reply_to"`
	References  []string         `json:"references"`
	ParentID    *int64           `json:"parent_id"`
	Text        string           `json:"text"`
	HTML        string           `json:"html"`
	Attachments []attachmentJSON `json:"attachments"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func summary(m *store.Message) summaryJSON {
	s := summaryJSON{
		ID: m.ID, InboxID: m.InboxID, Direction: m.Direction, MessageID: m.MessageID,
		EnvelopeFrom: m.EnvelopeFrom, EnvelopeTo: m.EnvelopeTo, DecodedTo: m.DecodedTo,
		From: m.From, To: nonNil(m.To), Cc: nonNil(m.Cc), Subject: m.Subject,
		ReceivedAt: m.ReceivedAt, Size: m.Size, Links: nonNil(m.Links), Codes: nonNil(m.Codes),
		Attachments: m.AttachmentCount,
	}
	if !m.Date.IsZero() {
		d := m.Date
		s.Date = &d
	}
	return s
}

func summaries(ms []*store.Message) []summaryJSON {
	out := make([]summaryJSON, len(ms))
	for i, m := range ms {
		out[i] = summary(m)
	}
	return out
}

func full(m *store.Message) fullJSON {
	return fullJSON{
		summaryJSON: summary(m), InReplyTo: m.InReplyTo, References: nonNil(m.References),
		ParentID: m.ParentID, Text: m.Text, HTML: m.HTML, Attachments: []attachmentJSON{},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (a *API) sendFailed(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, outbound.ErrNotVerified):
		jsonErr(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, outbound.ErrDisabled):
		jsonErr(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, outbound.ErrUnsupportedAddress):
		jsonErr(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("api send", "err", err)
		jsonErr(w, http.StatusBadGateway, err.Error())
	}
}

// reply: {"text": "...", "quote": true}
func (a *API) reply(w http.ResponseWriter, r *http.Request) {
	m, ok := a.message(w, r)
	if !ok {
		return
	}
	var body struct {
		Text  string `json:"text"`
		Quote *bool  `json:"quote"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		jsonErr(w, http.StatusBadRequest, "body: JSON {\"text\": ..., \"quote\": true}")
		return
	}
	out, err := a.Mailer.Reply(r.Context(), m, outbound.ReplyOptions{Text: body.Text, Quote: body.Quote == nil || *body.Quote})
	if err != nil {
		a.sendFailed(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, summary(out))
}

// forward: {"to": "verified@x", "text": "note", "reply_to_mailcatcher": false}
func (a *API) forward(w http.ResponseWriter, r *http.Request) {
	m, ok := a.message(w, r)
	if !ok {
		return
	}
	var body struct {
		To                 string `json:"to"`
		Text               string `json:"text"`
		ReplyToMailcatcher bool   `json:"reply_to_mailcatcher"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.To == "" {
		jsonErr(w, http.StatusBadRequest, "body: JSON {\"to\": \"verified@address\", \"text\": ...}")
		return
	}
	out, err := a.Mailer.Forward(r.Context(), m, outbound.ForwardOptions{To: body.To, Note: body.Text, ReplyToMailcatcher: body.ReplyToMailcatcher})
	if err != nil {
		a.sendFailed(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, summary(out))
}
