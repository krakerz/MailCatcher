package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Everything except the (possibly large) bodies.
const msgCols = `id, inbox_id, direction, message_id, in_reply_to, refs, envelope_from, envelope_to,
	from_name, from_addr, to_json, cc_json, subject, date, received_at, size, raw_path, raw_sha256,
	links, codes, decoded_to, sent_by, parent_id,
	(SELECT count(*) FROM attachments a WHERE a.message_id = messages.id AND a.inline = 0)`

func scanMessage(row interface{ Scan(...any) error }, bodies bool) (*Message, error) {
	var (
		m                          Message
		refs, to, cc, links, codes string
		date                       sql.NullInt64
		received                   int64
		sentBy, parent             sql.NullInt64
	)
	dst := []any{&m.ID, &m.InboxID, &m.Direction, &m.MessageID, &m.InReplyTo, &refs, &m.EnvelopeFrom, &m.EnvelopeTo,
		&m.From.Name, &m.From.Address, &to, &cc, &m.Subject, &date, &received, &m.Size, &m.RawPath, &m.RawSHA256,
		&links, &codes, &m.DecodedTo, &sentBy, &parent, &m.AttachmentCount}
	if bodies {
		dst = append(dst, &m.Text, &m.HTML)
	}
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		src string
		dst any
	}{{refs, &m.References}, {to, &m.To}, {cc, &m.Cc}, {links, &m.Links}, {codes, &m.Codes}} {
		if err := json.Unmarshal([]byte(f.src), f.dst); err != nil {
			return nil, fmt.Errorf("message %d: %w", m.ID, err)
		}
	}
	if date.Valid {
		m.Date = time.UnixMilli(date.Int64)
	}
	m.ReceivedAt = time.UnixMilli(received)
	if sentBy.Valid {
		m.SentBy = &sentBy.Int64
	}
	if parent.Valid {
		m.ParentID = &parent.Int64
	}
	return &m, nil
}

// Filter selects messages. Text fields are case-insensitive substring matches.
type Filter struct {
	InboxID   int64
	Direction string // "in", "out" or "" for both
	To        string // envelope recipient, To or Cc
	From      string // envelope sender or From
	Subject   string
	Q         string // any of to/from/subject
	Since     time.Time
	AfterID   int64 // id > AfterID (newer)
	BeforeID  int64 // id < BeforeID (older, for paging)
	Limit     int   // default 50, max 500
}

func likeArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func (f Filter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	add := func(c string, a ...any) { conds = append(conds, c); args = append(args, a...) }
	if f.InboxID != 0 {
		add("inbox_id = ?", f.InboxID)
	}
	if f.Direction != "" {
		add("direction = ?", f.Direction)
	}
	if f.To != "" {
		l := likeArg(f.To)
		add(`(envelope_to LIKE ? ESCAPE '\' OR to_json LIKE ? ESCAPE '\' OR cc_json LIKE ? ESCAPE '\' OR decoded_to LIKE ? ESCAPE '\')`, l, l, l, l)
	}
	if f.From != "" {
		l := likeArg(f.From)
		add(`(envelope_from LIKE ? ESCAPE '\' OR from_addr LIKE ? ESCAPE '\' OR from_name LIKE ? ESCAPE '\')`, l, l, l)
	}
	if f.Subject != "" {
		add(`subject LIKE ? ESCAPE '\'`, likeArg(f.Subject))
	}
	if f.Q != "" {
		l := likeArg(f.Q)
		add(`(envelope_to LIKE ? ESCAPE '\' OR to_json LIKE ? ESCAPE '\' OR decoded_to LIKE ? ESCAPE '\'
			OR envelope_from LIKE ? ESCAPE '\' OR from_addr LIKE ? ESCAPE '\' OR from_name LIKE ? ESCAPE '\'
			OR subject LIKE ? ESCAPE '\')`, l, l, l, l, l, l, l)
	}
	if !f.Since.IsZero() {
		add("received_at >= ?", f.Since.UnixMilli())
	}
	if f.AfterID != 0 {
		add("id > ?", f.AfterID)
	}
	if f.BeforeID != 0 {
		add("id < ?", f.BeforeID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListMessages returns matches newest first, without bodies.
func (s *Store) ListMessages(ctx context.Context, f Filter) ([]*Message, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, "SELECT "+msgCols+" FROM messages"+where+" ORDER BY id DESC LIMIT ?", append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMessages removes matching messages with their files and returns how many.
func (s *Store) DeleteMessages(ctx context.Context, f Filter) (int, error) {
	where, args := f.where()
	if where == "" {
		return 0, errors.New("refusing to delete without a filter")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, raw_path FROM messages"+where, args...)
	if err != nil {
		return 0, err
	}
	type victim struct {
		id  int64
		raw string
	}
	var vs []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.id, &v.raw); err != nil {
			rows.Close()
			return 0, err
		}
		vs = append(vs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, v := range vs {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", v.id); err != nil {
			return 0, err
		}
		if v.raw != "" {
			os.Remove(s.Path(v.raw))
		}
		os.RemoveAll(s.Path(filepath.Join("att", strconv.FormatInt(v.id, 10))))
	}
	return len(vs), nil
}

func (s *Store) GetAttachment(ctx context.Context, messageID, id int64) (*Attachment, error) {
	var a Attachment
	err := s.db.QueryRowContext(ctx, `SELECT id, message_id, filename, content_type, size, path, cid, inline
		FROM attachments WHERE message_id = ? AND id = ?`, messageID, id).
		Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.Size, &a.Path, &a.ContentID, &a.Inline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// Inbox is a virtual inbox; address formats and routing arrive with step 4.
type Inbox struct {
	ID             int64
	Name           string
	Slug           string
	Domain         string
	FormatTemplate string
	AssumedTLD     string
	Priority       int
	IsDefault      bool
	HasAPIKey      bool
}

const inboxCols = `id, name, slug, coalesce(domain, ''), format_template, assumed_tld, priority, is_default, api_key_hash IS NOT NULL`

func scanInbox(row interface{ Scan(...any) error }) (*Inbox, error) {
	var in Inbox
	err := row.Scan(&in.ID, &in.Name, &in.Slug, &in.Domain, &in.FormatTemplate, &in.AssumedTLD, &in.Priority, &in.IsDefault, &in.HasAPIKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &in, err
}

func (s *Store) ListInboxes(ctx context.Context) ([]*Inbox, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+inboxCols+" FROM inboxes ORDER BY priority, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Inbox
	for rows.Next() {
		in, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) GetInbox(ctx context.Context, id int64) (*Inbox, error) {
	return scanInbox(s.db.QueryRowContext(ctx, "SELECT "+inboxCols+" FROM inboxes WHERE id = ?", id))
}

func (s *Store) InboxByAPIKeyHash(ctx context.Context, hash string) (*Inbox, error) {
	return scanInbox(s.db.QueryRowContext(ctx, "SELECT "+inboxCols+" FROM inboxes WHERE api_key_hash = ?", hash))
}

func (s *Store) SetInboxAPIKeyHash(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE inboxes SET api_key_hash = ? WHERE id = ?", hash, id)
	return err
}

// Children returns messages that answer id (replies and forwards), oldest first.
func (s *Store) Children(ctx context.Context, id int64) ([]*Message, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+msgCols+" FROM messages WHERE parent_id = ? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
