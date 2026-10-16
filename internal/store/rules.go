package store

import (
	"context"
	"database/sql"
	"time"
)

type ForwardRule struct {
	ID          int64
	InboxID     int64 // 0 = any inbox
	AddressGlob string
	Destination string
	Enabled     bool
	CreatedAt   time.Time
}

func (s *Store) ListRules(ctx context.Context) ([]*ForwardRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, coalesce(inbox_id, 0), address_glob, destination, enabled, created_at
		FROM forward_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ForwardRule
	for rows.Next() {
		var (
			r       ForwardRule
			created int64
		)
		if err := rows.Scan(&r.ID, &r.InboxID, &r.AddressGlob, &r.Destination, &r.Enabled, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(created)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRule(ctx context.Context, r *ForwardRule) error {
	var inbox any
	if r.InboxID != 0 {
		inbox = r.InboxID
	}
	r.CreatedAt = time.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO forward_rules (inbox_id, address_glob, destination, enabled, created_at)
		VALUES (?,?,?,?,?)`, inbox, r.AddressGlob, r.Destination, r.Enabled, r.CreatedAt.UnixMilli())
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

func (s *Store) SetRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	return expectRow(s.db.ExecContext(ctx, "UPDATE forward_rules SET enabled = ? WHERE id = ?", enabled, id))
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	return expectRow(s.db.ExecContext(ctx, "DELETE FROM forward_rules WHERE id = ?", id))
}

func expectRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetRule(ctx context.Context, id int64) (*ForwardRule, error) {
	list, err := s.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, ErrNotFound
}
