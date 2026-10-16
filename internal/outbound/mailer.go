package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"mailcatcher/internal/address"
	"mailcatcher/internal/mailparse"
	"mailcatcher/internal/store"
)

// Mailer sends replies, forwards and new mail, and stores what was sent.
type Mailer struct {
	Store  *store.Store
	Sender *Sender
	Domain string // main catch-all domain
	// Verified, when set, is checked before sending so an unverified recipient
	// fails fast with ErrNotVerified instead of after a round trip to Cloudflare.
	Verified func(ctx context.Context, email string) (bool, error)
}

// ErrUnsupportedAddress: a compose identity the inbox format cannot express.
var ErrUnsupportedAddress = address.ErrUnsupported

// MaxUpload caps attachments added in the UI/API; Cloudflare's limit is 25 MiB encoded.
const MaxUpload = 18 << 20

func (m *Mailer) send(ctx context.Context, d Draft, inboxID int64, parentID, sentBy *int64, decoded string) (*store.Message, error) {
	if m.Verified != nil {
		ok, err := m.Verified(ctx, d.To)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotVerified
		}
	}
	fromAddr, err := mail.ParseAddress(d.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	_, fromDomain, _ := address.Split(fromAddr.Address)
	raw, err := Build(d, fromDomain, time.Now())
	if err != nil {
		return nil, err
	}
	id, err := m.Sender.Send(ctx, fromAddr.Address, d.To, raw)
	if err != nil {
		return nil, err
	}
	raw = ReplaceMessageID(raw, id)

	p, err := mailparse.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("sent, but storing the copy failed: %w", err)
	}
	msg := store.FromParsed(p)
	msg.Direction, msg.InboxID = "out", inboxID
	msg.EnvelopeFrom, msg.EnvelopeTo = strings.ToLower(fromAddr.Address), strings.ToLower(d.To)
	msg.SentBy, msg.ParentID, msg.DecodedTo = sentBy, parentID, decoded
	if id != "" {
		msg.MessageID = id
	}
	if _, err := m.Store.InsertMessage(ctx, msg, raw, p.Attachments); err != nil {
		return nil, fmt.Errorf("sent, but storing the copy failed: %w", err)
	}
	return msg, nil
}

// identity is the catch-all address a received message was sent to.
func identity(orig *store.Message) string {
	if orig.EnvelopeTo != "" {
		return orig.EnvelopeTo
	}
	if len(orig.To) > 0 {
		return orig.To[0].Address
	}
	return ""
}

// ReplyTarget is where a reply to orig goes: its Reply-To, else its From.
func (m *Mailer) ReplyTarget(orig *store.Message) string {
	if f, err := os.Open(m.Store.Path(orig.RawPath)); err == nil {
		defer f.Close()
		if msg, err := mail.ReadMessage(f); err == nil {
			if list, err := msg.Header.AddressList("Reply-To"); err == nil && len(list) > 0 {
				return strings.ToLower(list[0].Address)
			}
		}
	}
	if orig.From.Address != "" {
		return orig.From.Address
	}
	return orig.EnvelopeFrom
}

func attribution(orig *store.Message) string {
	who := orig.From.Address
	if orig.From.Name != "" {
		who = orig.From.Name + " <" + orig.From.Address + ">"
	}
	when := orig.Date
	if when.IsZero() {
		when = orig.ReceivedAt
	}
	return fmt.Sprintf("On %s, %s wrote:", when.Format("Mon, 2 Jan 2006 at 15:04"), who)
}

type ReplyOptions struct {
	Text   string
	Quote  bool
	Files  []File
	SentBy *int64
}

func (m *Mailer) Reply(ctx context.Context, orig *store.Message, o ReplyOptions) (*store.Message, error) {
	from := identity(orig)
	if from == "" {
		return nil, errors.New("the original has no recipient to reply from")
	}
	body := o.Text
	if o.Quote && strings.TrimSpace(orig.Text) != "" {
		body = strings.TrimRight(body, "\n") + "\n\n" + Quote(attribution(orig), orig.Text)
	}
	refs := append([]string{}, orig.References...)
	if orig.MessageID != "" {
		refs = append(refs, orig.MessageID)
	}
	d := Draft{
		From:       from,
		To:         m.ReplyTarget(orig),
		Subject:    ReplySubject(orig.Subject),
		Text:       body,
		InReplyTo:  orig.MessageID,
		References: refs,
		Files:      o.Files,
	}
	return m.send(ctx, d, orig.InboxID, &orig.ID, o.SentBy, orig.DecodedTo)
}

type ForwardOptions struct {
	To   string
	Note string
	// ReplyToMailcatcher routes replies to the forward back into mailcatcher
	// instead of to the original sender.
	ReplyToMailcatcher bool
	SentBy             *int64
}

func (m *Mailer) Forward(ctx context.Context, orig *store.Message, o ForwardOptions) (*store.Message, error) {
	from := identity(orig)
	if from == "" {
		return nil, errors.New("the original has no recipient to forward from")
	}
	raw, err := os.ReadFile(m.Store.Path(orig.RawPath))
	if err != nil {
		return nil, err
	}
	replyTo := m.ReplyTarget(orig)
	if o.ReplyToMailcatcher {
		replyTo = from
	}
	var b strings.Builder
	if n := strings.TrimSpace(o.Note); n != "" {
		b.WriteString(n + "\n\n")
	}
	b.WriteString("---------- Forwarded message ----------\n")
	fmt.Fprintf(&b, "From: %s\n", headerAddr(orig.From))
	if !orig.Date.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", orig.Date.Format(time.RFC1123Z))
	}
	fmt.Fprintf(&b, "Subject: %s\n", orig.Subject)
	fmt.Fprintf(&b, "To: %s\n\n", from)
	b.WriteString(orig.Text)

	d := Draft{
		From:          from,
		To:            strings.TrimSpace(o.To),
		ReplyTo:       replyTo,
		Subject:       ForwardSubject(orig.Subject),
		Text:          b.String(),
		Forwarded:     raw,
		ForwardedName: "original.eml",
	}
	return m.send(ctx, d, orig.InboxID, &orig.ID, o.SentBy, orig.DecodedTo)
}

func headerAddr(a mailparse.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.Name + " <" + a.Address + ">"
}

type ComposeOptions struct {
	InboxID int64
	// Identity is the real address to send as (b@gmail.com, encoded with the inbox
	// format) or a catch-all address used as is.
	Identity string
	To       string
	Subject  string
	Text     string
	Files    []File
	SentBy   *int64
}

// FromAddress resolves a compose identity to the catch-all From address and the
// real address it stands for.
func (m *Mailer) FromAddress(in *store.Inbox, ident string) (from, decoded string, err error) {
	ident = strings.ToLower(strings.TrimSpace(ident))
	f, err := address.For(in)
	if err != nil {
		return "", "", err
	}
	catch := address.CatchDomain(in, m.Domain)
	if local, domain, ok := address.Split(ident); ok && domain == catch {
		decoded, _ := f.Decode(local)
		return ident, decoded, nil
	}
	local, err := f.Encode(ident)
	if err != nil {
		return "", "", err
	}
	return local + "@" + catch, ident, nil
}

func (m *Mailer) Compose(ctx context.Context, o ComposeOptions) (*store.Message, error) {
	in, err := m.Store.GetInbox(ctx, o.InboxID)
	if err != nil {
		return nil, err
	}
	from, decoded, err := m.FromAddress(in, o.Identity)
	if err != nil {
		return nil, err
	}
	d := Draft{From: from, To: strings.TrimSpace(o.To), Subject: o.Subject, Text: o.Text, Files: o.Files}
	return m.send(ctx, d, in.ID, nil, o.SentBy, decoded)
}
