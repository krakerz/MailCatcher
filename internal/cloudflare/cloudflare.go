// Package cloudflare is a minimal client for Email Routing destination addresses.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Error codes seen in the Phase 0 spike (see NOTES.md).
const (
	CodeSentTooRecently    = 2025 // re-adding an unverified address within the cooldown
	CodeCreatedTooRecently = 2032 // deleting right after creating
)

type Client struct {
	Token     string
	AccountID string
	BaseURL   string // DefaultBaseURL when empty
	HTTP      *http.Client
}

func (c *Client) Enabled() bool { return c != nil && c.Token != "" && c.AccountID != "" }

type Address struct {
	ID       string     `json:"id"`
	Email    string     `json:"email"`
	Verified *time.Time `json:"verified"` // nil until the owner clicks the link
	Created  time.Time  `json:"created"`
}

// APIError carries Cloudflare's first error code and message.
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cloudflare: %s (code %d, HTTP %d)", e.Message, e.Code, e.Status)
}

func IsCode(err error, code int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == code
}

type envelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*envelope, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/accounts/"+url.PathEscape(c.AccountID)+"/email/routing"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err)
	}
	defer res.Body.Close()
	var env envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("cloudflare: HTTP %d, unreadable response", res.StatusCode)
	}
	if !env.Success {
		e := &APIError{Status: res.StatusCode, Message: "request failed"}
		if len(env.Errors) > 0 {
			e.Code, e.Message = env.Errors[0].Code, env.Errors[0].Message
		}
		return nil, e
	}
	return &env, nil
}

// List returns every destination address of the account.
func (c *Client) List(ctx context.Context) ([]Address, error) {
	var all []Address
	for page := 1; ; page++ {
		env, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/addresses?per_page=50&page=%d", page), nil)
		if err != nil {
			return nil, err
		}
		var batch []Address
		if err := json.Unmarshal(env.Result, &batch); err != nil {
			return nil, fmt.Errorf("cloudflare: %w", err)
		}
		all = append(all, batch...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(batch) == 0 {
			return all, nil
		}
	}
}

// Create adds an address; Cloudflare emails its owner a verification link.
// Calling it again for a pending address re-sends that email (rate-limited).
func (c *Client) Create(ctx context.Context, email string) (*Address, error) {
	env, err := c.do(ctx, http.MethodPost, "/addresses", map[string]string{"email": email})
	if err != nil {
		return nil, err
	}
	var a Address
	if err := json.Unmarshal(env.Result, &a); err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err)
	}
	return &a, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/addresses/"+url.PathEscape(id), nil)
	return err
}
