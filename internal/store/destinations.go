package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Destination struct {
	Email      string
	CFID       string
	VerifiedAt time.Time // zero while pending
	CreatedAt  time.Time
	SyncedAt   time.Time
}

func (d *Destination) Verified() bool { return !d.VerifiedAt.IsZero() }

// ReplaceDestinations makes the table equal to list (a full Cloudflare sync).
func (s *Store) ReplaceDestinations(ctx context.Context, list []Destination) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM destinations"); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, d := range list {
		if err := upsertDestination(ctx, tx, d, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PutDestination inserts or updates one address (after creating it in Cloudflare).
func (s *Store) PutDestination(ctx context.Context, d Destination) error {
	return upsertDestination(ctx, s.db, d, time.Now().UnixMilli())
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func upsertDestination(ctx context.Context, db execer, d Destination, now int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO destinations (email, cf_id, verified_at, created_at, synced_at) VALUES (?,?,?,?,?)
		ON CONFLICT (email) DO UPDATE SET cf_id = excluded.cf_id, verified_at = excluded.verified_at,
		created_at = excluded.created_at, synced_at = excluded.synced_at`,
		strings.ToLower(d.Email), d.CFID, msOrNil(d.VerifiedAt), d.CreatedAt.UnixMilli(), now)
	return err
}

func (s *Store) DeleteDestination(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM destinations WHERE email = ?", email)
	return err
}

func scanDestination(row interface{ Scan(...any) error }) (*Destination, error) {
	var (
		d               Destination
		verified        sql.NullInt64
		created, synced int64
	)
	if err := row.Scan(&d.Email, &d.CFID, &verified, &created, &synced); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if verified.Valid {
		d.VerifiedAt = time.UnixMilli(verified.Int64)
	}
	d.CreatedAt, d.SyncedAt = time.UnixMilli(created), time.UnixMilli(synced)
	return &d, nil
}

func (s *Store) ListDestinations(ctx context.Context) ([]*Destination, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT email, cf_id, verified_at, created_at, synced_at FROM destinations ORDER BY verified_at IS NULL DESC, email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Destination
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDestination(ctx context.Context, email string) (*Destination, error) {
	return scanDestination(s.db.QueryRowContext(ctx, "SELECT email, cf_id, verified_at, created_at, synced_at FROM destinations WHERE email = ?", strings.ToLower(email)))
}
