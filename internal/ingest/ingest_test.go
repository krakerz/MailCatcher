package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mailcatcher/internal/sig"
	"mailcatcher/internal/store"
)

const secret = "0123456789abcdef0123456789abcdef"

var now = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func mail(msgID, inReplyTo, body string) []byte {
	s := "From: Alice <alice@example.com>\nTo: b.gmail@example.test\nSubject: hi\n" +
		"Message-ID: " + msgID + "\n"
	if inReplyTo != "" {
		s += "In-Reply-To: " + inReplyTo + "\n"
	}
	s += "MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=b\n\n" +
		"--b\nContent-Type: text/plain\n\n" + body + "\n" +
		"--b\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=a.pdf\nContent-Transfer-Encoding: base64\n\nJVBERi0=\n--b--\n"
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

func setup(t *testing.T) (*Handler, *store.Store, *[]int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var stored []int64
	h := &Handler{Store: st, Secret: secret, Now: func() time.Time { return now },
		OnStored: func(m *store.Message) { stored = append(stored, m.ID) }}
	return h, st, &stored
}

func post(h http.Handler, raw []byte, signWith string, at time.Time) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/internal/ingest", bytes.NewReader(raw))
	sig.SetHeaders(req.Header, signWith, raw, at)
	req.Header.Set(HeaderEnvelopeFrom, "Alice@Example.com")
	req.Header.Set(HeaderEnvelopeTo, "b.gmail@example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIngestStoresMessage(t *testing.T) {
	h, st, stored := setup(t)
	rec := post(h, mail("<m1@example.com>", "", "Your code is 552211"), secret, now)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		ID        int64
		Duplicate bool
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ID == 0 || resp.Duplicate || len(*stored) != 1 {
		t.Fatalf("resp %+v stored %v", resp, *stored)
	}

	ctx := context.Background()
	m, err := st.GetMessage(ctx, resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Direction != "in" || m.EnvelopeFrom != "alice@example.com" || m.MessageID != "<m1@example.com>" {
		t.Errorf("message %+v", m)
	}
	if len(m.Codes) != 1 || m.Codes[0] != "552211" || !m.ReceivedAt.Equal(now) {
		t.Errorf("codes %v received %v", m.Codes, m.ReceivedAt)
	}
	if raw, err := os.ReadFile(st.Path(m.RawPath)); err != nil || int64(len(raw)) != m.Size {
		t.Errorf("raw file: %v len %d", err, len(raw))
	}

	atts, err := st.Attachments(ctx, m.ID)
	if err != nil || len(atts) != 1 || atts[0].Filename != "a.pdf" {
		t.Fatalf("attachments %+v %v", atts, err)
	}
	if data, err := os.ReadFile(st.Path(atts[0].Path)); err != nil || string(data) != "%PDF-" {
		t.Errorf("attachment file %q %v", data, err)
	}
}

func TestIngestDuplicateAndThreading(t *testing.T) {
	h, st, stored := setup(t)
	first := mail("<m1@example.com>", "", "first")
	post(h, first, secret, now)

	rec := post(h, first, secret, now)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"duplicate":true`) || len(*stored) != 1 {
		t.Fatalf("duplicate not detected: %s stored %v", rec.Body, *stored)
	}

	rec = post(h, mail("<m2@example.com>", "<m1@example.com>", "reply"), secret, now)
	var resp struct{ ID int64 }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	m, err := st.GetMessage(context.Background(), resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.ParentID == nil || *m.ParentID != (*stored)[0] {
		t.Errorf("parent %v, want %d", m.ParentID, (*stored)[0])
	}
}

func TestIngestRejectsBadSignature(t *testing.T) {
	h, _, stored := setup(t)
	raw := mail("<m1@example.com>", "", "x")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong secret": post(h, raw, strings.Repeat("x", 32), now),
		"stale":        post(h, raw, secret, now.Add(-10*time.Minute)),
	} {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if len(*stored) != 0 {
		t.Errorf("stored %v", *stored)
	}
}
