package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/events"
	"mailcatcher/internal/ingest"
	"mailcatcher/internal/store"
)

const key = "mc_test-key"

func setup(t *testing.T) (*httptest.Server, *store.Store, *events.Broker) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetInboxAPIKeyHash(context.Background(), 1, auth.HashToken(key)); err != nil {
		t.Fatal(err)
	}
	b := events.NewBroker()
	mux := http.NewServeMux()
	(&API{Store: st, Broker: b}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st, b
}

func add(t *testing.T, st *store.Store, b *events.Broker, to, subject string) int64 {
	t.Helper()
	raw := []byte("From: a@example.com\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nMessage-ID: <" +
		strconv.FormatInt(time.Now().UnixNano(), 10) + "@x>\r\n\r\nYour code is 424242\r\n")
	m, atts, err := ingest.Build(raw, "a@example.com", to)
	if err != nil {
		t.Fatal(err)
	}
	m.InboxID = 1
	if _, err := st.InsertMessage(context.Background(), m, raw, atts); err != nil {
		t.Fatal(err)
	}
	b.Publish(events.Event{MessageID: m.ID, InboxID: 1})
	return m.ID
}

func get(t *testing.T, url, k string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if v != nil {
		json.NewDecoder(res.Body).Decode(v)
	}
	return res.StatusCode
}

type list struct {
	Messages []struct {
		ID         int64
		EnvelopeTo string `json:"envelope_to"`
		Codes      []string
	}
}

func TestAuth(t *testing.T) {
	srv, _, _ := setup(t)
	if code := get(t, srv.URL+"/api/inboxes/1/messages", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no key: %d", code)
	}
	if code := get(t, srv.URL+"/api/inboxes/1/messages", "mc_wrong", nil); code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", code)
	}
	if code := get(t, srv.URL+"/api/inboxes/2/messages", key, nil); code != http.StatusForbidden {
		t.Errorf("other inbox: %d", code)
	}
}

func TestListFilterAndGet(t *testing.T) {
	srv, st, b := setup(t)
	add(t, st, b, "x.gmail@example.test", "one")
	id := add(t, st, b, "y.gmail@example.test", "two")

	var l list
	get(t, srv.URL+"/api/inboxes/1/messages?to=y.gmail", key, &l)
	if len(l.Messages) != 1 || l.Messages[0].ID != id || l.Messages[0].Codes[0] != "424242" {
		t.Fatalf("filter: %+v", l)
	}
	var m struct{ Subject, Text string }
	if code := get(t, srv.URL+"/api/messages/"+strconv.FormatInt(id, 10), key, &m); code != 200 || m.Subject != "two" || !strings.Contains(m.Text, "424242") {
		t.Fatalf("get: %d %+v", code, m)
	}
	if code := get(t, srv.URL+"/api/messages/999", key, nil); code != http.StatusNotFound {
		t.Fatalf("missing: %d", code)
	}
}

func TestLongPollWakesOnNewMessage(t *testing.T) {
	srv, st, b := setup(t)
	add(t, st, b, "other@example.test", "noise")

	go func() {
		time.Sleep(200 * time.Millisecond)
		add(t, st, b, "other@example.test", "still noise") // wakes the poll, but does not match
		time.Sleep(200 * time.Millisecond)
		add(t, st, b, "wanted@example.test", "hit")
	}()
	start := time.Now()
	var l list
	get(t, srv.URL+"/api/inboxes/1/messages?to=wanted&wait=5s", key, &l)
	if el := time.Since(start); len(l.Messages) != 1 || el > 2*time.Second {
		t.Fatalf("got %+v after %v", l, el)
	}
}

func TestLongPollTimeout(t *testing.T) {
	srv, _, _ := setup(t)
	start := time.Now()
	var l list
	if code := get(t, srv.URL+"/api/inboxes/1/messages?to=nobody&wait=300ms", key, &l); code != 200 || len(l.Messages) != 0 {
		t.Fatalf("%d %+v", code, l)
	}
	if el := time.Since(start); el < 300*time.Millisecond {
		t.Fatalf("returned early: %v", el)
	}
}

func TestDelete(t *testing.T) {
	srv, st, b := setup(t)
	add(t, st, b, "a@example.test", "1")
	add(t, st, b, "b@example.test", "2")
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/inboxes/1/messages?to=a@", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Deleted int }
	json.NewDecoder(res.Body).Decode(&d)
	res.Body.Close()
	var l list
	get(t, srv.URL+"/api/inboxes/1/messages", key, &l)
	if d.Deleted != 1 || len(l.Messages) != 1 || l.Messages[0].EnvelopeTo != "b@example.test" {
		t.Fatalf("deleted %d, left %+v", d.Deleted, l)
	}
}
