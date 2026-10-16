package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/mailparse"
)

var ErrNotFound = errors.New("not found")

type Message struct {
	ID           int64
	InboxID      int64
	Direction    string // "in" | "out"
	MessageID    string
	InReplyTo    string
	References   []string
	EnvelopeFrom string
	EnvelopeTo   string
	From         mailparse.Address
	To           []mailparse.Address
	Cc           []mailparse.Address
	Subject      string
	Date         time.Time // zero if the message had none
	ReceivedAt   time.Time
	Size         int64
	RawPath      string
	RawSHA256    string
	Text         string
	HTML         string
	Links        []string
	Codes        []string
	DecodedTo    string
	SentBy       *int64
	ParentID     *int64

	AutoForwarded   []string // destinations the Worker forwarded this inbound mail to
	AttachmentCount int      // non-inline attachments; filled by reads only
}

type Attachment struct {
	ID          int64
	MessageID   int64
	Filename    string
	ContentType string
	Size        int64
	Path        string
	ContentID   string
	Inline      bool
}

// FromParsed fills a Message from a parse result; the caller sets direction, inbox and envelope.
func FromParsed(p *mailparse.Parsed) *Message {
	return &Message{
		MessageID:  p.MessageID,
		InReplyTo:  p.InReplyTo,
		References: p.References,
		From:       p.From,
		To:         p.To,
		Cc:         p.Cc,
		Subject:    p.Subject,
		Date:       p.Date,
		Text:       p.Text,
		HTML:       p.HTML,
		Links:      p.Links,
		Codes:      p.Codes,
	}
}

func (s *Store) DefaultInboxID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM inboxes WHERE is_default = 1 ORDER BY id LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// InsertMessage stores m with its raw source and attachments. Identical raw bytes
// (e.g. a KV-stashed copy drained after a successful ingest) are stored once:
// dup is true and m.ID is set to the existing row.
func (s *Store) InsertMessage(ctx context.Context, m *Message, raw []byte, atts []mailparse.Attachment) (dup bool, err error) {
	sum := sha256.Sum256(raw)
	m.RawSHA256 = hex.EncodeToString(sum[:])
	m.Size = int64(len(raw))
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now()
	}

	err = s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE raw_sha256 = ?", m.RawSHA256).Scan(&m.ID)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	var written []string
	defer func() {
		if err != nil {
			tx.Rollback()
			for _, p := range written {
				os.RemoveAll(p)
			}
		}
	}()

	if m.ParentID == nil {
		m.ParentID, err = findParent(ctx, tx, m)
		if err != nil {
			return false, err
		}
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO messages (
		inbox_id, direction, message_id, in_reply_to, refs, envelope_from, envelope_to,
		from_name, from_addr, to_json, cc_json, subject, date, received_at, size,
		raw_sha256, text, html, links, codes, decoded_to, sent_by, parent_id, auto_forwarded)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.InboxID, m.Direction, m.MessageID, m.InReplyTo, jsonStr(m.References), m.EnvelopeFrom, m.EnvelopeTo,
		m.From.Name, m.From.Address, jsonStr(m.To), jsonStr(m.Cc), m.Subject, msOrNil(m.Date), m.ReceivedAt.UnixMilli(), m.Size,
		m.RawSHA256, m.Text, m.HTML, jsonStr(m.Links), jsonStr(m.Codes), m.DecodedTo, m.SentBy, m.ParentID, jsonStr(m.AutoForwarded))
	if err != nil {
		// Lost a race with a concurrent ingest of the same bytes.
		if strings.Contains(err.Error(), "UNIQUE constraint failed: messages.raw_sha256") {
			tx.Rollback()
			err = s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE raw_sha256 = ?", m.RawSHA256).Scan(&m.ID)
			return err == nil, err
		}
		return false, err
	}
	if m.ID, err = res.LastInsertId(); err != nil {
		return false, err
	}

	m.RawPath = filepath.Join("raw", strconv.FormatInt(m.ID, 10)+".eml")
	if err = writeFile(s.Path(m.RawPath), raw); err != nil {
		return false, err
	}
	written = append(written, s.Path(m.RawPath))
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET raw_path = ? WHERE id = ?", m.RawPath, m.ID); err != nil {
		return false, err
	}

	attDir := filepath.Join("att", strconv.FormatInt(m.ID, 10))
	if len(atts) > 0 {
		if err = os.MkdirAll(s.Path(attDir), 0o750); err != nil {
			return false, err
		}
		written = append(written, s.Path(attDir))
	}
	for _, a := range atts {
		res, err = tx.ExecContext(ctx, `INSERT INTO attachments (message_id, filename, content_type, size, cid, inline)
			VALUES (?,?,?,?,?,?)`, m.ID, a.Filename, a.ContentType, len(a.Data), a.ContentID, a.Inline)
		if err != nil {
			return false, err
		}
		aid, err2 := res.LastInsertId()
		if err = err2; err != nil {
			return false, err
		}
		rel := filepath.Join(attDir, strconv.FormatInt(aid, 10))
		if err = writeFile(s.Path(rel), a.Data); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE attachments SET path = ? WHERE id = ?", rel, aid); err != nil {
			return false, err
		}
	}
	return false, tx.Commit()
}

// findParent links a reply to the stored message it answers: In-Reply-To first,
// then References from newest to oldest.
func findParent(ctx context.Context, tx *sql.Tx, m *Message) (*int64, error) {
	ids := make([]string, 0, 1+len(m.References))
	if m.InReplyTo != "" {
		ids = append(ids, m.InReplyTo)
	}
	for i := len(m.References) - 1; i >= 0; i-- {
		ids = append(ids, m.References[i])
	}
	for _, mid := range ids {
		var id int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE message_id = ? ORDER BY id DESC LIMIT 1", mid).Scan(&id)
		if err == nil {
			return &id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Store) GetMessage(ctx context.Context, id int64) (*Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, "SELECT "+msgCols+", text, html FROM messages WHERE id = ?", id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *Store) Attachments(ctx context.Context, messageID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id, filename, content_type, size, path, cid, inline
		FROM attachments WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.Size, &a.Path, &a.ContentID, &a.Inline); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func msOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
