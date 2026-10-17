// Package web serves the browser UI: login, inbox, message view and admin pages.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/destinations"
	"mailcatcher/internal/events"
	"mailcatcher/internal/outbound"
	"mailcatcher/internal/rules"
	"mailcatcher/internal/store"
)

//go:embed all:templates static
var assets embed.FS

const (
	cookieName = "mc_session"
	sessionTTL = 30 * 24 * time.Hour
)

type Server struct {
	Store        *store.Store
	Broker       *events.Broker
	Limiter      *auth.Limiter
	CookieSecure bool
	Domain       string // main catch-all domain
	Mailer       *outbound.Mailer
	Dest         *destinations.Service
	Rules        *rules.Pusher
	// Drain collects mail waiting in the Worker's KV (the inbox Refresh button).
	Drain func(ctx context.Context) (int, error)

	pages map[string]*template.Template
	flash flashes
}

func New(st *store.Store, b *events.Broker, domain string, cookieSecure bool) (*Server, error) {
	s := &Server{
		Store:        st,
		Domain:       domain,
		Broker:       b,
		Limiter:      auth.NewLimiter(10, 15*time.Minute),
		CookieSecure: cookieSecure,
		pages:        map[string]*template.Template{},
	}
	assetVersion = staticHash()
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	partials, err := fs.Glob(assets, "templates/_*.html")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if base == "layout" || strings.HasPrefix(base, "_") {
			continue
		}
		files := append([]string{"templates/layout.html", n}, partials...)
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, files...)
		if err != nil {
			return nil, err
		}
		s.pages[base] = t
	}
	return s, nil
}

// Register mounts all UI routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.Handle("POST /logout", s.user(s.logout))
	mux.Handle("GET /account/password", s.user(s.passwordPage))
	mux.Handle("POST /account/password", s.user(s.passwordSubmit))

	mux.Handle("GET /{$}", s.user(s.home))
	mux.Handle("GET /inbox/{id}", s.user(s.inboxPage))
	mux.Handle("GET /inbox/{id}/rows", s.user(s.inboxRows))
	mux.Handle("POST /inbox/{id}/read", s.user(s.inboxMarkRead))
	mux.Handle("POST /refresh", s.user(s.refresh))
	mux.Handle("POST /m/{id}/unread", s.user(s.messageUnread))
	mux.Handle("POST /messages/bulk", s.user(s.messagesBulk))
	mux.Handle("GET /events", s.user(s.events))
	mux.Handle("GET /m/{id}", s.user(s.messagePage))
	mux.Handle("GET /m/{id}/body", s.user(s.messageBody))
	mux.Handle("GET /m/{id}/raw", s.user(s.messageRaw))
	mux.Handle("GET /m/{id}/att/{aid}", s.user(s.attachment))
	mux.Handle("GET /m/{id}/reply", s.user(s.replyPage))
	mux.Handle("POST /m/{id}/reply", s.user(s.replySubmit))
	mux.Handle("GET /m/{id}/forward", s.user(s.forwardPage))
	mux.Handle("POST /m/{id}/forward", s.user(s.forwardSubmit))
	mux.Handle("GET /compose", s.user(s.composePage))
	mux.Handle("POST /compose", s.user(s.composeSubmit))
	mux.Handle("GET /compose/from", s.user(s.composeFrom))
	mux.Handle("GET /sent", s.user(s.sentPage))
	mux.Handle("GET /destinations", s.user(s.destinationsPage))
	mux.Handle("POST /destinations", s.admin(s.destinationAdd))
	mux.Handle("POST /destinations/sync", s.user(s.destinationSync))
	mux.Handle("POST /destinations/remove", s.admin(s.destinationRemove))

	mux.Handle("GET /admin/users", s.admin(s.usersPage))
	mux.Handle("POST /admin/users", s.admin(s.userCreate))
	mux.Handle("POST /admin/users/{id}", s.admin(s.userUpdate))
	mux.Handle("GET /admin/inboxes", s.admin(s.inboxesPage))
	mux.Handle("POST /admin/inboxes", s.admin(s.inboxCreate))
	mux.Handle("POST /admin/inboxes/reroute", s.admin(s.inboxReroute))
	mux.Handle("GET /admin/inboxes/{id}", s.admin(s.inboxEditPage))
	mux.Handle("POST /admin/inboxes/{id}", s.admin(s.inboxUpdate))
	mux.Handle("POST /admin/inboxes/{id}/apikey", s.admin(s.inboxAPIKey))
	mux.Handle("GET /admin/format-preview", s.admin(s.formatPreview))
	mux.Handle("GET /rules", s.user(s.rulesPage))
	mux.Handle("POST /rules", s.user(s.ruleCreate))
	mux.Handle("POST /rules/push", s.user(s.rulesPush))
	mux.Handle("POST /rules/{id}", s.user(s.ruleUpdate))
}

type ctxKey struct{}

type reqCtx struct {
	User    *store.User
	Session *store.Session
	// Allowed lists the inboxes a restricted user may open; nil = all (admins always).
	Allowed []int64
}

// canManageRule: rules for "any inbox" (0) need unrestricted access.
func (c *reqCtx) canManageRule(inboxID int64) bool {
	if inboxID == 0 {
		return c.Allowed == nil
	}
	return c.canSee(inboxID)
}

func (c *reqCtx) canSee(inboxID int64) bool {
	return c.Allowed == nil || slices.Contains(c.Allowed, inboxID)
}

// visible filters inboxes down to the ones the user may open.
func (c *reqCtx) visible(list []*store.Inbox) []*store.Inbox {
	if c.Allowed == nil {
		return list
	}
	var out []*store.Inbox
	for _, in := range list {
		if c.canSee(in.ID) {
			out = append(out, in)
		}
	}
	return out
}

func current(r *http.Request) *reqCtx {
	c, _ := r.Context().Value(ctxKey{}).(*reqCtx)
	return c
}

// user requires a logged-in, enabled user, enforces a pending password change
// and checks the CSRF token on every non-GET request.
func (s *Server) user(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.session(r)
		if err != nil {
			if r.Method == http.MethodGet && r.URL.Path != "/events" {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			// Uploads included; a little above the attachment cap for the other fields.
			r.Body = http.MaxBytesReader(w, r.Body, outbound.MaxUpload+(2<<20))
			tok := r.PostFormValue("csrf")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(c.Session.CSRF)) != 1 {
				http.Error(w, "invalid form token, reload the page", http.StatusForbidden)
				return
			}
		}
		if c.User.MustChangePassword && r.URL.Path != "/account/password" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return s.user(func(w http.ResponseWriter, r *http.Request) {
		if !current(r).User.IsAdmin() {
			http.Error(w, "admins only", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

func (s *Server) session(r *http.Request) (*reqCtx, error) {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		return nil, err
	}
	sess, err := s.Store.GetSession(r.Context(), auth.HashToken(ck.Value))
	if err != nil {
		return nil, err
	}
	u, err := s.Store.GetUser(r.Context(), sess.UserID)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, errors.New("user disabled")
	}
	c := &reqCtx{User: u, Session: sess}
	if !u.IsAdmin() {
		ids, err := s.Store.UserInboxIDs(r.Context(), u.ID)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			c.Allowed = ids
		}
	}
	return c, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clientIP prefers Cloudflare's header: the app is only reachable through the tunnel.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SecurityHeaders wraps the whole app; the message body handler sets its own stricter CSP.
func SecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		// no-transform stops Cloudflare from injecting its analytics beacon and rewriting
		// email addresses (Email Obfuscation) in pages; mail is never cached anywhere.
		if strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Query().Get("v") == assetVersion {
			// Versioned URL (asset helper): content can never change under it.
			hd.Set("Cache-Control", "public, max-age=31536000, immutable, no-transform")
		} else if strings.HasPrefix(r.URL.Path, "/static/") {
			hd.Set("Cache-Control", "public, max-age=300, no-transform")
		} else if noStore(r.URL.Path) {
			hd.Set("Cache-Control", "private, no-store, no-transform")
		} else {
			// Pages may stay in this browser (instant Back) but are re-checked before reuse.
			hd.Set("Cache-Control", "private, no-cache, no-transform")
		}
		// Not no-referrer: Chrome then sends "Origin: null" on form posts, breaking the login Origin check.
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

var attachmentPath = regexp.MustCompile(`^/m/\d+/(att|raw)(/|$)`)

// noStore: attachments and message sources never land in a browser cache; the API is
// for scripts, which have no use for one.
func noStore(path string) bool {
	return attachmentPath.MatchString(path) || strings.HasPrefix(path, "/api/")
}

type page struct {
	Title     string
	User      *store.User
	CSRF      string
	Data      any
	Nav       []*store.Inbox // sidebar
	NavUnread map[int64]int  // unread messages per sidebar inbox
	Path      string         // current path, for the active sidebar entry
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	p := page{Title: title, Data: data, Path: r.URL.Path}
	if d, ok := data.(sentData); ok && d.InboxID != 0 {
		p.Path = "/inbox/" + strconv.FormatInt(d.InboxID, 10) // a Sent list highlights its inbox
	}
	if c := current(r); c != nil {
		p.User, p.CSRF = c.User, c.Session.CSRF
		all, _ := s.Store.ListInboxes(r.Context())
		p.Nav = c.visible(all)
		p.NavUnread, _ = s.Store.UnreadCounts(r.Context(), c.User)
	}
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown page "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, p); err != nil {
		slog.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Error("web", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// safeNext only allows local paths as a post-login redirect target.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// assetVersion busts browser caches of /static/* whenever the bundled files change.
var assetVersion string

func staticHash() string {
	h := sha256.New()
	fs.WalkDir(assets, "static", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := assets.ReadFile(path)
			h.Write([]byte(path))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}
