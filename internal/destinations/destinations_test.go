package destinations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcatcher/internal/cloudflare"
	"mailcatcher/internal/store"
)

// fakeCF imitates the Email Routing addresses API, including the spike's error codes.
type fakeCF struct {
	mu      sync.Mutex
	addrs   map[string]map[string]any // email -> address object
	lists   int
	created map[string]time.Time
}

func (f *fakeCF) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
			return
		}
		ok := func(result any, info any) {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "result_info": info})
		}
		fail := func(code int, msg string) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": msg}}})
		}
		path := strings.TrimPrefix(r.URL.Path, "/accounts/acc/email/routing")
		switch {
		case r.Method == "GET" && path == "/addresses":
			f.lists++
			var out []any
			for _, a := range f.addrs {
				out = append(out, a)
			}
			ok(out, map[string]int{"page": 1, "total_pages": 1})
		case r.Method == "POST" && path == "/addresses":
			var body struct{ Email string }
			json.NewDecoder(r.Body).Decode(&body)
			if _, exists := f.addrs[body.Email]; exists {
				fail(cloudflare.CodeSentTooRecently, "Verification email has been sent too recently")
				return
			}
			a := map[string]any{"id": "id-" + body.Email, "email": body.Email, "verified": nil, "created": time.Now().Format(time.RFC3339)}
			f.addrs[body.Email] = a
			f.created[body.Email] = time.Now()
			ok(a, nil)
		case r.Method == "DELETE" && strings.HasPrefix(path, "/addresses/"):
			email := strings.TrimPrefix(path, "/addresses/id-")
			if time.Since(f.created[email]) < time.Minute {
				fail(cloudflare.CodeCreatedTooRecently, "Destination address has been created too recently")
				return
			}
			delete(f.addrs, email)
			ok(nil, nil)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	})
}

func (f *fakeCF) verify(email string) {
	f.mu.Lock()
	f.addrs[email]["verified"] = time.Now().Format(time.RFC3339)
	f.mu.Unlock()
}

func setup(t *testing.T) (*Service, *fakeCF) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakeCF{addrs: map[string]map[string]any{}, created: map[string]time.Time{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return &Service{Store: st, CF: &cloudflare.Client{Token: "tok", AccountID: "acc", BaseURL: srv.URL}}, f
}

func TestAddVerifyFlow(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	d, err := s.Add(ctx, "QA <Tester@Example.com>")
	if err != nil || d.Email != "tester@example.com" || d.Verified() {
		t.Fatalf("add: %+v %v", d, err)
	}
	if _, err := s.Add(ctx, "tester@example.com"); err != ErrTooSoon {
		t.Fatalf("resend: %v", err)
	}
	if ok, _ := s.Verified(ctx, "tester@example.com"); ok {
		t.Fatal("pending address counted as verified")
	}

	f.verify("tester@example.com")
	s.lastSync = time.Time{} // the owner clicked the link; next check may resync
	if ok, _ := s.Verified(ctx, "tester@example.com"); !ok {
		t.Fatal("verified address not picked up by resync")
	}
	lists := f.lists
	s.Verified(ctx, "tester@example.com")
	if f.lists != lists {
		t.Error("verified address caused another sync")
	}

	if err := s.Remove(ctx, "tester@example.com"); err != ErrTooSoon {
		t.Fatalf("remove right after create: %v", err)
	}
}

func TestResyncIsRateLimited(t *testing.T) {
	s, f := setup(t)
	ctx := context.Background()
	s.Verified(ctx, "a@example.com")
	s.Verified(ctx, "b@example.com")
	if f.lists != 1 {
		t.Fatalf("unknown addresses synced %d times, want 1", f.lists)
	}
}

func TestDisabledLetsCloudflareDecide(t *testing.T) {
	s := &Service{CF: &cloudflare.Client{}}
	if ok, err := s.Verified(context.Background(), "x@example.com"); !ok || err != nil {
		t.Fatalf("disabled: %v %v", ok, err)
	}
	if _, err := s.Add(context.Background(), "x@example.com"); err != ErrDisabled {
		t.Fatalf("add while disabled: %v", err)
	}
}
