// Package outbound builds outgoing MIME and talks to the Worker (/send, /rules, /pending).
package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mailcatcher/internal/sig"
)

var (
	ErrDisabled = errors.New("sending is not configured (set MC_WORKER_URL)")
	// ErrGone: a stashed key that no longer exists (KV listings lag behind deletes).
	ErrGone = errors.New("stashed mail is already gone")
	// ErrNotActive: our list says verified but Cloudflare still refuses (activation lag).
	ErrNotActive = errors.New("Cloudflare has not activated this destination address yet")
	// ErrNotVerified: Cloudflare only delivers to verified destination addresses on the free plan.
	ErrNotVerified = errors.New("the recipient is not a verified destination address in Cloudflare Email Routing")
)

// Sender makes signed requests to the Worker.
type Sender struct {
	WorkerURL string // e.g. https://mailcatcher.<subdomain>.workers.dev
	Secret    string
	Client    *http.Client
}

// call sends a signed request and returns status and (capped) body.
func (s *Sender) call(ctx context.Context, method, path string, body []byte, hdr map[string]string, limit int64) (int, []byte, error) {
	if s.WorkerURL == "" {
		return 0, nil, ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.WorkerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	sig.SetHeaders(req.Header, s.Secret, body, time.Now())
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("worker: %w", err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, limit))
	return res.StatusCode, out, err
}

func workerErr(status int, body []byte) error {
	return fmt.Errorf("worker: HTTP %d: %s", status, strings.TrimSpace(string(body)))
}

// Send delivers raw to one recipient and returns the Message-ID Cloudflare used
// (it replaces the one in raw).
func (s *Sender) Send(ctx context.Context, envFrom, envTo string, raw []byte) (string, error) {
	status, body, err := s.call(ctx, http.MethodPost, "/send", raw, map[string]string{
		"Content-Type":       "message/rfc822",
		"X-MC-Envelope-From": envFrom,
		"X-MC-Envelope-To":   envTo,
	}, 64<<10)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		if strings.Contains(string(body), "not a verified address") {
			return "", ErrNotVerified
		}
		return "", workerErr(status, body)
	}
	var out struct {
		MessageID string `json:"messageId"`
	}
	json.Unmarshal(body, &out)
	return out.MessageID, nil
}

// PutRules replaces the Worker's auto-forward rules (any JSON-marshalable list).
func (s *Sender) PutRules(ctx context.Context, rules any) error {
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	status, body, err := s.call(ctx, http.MethodPut, "/rules", b, map[string]string{"Content-Type": "application/json"}, 4<<10)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return workerErr(status, body)
	}
	return nil
}

// PutIngestURL tells the Worker where to deliver inbound mail.
func (s *Sender) PutIngestURL(ctx context.Context, ingestURL string) error {
	b, _ := json.Marshal(map[string]string{"ingest_url": ingestURL})
	status, body, err := s.call(ctx, http.MethodPut, "/config", b, map[string]string{"Content-Type": "application/json"}, 4<<10)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return workerErr(status, body)
	}
	return nil
}

// Pending is mail the Worker stashed in KV while the app was unreachable.
type Pending struct {
	Key       string   `json:"key"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Forwarded []string `json:"forwarded"`
}

// ListPending returns up to 50 stashed mails, oldest first (KV lists keys in order).
// Without all, the Worker answers from its "mail waiting" flag and lists KV only when set.
func (s *Sender) ListPending(ctx context.Context, all bool) ([]Pending, error) {
	path := "/pending"
	if all {
		path += "?all=1"
	}
	status, body, err := s.call(ctx, http.MethodGet, path, nil, nil, 1<<20)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, workerErr(status, body)
	}
	var out []Pending
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("worker: pending list: %w", err)
	}
	return out, nil
}

// FetchPending returns the raw mail of a stashed key.
func (s *Sender) FetchPending(ctx context.Context, key string) ([]byte, error) {
	status, body, err := s.call(ctx, http.MethodGet, "/pending/"+url.PathEscape(key), nil, nil, 32<<20)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, ErrGone
	}
	if status != http.StatusOK {
		return nil, workerErr(status, body[:min(len(body), 200)])
	}
	return body, nil
}

func (s *Sender) DeletePending(ctx context.Context, key string) error {
	status, body, err := s.call(ctx, http.MethodDelete, "/pending/"+url.PathEscape(key), nil, nil, 4<<10)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return workerErr(status, body)
	}
	return nil
}
