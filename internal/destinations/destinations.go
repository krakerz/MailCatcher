// Package destinations mirrors Cloudflare's verified destination addresses
// (the only recipients the free plan delivers to) and manages them.
package destinations

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"time"

	"mailcatcher/internal/cloudflare"
	"mailcatcher/internal/store"
)

var (
	ErrDisabled = errors.New("destination management is not configured (set MC_CF_API_TOKEN and MC_CF_ACCOUNT_ID)")
	// ErrTooSoon: Cloudflare rate-limits re-sending verification and deleting new addresses.
	ErrTooSoon = errors.New("Cloudflare asks to wait a bit before doing this again for this address")
)

const (
	SyncEvery = 5 * time.Minute
	// A send to an unknown/pending address triggers a sync at most this often;
	// short, because the owner may have clicked the link seconds ago.
	minResync = 5 * time.Second
)

type Service struct {
	Store *store.Store
	CF    *cloudflare.Client

	mu       sync.Mutex
	lastSync time.Time
}

func (s *Service) Enabled() bool { return s != nil && s.CF.Enabled() }

func (s *Service) Sync(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	list, err := s.CF.List(ctx)
	if err != nil {
		return err
	}
	out := make([]store.Destination, len(list))
	for i, a := range list {
		out[i] = toStore(a)
	}
	if err := s.Store.ReplaceDestinations(ctx, out); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastSync = time.Now()
	s.mu.Unlock()
	return nil
}

func toStore(a cloudflare.Address) store.Destination {
	d := store.Destination{Email: strings.ToLower(a.Email), CFID: a.ID, CreatedAt: a.Created}
	if a.Verified != nil {
		d.VerifiedAt = *a.Verified
	}
	return d
}

// Run syncs now and every SyncEvery until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(SyncEvery)
	defer t.Stop()
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("destinations sync", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func normalize(email string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil {
		return "", errors.New("not a valid email address")
	}
	return strings.ToLower(a.Address), nil
}

// Add creates the address in Cloudflare, which emails its owner a verification
// link. For an address that is still pending this re-sends the email.
func (s *Service) Add(ctx context.Context, email string) (*store.Destination, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	email, err := normalize(email)
	if err != nil {
		return nil, err
	}
	a, err := s.CF.Create(ctx, email)
	if cloudflare.IsCode(err, cloudflare.CodeSentTooRecently) {
		return nil, ErrTooSoon
	}
	if err != nil {
		return nil, err
	}
	d := toStore(*a)
	if err := s.Store.PutDestination(ctx, d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Service) Remove(ctx context.Context, email string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	d, err := s.Store.GetDestination(ctx, email)
	if err != nil {
		return err
	}
	err = s.CF.Delete(ctx, d.CFID)
	if cloudflare.IsCode(err, cloudflare.CodeCreatedTooRecently) {
		return ErrTooSoon
	}
	if err != nil {
		return err
	}
	return s.Store.DeleteDestination(ctx, d.Email)
}

// Verified reports whether email may receive mail. When management is not
// configured it returns true and leaves the decision to Cloudflare at send time.
// An unknown or pending address triggers a (rate-limited) sync first, so a
// link clicked a minute ago counts.
func (s *Service) Verified(ctx context.Context, email string) (bool, error) {
	if !s.Enabled() {
		return true, nil
	}
	email, err := normalize(email)
	if err != nil {
		return false, err
	}
	d, err := s.Store.GetDestination(ctx, email)
	if err == nil && d.Verified() {
		return true, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	s.mu.Lock()
	stale := time.Since(s.lastSync) > minResync
	s.mu.Unlock()
	if !stale {
		return false, nil
	}
	if err := s.Sync(ctx); err != nil {
		slog.Warn("destinations sync before send", "err", err)
		return true, nil // let Cloudflare decide rather than block on our sync
	}
	d, err = s.Store.GetDestination(ctx, email)
	return err == nil && d.Verified(), nil
}
