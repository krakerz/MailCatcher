package web

import (
	"net/http"
	"sync"
	"time"
)

// Flashes carry a successful POST's result across its redirect (Post/Redirect/Get),
// so reloading the page never repeats the action. Kept in memory, not in a cookie or
// URL: they can hold one-time secrets (temporary passwords, API keys).
type flashes struct {
	mu sync.Mutex
	m  map[string]flashEntry
}

type flashEntry struct {
	v   any
	exp time.Time
}

const flashTTL = 5 * time.Minute

// redirectFlash stores v for the next GET of path in this session and redirects there.
func (s *Server) redirectFlash(w http.ResponseWriter, r *http.Request, path string, v any) {
	now := time.Now()
	s.flash.mu.Lock()
	if s.flash.m == nil {
		s.flash.m = map[string]flashEntry{}
	}
	for k, e := range s.flash.m {
		if now.After(e.exp) {
			delete(s.flash.m, k)
		}
	}
	s.flash.m[flashKey(r, path)] = flashEntry{v: v, exp: now.Add(flashTTL)}
	s.flash.mu.Unlock()
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// takeFlash returns (and forgets) the value flashed for this page, or T's zero value.
func takeFlash[T any](s *Server, r *http.Request) T {
	var zero T
	key := flashKey(r, r.URL.Path)
	s.flash.mu.Lock()
	e, ok := s.flash.m[key]
	delete(s.flash.m, key)
	s.flash.mu.Unlock()
	if !ok || time.Now().After(e.exp) {
		return zero
	}
	v, _ := e.v.(T)
	return v
}

func flashKey(r *http.Request, path string) string {
	return current(r).Session.TokenHash + " " + path
}
