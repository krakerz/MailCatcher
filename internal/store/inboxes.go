package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) CreateInbox(ctx context.Context, in *Inbox) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO inboxes (name, slug, domain, format_template, assumed_tld, priority, addresses, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, in.Name, in.Slug, nullStr(in.Domain), in.FormatTemplate, in.AssumedTLD, in.Priority, strings.Join(in.Addresses, "\n"), time.Now().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrExists
		}
		return err
	}
	in.ID, err = res.LastInsertId()
	return err
}

func (s *Store) UpdateInbox(ctx context.Context, in *Inbox) error {
	_, err := s.db.ExecContext(ctx, `UPDATE inboxes SET name = ?, slug = ?, domain = ?, format_template = ?, assumed_tld = ?, priority = ?, addresses = ?
		WHERE id = ?`, in.Name, in.Slug, nullStr(in.Domain), in.FormatTemplate, in.AssumedTLD, in.Priority, strings.Join(in.Addresses, "\n"), in.ID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrExists
	}
	return err
}

func (s *Store) SetDefaultInbox(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM inboxes WHERE id = ?", id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE inboxes SET is_default = (id = ?)", id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteInbox removes a non-default inbox with all its messages and their files.
func (s *Store) DeleteInbox(ctx context.Context, id int64) error {
	in, err := s.GetInbox(ctx, id)
	if err != nil {
		return err
	}
	if in.IsDefault {
		return errors.New("the default inbox cannot be deleted")
	}
	if _, err := s.DeleteMessages(ctx, Filter{InboxID: id}); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM inboxes WHERE id = ?", id)
	return err
}

// SetRouting updates where a message belongs after (re)routing.
func (s *Store) SetRouting(ctx context.Context, messageID, inboxID int64, decodedTo string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE messages SET inbox_id = ?, decoded_to = ? WHERE id = ?", inboxID, decodedTo, messageID)
	return err
}

type Routing struct {
	EnvelopeTo string
	InboxID    int64
	DecodedTo  string
}

// InboundRouting returns recipient, inbox and decoded address of every inbound message.
func (s *Store) InboundRouting(ctx context.Context) (map[int64]Routing, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, envelope_to, inbox_id, decoded_to FROM messages WHERE direction = 'in'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Routing{}
	for rows.Next() {
		var (
			id int64
			r  Routing
		)
		if err := rows.Scan(&id, &r.EnvelopeTo, &r.InboxID, &r.DecodedTo); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// Purge deletes all messages received before t, with their files.
func (s *Store) Purge(ctx context.Context, before time.Time) (int, error) {
	return s.DeleteMessages(ctx, Filter{Until: before})
}

// UserInboxIDs returns the inboxes a user may open; nil means all.
func (s *Store) UserInboxIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT inbox_id FROM user_inboxes WHERE user_id = ? ORDER BY inbox_id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetUserInboxes replaces a user's inbox access; an empty list means all inboxes.
func (s *Store) SetUserInboxes(ctx context.Context, userID int64, inboxIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_inboxes WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, id := range inboxIDs {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO user_inboxes (user_id, inbox_id) SELECT ?, id FROM inboxes WHERE id = ?", userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
