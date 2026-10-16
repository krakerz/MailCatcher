package store

import (
	"context"
	"strings"
	"time"
)

// Unread means: inbound, received after the user was created, and not opened by them.
const unreadCond = `m.direction = 'in' AND m.received_at >= ? AND NOT EXISTS
	(SELECT 1 FROM message_reads r WHERE r.message_id = m.id AND r.user_id = ?)`

func (s *Store) MarkRead(ctx context.Context, userID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO message_reads (user_id, message_id, read_at) VALUES (?,?,?)`,
		userID, messageID, time.Now().UnixMilli())
	return err
}

func (s *Store) MarkUnread(ctx context.Context, userID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM message_reads WHERE user_id = ? AND message_id = ?", userID, messageID)
	return err
}

// MarkInboxRead marks every inbound message of an inbox as read for the user.
func (s *Store) MarkInboxRead(ctx context.Context, u *User, inboxID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO message_reads (user_id, message_id, read_at)
		SELECT ?, m.id, ? FROM messages m WHERE m.inbox_id = ? AND `+unreadCond,
		u.ID, time.Now().UnixMilli(), inboxID, u.CreatedAt.UnixMilli(), u.ID)
	return err
}

// Unread returns which of ids are unread for the user.
func (s *Store) Unread(ctx context.Context, u *User, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{u.CreatedAt.UnixMilli(), u.ID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT m.id FROM messages m WHERE "+unreadCond+
		" AND m.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// UnreadCounts returns unread inbound messages per inbox for the user.
func (s *Store) UnreadCounts(ctx context.Context, u *User) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT m.inbox_id, count(*) FROM messages m WHERE "+unreadCond+" GROUP BY m.inbox_id",
		u.CreatedAt.UnixMilli(), u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
