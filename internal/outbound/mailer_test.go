package outbound

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mailcatcher/internal/mailparse"
	"mailcatcher/internal/sig"
	"mailcatcher/internal/store"
)

const secret = "0123456789abcdef0123456789abcdef"

type sent struct {
	from, to string
	raw      string
}

// fakeWorker verifies the signature like the real Worker and answers with a new Message-ID.
func fakeWorker(t *testing.T, got *[]sent) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := sig.Verify(secret, r.Header, body, time.Now()); err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		to := r.Header.Get("X-MC-Envelope-To")
		if strings.HasPrefix(to, "unverified") {
			http.Error(w, "destination address is not a verified address", 502)
			return
		}
		*got = append(*got, sent{r.Header.Get("X-MC-Envelope-From"), to, string(body)})
		w.Write([]byte(`{"messageId":"<cf-assigned@example.test>"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T) (*Mailer, *store.Store, *store.Message, *[]sent) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var got []sent
	srv := fakeWorker(t, &got)
	m := &Mailer{Store: st, Sender: &Sender{WorkerURL: srv.URL, Secret: secret}, Domain: "example.test"}

	raw := []byte("From: Alice <alice@gmail.com>\r\nReply-To: alice.replies@gmail.com\r\nTo: b.gmail@example.test\r\n" +
		"Subject: Hello\r\nMessage-ID: <orig@gmail.com>\r\nReferences: <older@gmail.com>\r\nDate: Fri, 16 Oct 2026 10:00:00 +0000\r\n\r\nLine one\r\nLine two\r\n")
	p, _ := mailparse.Parse(raw)
	orig := store.FromParsed(p)
	orig.InboxID, orig.Direction, orig.EnvelopeTo, orig.DecodedTo = 1, "in", "b.gmail@example.test", "b@gmail.com"
	if _, err := st.InsertMessage(context.Background(), orig, raw, nil); err != nil {
		t.Fatal(err)
	}
	return m, st, orig, &got
}

func TestReply(t *testing.T) {
	m, st, orig, got := setup(t)
	uid := int64(7)
	out, err := m.Reply(context.Background(), orig, ReplyOptions{Text: "Thanks!", Quote: true, SentBy: &uid})
	if err != nil {
		t.Fatal(err)
	}
	s := (*got)[0]
	if s.from != "b.gmail@example.test" || s.to != "alice.replies@gmail.com" {
		t.Errorf("envelope %s -> %s", s.from, s.to)
	}
	for _, want := range []string{"Subject: Re: Hello", "In-Reply-To: <orig@gmail.com>", "References: <older@gmail.com> <orig@gmail.com>", "> Line one", "Thanks!"} {
		if !strings.Contains(s.raw, want) {
			t.Errorf("raw lacks %q:\n%s", want, s.raw)
		}
	}

	stored, err := st.GetMessage(context.Background(), out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Direction != "out" || stored.MessageID != "<cf-assigned@example.test>" || stored.ParentID == nil || *stored.ParentID != orig.ID ||
		stored.SentBy == nil || *stored.SentBy != 7 || stored.DecodedTo != "b@gmail.com" {
		t.Errorf("stored %+v", stored)
	}
	raw, _ := os.ReadFile(st.Path(stored.RawPath))
	if !strings.Contains(string(raw), "Message-ID: <cf-assigned@example.test>") {
		t.Error("stored raw keeps the replaced Message-ID")
	}
}

func TestForwardAndCompose(t *testing.T) {
	m, _, orig, got := setup(t)
	if _, err := m.Forward(context.Background(), orig, ForwardOptions{To: "qa@gmail.com", Note: "FYI"}); err != nil {
		t.Fatal(err)
	}
	f := (*got)[0].raw
	for _, want := range []string{"Subject: Fwd: Hello", "Reply-To: <alice.replies@gmail.com>", "message/rfc822", "FYI", "---------- Forwarded message"} {
		if !strings.Contains(f, want) {
			t.Errorf("forward lacks %q", want)
		}
	}
	m.Forward(context.Background(), orig, ForwardOptions{To: "qa@gmail.com", ReplyToMailcatcher: true})
	if !strings.Contains((*got)[1].raw, "Reply-To: <b.gmail@example.test>") {
		t.Error("reply-to-mailcatcher not applied")
	}

	out, err := m.Compose(context.Background(), ComposeOptions{InboxID: 1, Identity: "c@gmail.com", To: "qa@gmail.com", Subject: "New", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if (*got)[2].from != "c.gmail@example.test" || out.DecodedTo != "c@gmail.com" {
		t.Errorf("compose from %s decoded %s", (*got)[2].from, out.DecodedTo)
	}
	if _, err := m.Compose(context.Background(), ComposeOptions{InboxID: 1, Identity: "c@yahoo.co.id", To: "qa@gmail.com"}); err == nil {
		t.Error("identity that the format cannot express was accepted")
	}
}

func TestNotVerified(t *testing.T) {
	m, _, orig, _ := setup(t)
	if _, err := m.Forward(context.Background(), orig, ForwardOptions{To: "unverified@gmail.com"}); err != ErrNotVerified {
		t.Fatalf("got %v", err)
	}
}

func TestReplaceMessageID(t *testing.T) {
	raw := []byte("Subject: x\r\nMessage-Id: <a@b>\r\n\r\nMessage-ID: <body@line>\r\n")
	got := string(ReplaceMessageID(raw, "<new@id>"))
	if !strings.Contains(got, "Message-ID: <new@id>\r\n\r\n") || !strings.Contains(got, "<body@line>") {
		t.Errorf("got %q", got)
	}
}
