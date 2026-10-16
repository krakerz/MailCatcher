package outbound

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"

	"mailcatcher/internal/version"
)

type File struct {
	Name        string
	ContentType string
	Data        []byte
}

// Draft is one outgoing message to a single recipient (Cloudflare sends to one).
type Draft struct {
	From       string
	To         string
	ReplyTo    string
	Subject    string
	Text       string
	InReplyTo  string
	References []string
	Files      []File
	// Forwarded is attached as message/rfc822 when set.
	Forwarded     []byte
	ForwardedName string
}

// Build renders the draft as MIME. msgDomain is used for the (soon replaced) Message-ID.
func Build(d Draft, msgDomain string, now time.Time) ([]byte, error) {
	m := gomail.NewMsg()
	if err := m.From(d.From); err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	if err := m.To(d.To); err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if d.ReplyTo != "" {
		if err := m.ReplyTo(d.ReplyTo); err != nil {
			return nil, fmt.Errorf("reply-to: %w", err)
		}
	}
	m.Subject(d.Subject)
	m.SetDateWithValue(now)
	m.SetMessageIDWithValue(fmt.Sprintf("%d.%s@%s", now.UnixNano(), randHex(), msgDomain))
	m.SetGenHeader(gomail.HeaderXMailer, "mailcatcher "+version.Version)
	if d.InReplyTo != "" {
		m.SetGenHeader(gomail.HeaderInReplyTo, d.InReplyTo)
	}
	if len(d.References) > 0 {
		m.SetGenHeaderPreformatted(gomail.HeaderReferences, strings.Join(d.References, " "))
	}
	m.SetBodyString(gomail.TypeTextPlain, d.Text)
	for _, f := range d.Files {
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := m.AttachReader(f.Name, bytes.NewReader(f.Data), gomail.WithFileContentType(gomail.ContentType(ct))); err != nil {
			return nil, err
		}
	}
	if d.Forwarded != nil {
		name := d.ForwardedName
		if name == "" {
			name = "forwarded.eml"
		}
		if err := m.AttachReader(name, bytes.NewReader(d.Forwarded), gomail.WithFileContentType("message/rfc822")); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReplaceMessageID swaps the Message-ID header value in raw (headers only).
func ReplaceMessageID(raw []byte, newID string) []byte {
	if newID == "" {
		return raw
	}
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		return raw
	}
	head := raw[:end]
	lines := bytes.Split(head, []byte("\r\n"))
	for i, l := range lines {
		if len(l) > 11 && strings.EqualFold(string(l[:11]), "message-id:") {
			lines[i] = []byte("Message-ID: " + newID)
			return append(bytes.Join(lines, []byte("\r\n")), raw[end:]...)
		}
	}
	return raw
}

// ReplySubject adds "Re: " unless the subject already starts with it.
func ReplySubject(s string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "re:") {
		return s
	}
	return "Re: " + s
}

func ForwardSubject(s string) string {
	if l := strings.ToLower(strings.TrimSpace(s)); strings.HasPrefix(l, "fwd:") || strings.HasPrefix(l, "fw:") {
		return s
	}
	return "Fwd: " + s
}

// Quote prefixes every line with "> " under an attribution line.
func Quote(attribution, text string) string {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l == "" || strings.HasPrefix(l, ">") {
			lines[i] = ">" + l
		} else {
			lines[i] = "> " + l
		}
	}
	return attribution + "\n" + strings.Join(lines, "\n") + "\n"
}

func randHex() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
