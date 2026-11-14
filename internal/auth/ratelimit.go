package auth

import (
	"strings"
	"sync"
	"time"
)

// Limiter counts failed logins per key (IP and username separately) in a sliding window.
type Limiter struct {
	Max    int
	Window time.Duration

	mu    sync.Mutex
	fails map[string][]time.Time
	now   func() time.Time
	sweep time.Time // last full sweep
}

// maxKeys bounds the map: failed usernames are attacker-chosen keys.
const maxKeys = 10000

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{Max: max, Window: window, fails: map[string][]time.Time{}, now: time.Now}
}

func (l *Limiter) prune(key string) []time.Time {
	cut := l.now().Add(-l.Window)
	ts := l.fails[key]
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = ts
	}
	return ts
}

// Blocked reports whether any of keys has reached the failure limit.
func (l *Limiter) Blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if len(l.prune(k)) >= l.Max {
			return true
		}
	}
	return false
}

// sweepExpired drops every key whose failures have all left the window; it runs
// at most once per minute, or at once when the map is full.
func (l *Limiter) sweepExpired(force bool) {
	now := l.now()
	if !force && now.Sub(l.sweep) < time.Minute {
		return
	}
	l.sweep = now
	for k := range l.fails {
		l.prune(k)
	}
}

func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpired(len(l.fails) >= maxKeys)
	for _, k := range keys {
		// Still full after a sweep: refuse new username keys (the IP key is what
		// throttles a flood anyway) but keep tracking IPs and known keys.
		if _, known := l.fails[k]; !known && len(l.fails) >= maxKeys && !strings.HasPrefix(k, "ip:") {
			continue
		}
		l.fails[k] = append(l.prune(k), l.now())
	}
}

func (l *Limiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fails, k)
	}
}
