// Package updates asks GitHub for the latest published release, so the UI can tell an admin a newer version exists.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultURL = "https://api.github.com/repos/krakerz/MailCatcher/releases/latest"

type Checker struct {
	URL      string
	Current  string
	Interval time.Duration
	File     string // state file; empty = not persisted
	Client   *http.Client
	Now      func() time.Time

	mu         sync.Mutex
	st         state
	lastLogged string
}

type state struct {
	Latest      string `json:"latest"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
	LastCheckAt int64  `json:"last_check_at"`
	Error       string `json:"error"`
	ETag        string `json:"etag"`
}

type View struct {
	Latest      string
	URL         string
	PublishedAt string
	LastCheck   time.Time
	Err         string
	current     string
}

// Available: a release is known and newer than the running version.
func (v View) Available() bool { return v.Latest != "" && Newer(v.Latest, v.current) }

// New loads the saved result so a restart keeps it.
func New(current, file string, interval time.Duration) *Checker {
	c := &Checker{
		URL:      DefaultURL,
		Current:  current,
		Interval: interval,
		File:     file,
		Client:   &http.Client{Timeout: 10 * time.Second},
		Now:      time.Now,
	}
	c.load()
	return c
}

// Run checks soon after start (not at once: the network may still be coming up), then every interval.
func (c *Checker) Run(ctx context.Context) {
	wait := min(60*time.Second, c.Interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c.Check(ctx)
		wait = c.Interval
	}
}

func (c *Checker) Snapshot() View {
	c.mu.Lock()
	defer c.mu.Unlock()
	return View{
		Latest:      c.st.Latest,
		URL:         c.st.URL,
		PublishedAt: c.st.PublishedAt,
		LastCheck:   time.Unix(c.st.LastCheckAt, 0),
		Err:         c.st.Error,
		current:     c.Current,
	}
}

// Check performs one request and records the outcome.
func (c *Checker) Check(ctx context.Context) {
	c.mu.Lock()
	etag := c.st.ETag
	if c.st.Latest == "" {
		etag = "" // nothing stored to keep on a 304
	}
	c.mu.Unlock()

	next, err := c.fetch(ctx, etag)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.LastCheckAt = c.Now().Unix()
	c.st.Error = ""
	if err != nil {
		c.st.Error = trim(err.Error(), 200)
		if c.st.Error != c.lastLogged {
			slog.Warn("update check failed", "err", c.st.Error)
		}
	} else {
		if next != nil {
			c.st.Latest, c.st.URL, c.st.PublishedAt, c.st.ETag = next.Latest, next.URL, next.PublishedAt, next.ETag
		}
		slog.Info("update check", "latest", c.st.Latest, "current", c.Current)
	}
	c.lastLogged = c.st.Error
	c.save()
}

type release struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

// fetch returns nil, nil on 304 (keep the stored result).
func (c *Checker) fetch(ctx context.Context, etag string) (*state, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mailcatcher/"+c.Current)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotModified:
		return nil, nil
	case res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub refused the request (HTTP %d, rate limit?)", res.StatusCode)
	case res.StatusCode == http.StatusNotFound:
		return nil, errors.New("no published release found (HTTP 404)")
	case res.StatusCode < 200 || res.StatusCode > 299:
		return nil, fmt.Errorf("GitHub answered HTTP %d", res.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("unreadable GitHub answer: %w", err)
	}
	latest := strings.TrimLeft(strings.TrimSpace(rel.TagName), "vV")
	if _, ok := parse(latest); !ok {
		return nil, fmt.Errorf("unrecognised release tag %q", trim(rel.TagName, 40))
	}
	return &state{Latest: latest, URL: rel.HTMLURL, PublishedAt: rel.PublishedAt, ETag: res.Header.Get("ETag")}, nil
}

func (c *Checker) load() {
	if c.File == "" {
		return
	}
	b, err := os.ReadFile(c.File)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("update state unreadable, ignoring", "err", err)
		}
		return
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		slog.Warn("update state unreadable, ignoring", "err", err)
		return
	}
	c.st = st
}

// save runs with c.mu held; tmp + rename so a crash never leaves half a file.
func (c *Checker) save() {
	if c.File == "" {
		return
	}
	b, err := json.Marshal(c.st)
	if err == nil {
		tmp := c.File + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, c.File)
		}
		if err != nil {
			os.Remove(tmp)
		}
	}
	if err != nil {
		slog.Warn("update state not saved", "err", err)
	}
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var versionRe = regexp.MustCompile(`(?i)^\s*v?(\d+(?:\.\d+)*)(.*)$`)

type key struct {
	nums    []int
	release bool // a plain release beats any suffixed one (rc, beta) of the same numbers
	suffix  string
}

func parse(v string) (key, bool) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return key{}, false
	}
	var nums []int
	for _, p := range strings.Split(m[1], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return key{}, false
		}
		nums = append(nums, n)
	}
	for len(nums) > 1 && nums[len(nums)-1] == 0 {
		nums = nums[:len(nums)-1]
	}
	suffix := strings.ToLower(strings.TrimSpace(m[2]))
	return key{nums, suffix == "", suffix}, true
}

func (a key) cmp(b key) int {
	for i := 0; i < len(a.nums) || i < len(b.nums); i++ {
		switch x, y := at(a.nums, i), at(b.nums, i); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	if a.release != b.release {
		if a.release {
			return 1
		}
		return -1
	}
	return strings.Compare(a.suffix, b.suffix)
}

// at pads with zeros so 1.2 and 1.2.1 compare part by part.
func at(n []int, i int) int {
	if i < len(n) {
		return n[i]
	}
	return 0
}

// Newer reports whether latest is a higher version than current; unparseable input is never newer.
func Newer(latest, current string) bool {
	a, ok1 := parse(latest)
	b, ok2 := parse(current)
	return ok1 && ok2 && a.cmp(b) > 0
}
