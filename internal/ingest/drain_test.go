package ingest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcatcher/internal/outbound"
	"mailcatcher/internal/sig"
)

// fakeKV imitates the Worker's /pending endpoints, including the "mail waiting" flag.
type fakeKV struct {
	mu     sync.Mutex
	items  map[string][]byte
	meta   map[string]map[string]any
	flag   bool
	full   int // lists asked with ?all=1
	polled int // lists asked without it
}

func (f *fakeKV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if sig.Verify(secret, r.Header, body, time.Now()) != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/pending" {
		var out []map[string]any
		if r.URL.Query().Get("all") == "1" {
			f.full++
		} else {
			f.polled++
			if !f.flag {
				json.NewEncoder(w).Encode(out)
				return
			}
		}
		for k := range f.items {
			e := map[string]any{"key": k}
			for mk, mv := range f.meta[k] {
				e[mk] = mv
			}
			out = append(out, e)
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/pending/"))
	raw, ok := f.items[key]
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	if r.Method == "DELETE" {
		delete(f.items, key)
		w.Write([]byte("deleted"))
		return
	}
	w.Write(raw)
}

func TestDrain(t *testing.T) {
	h, st, stored := setup(t)
	kv := &fakeKV{
		items: map[string][]byte{
			"pending:1792146928831:a": mail("<d1@x>", "", "first"),
			"pending:1792146999000:b": mail("<d2@x>", "", "second"),
		},
		meta: map[string]map[string]any{
			"pending:1792146928831:a": {"from": "alice@example.com", "to": "b.gmail@example.test", "forwarded": []string{"fwd@example.com"}},
			"pending:1792146999000:b": {"from": "alice@example.com", "to": "x@example.test"},
		},
	}
	srv := httptest.NewServer(kv)
	defer srv.Close()
	d := &Drainer{Handler: h, Worker: &outbound.Sender{WorkerURL: srv.URL, Secret: secret}}

	n, err := d.Drain(context.Background())
	if err != nil || n != 2 || len(kv.items) != 0 || len(*stored) != 2 {
		t.Fatalf("drained %d (%v), left %d, stored %v", n, err, len(kv.items), *stored)
	}
	m, _ := st.GetMessage(context.Background(), (*stored)[0])
	other, _ := st.GetMessage(context.Background(), (*stored)[1])
	if m.EnvelopeTo != "b.gmail@example.test" {
		m, other = other, m
	}
	if !m.ReceivedAt.Equal(time.UnixMilli(1792146928831)) || len(m.AutoForwarded) != 1 || m.DecodedTo != "b@gmail.com" {
		t.Errorf("drained message %+v", m)
	}
	if len(other.AutoForwarded) != 0 {
		t.Errorf("forwarded leaked: %v", other.AutoForwarded)
	}

	if kv.full != 1 || kv.polled != 0 {
		t.Fatalf("first drain should be a full list: full %d, polled %d", kv.full, kv.polled)
	}

	// Without the flag a poll finds nothing, and no second full list runs this soon.
	kv.items["pending:1792147000000:c"] = mail("<d1@x>", "", "first")
	kv.meta["pending:1792147000000:c"] = map[string]any{"from": "alice@example.com", "to": "b.gmail@example.test"}
	if n, _ := d.Drain(context.Background()); n != 0 || kv.full != 1 || kv.polled != 1 {
		t.Fatalf("unflagged poll: took %d, full %d, polled %d", n, kv.full, kv.polled)
	}

	// Flag set (a stash happened): already stored mail is just removed from KV.
	kv.flag = true
	if n, _ := d.Drain(context.Background()); n != 1 || len(*stored) != 2 || len(kv.items) != 0 {
		t.Fatalf("duplicate drain: %d, stored %v, left %d", n, *stored, len(kv.items))
	}
}
