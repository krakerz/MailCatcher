// Package web serves the browser UI: login, inbox, message view and admin pages.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/events"
	"mailcatcher/internal/store"
)

//go:embed templates static
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

	pages map[string]*template.Template
}

func New(st *store.Store, b *events.Broker, cookieSecure bool) (*Server, error) {
	s := &Server{
		Store:        st,
		Broker:       b,
		Limiter:      auth.NewLimiter(10, 15*time.Minute),
		CookieSecure: cookieSecure,
		pages:        map[string]*template.Template{},
	}
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if base == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html", n)
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
	mux.Handle("GET /events", s.user(s.events))
	mux.Handle("GET /m/{id}", s.user(s.messagePage))
	mux.Handle("GET /m/{id}/body", s.user(s.messageBody))
	mux.Handle("GET /m/{id}/raw", s.user(s.messageRaw))
	mux.Handle("GET /m/{id}/att/{aid}", s.user(s.attachment))

	mux.Handle("GET /admin/users", s.admin(s.usersPage))
	mux.Handle("POST /admin/users", s.admin(s.userCreate))
	mux.Handle("POST /admin/users/{id}", s.admin(s.userUpdate))
	mux.Handle("GET /admin/inboxes", s.admin(s.inboxesPage))
	mux.Handle("POST /admin/inboxes/{id}/apikey", s.admin(s.inboxAPIKey))
}

type ctxKey struct{}

type reqCtx struct {
	User    *store.User
	Session *store.Session
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
	return &reqCtx{User: u, Session: sess}, nil
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
		// Not no-referrer: Chrome then sends "Origin: null" on form posts, breaking the login Origin check.
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

type page struct {
	Title string
	User  *store.User
	CSRF  string
	Data  any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	p := page{Title: title, Data: data}
	if c := current(r); c != nil {
		p.User, p.CSRF = c.User, c.Session.CSRF
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
