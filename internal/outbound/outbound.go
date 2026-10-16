// Package outbound builds outgoing MIME and sends it through the Worker's /send.
package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mailcatcher/internal/sig"
)

var (
	ErrDisabled = errors.New("sending is not configured (set MC_WORKER_URL)")
	// ErrNotVerified: Cloudflare only delivers to verified destination addresses on the free plan.
	ErrNotVerified = errors.New("the recipient is not a verified destination address in Cloudflare Email Routing")
)

// Sender posts signed raw mail to the Worker.
type Sender struct {
	WorkerURL string // e.g. https://mailcatcher.<subdomain>.workers.dev
	Secret    string
	Client    *http.Client
}

// Send delivers raw to one recipient and returns the Message-ID Cloudflare used
// (it replaces the one in raw).
func (s *Sender) Send(ctx context.Context, envFrom, envTo string, raw []byte) (string, error) {
	if s.WorkerURL == "" {
		return "", ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.WorkerURL, "/")+"/send", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("X-MC-Envelope-From", envFrom)
	req.Header.Set("X-MC-Envelope-To", envTo)
	sig.SetHeaders(req.Header, s.Secret, raw, time.Now())

	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("worker: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if strings.Contains(msg, "not a verified address") {
			return "", ErrNotVerified
		}
		return "", fmt.Errorf("worker: %s: %s", res.Status, msg)
	}
	var out struct {
		MessageID string `json:"messageId"`
	}
	json.Unmarshal(body, &out)
	return out.MessageID, nil
}
