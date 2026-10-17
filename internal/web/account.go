package web

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

// Bootstrap creates the first admin when there are no users. With an empty
// password a random one is generated, logged once, and must be changed on first login.
func Bootstrap(ctx context.Context, st *store.Store, password string) error {
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	generated := password == ""
	if generated {
		password = auth.Token(12)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.CreateUser(ctx, &store.User{Username: "admin", PasswordHash: hash, Role: "admin", MustChangePassword: generated}); err != nil {
		return err
	}
	if generated {
		slog.Warn("created first user — change this password after logging in", "username", "admin", "password", password)
	} else {
		slog.Info("created first user from MC_ADMIN_PASSWORD", "username", "admin")
	}
	return nil
}

type loginData struct {
	Next, Username, Error string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.session(r); err == nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", "Log in", loginData{Next: r.URL.Query().Get("next")})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	// Login has no session yet, so no CSRF token: reject cross-site posts by Origin instead.
	if o := r.Header.Get("Origin"); o != "" && !strings.HasSuffix(o, "://"+r.Host) {
		http.Error(w, "cross-site login refused", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")
	ipKey, userKey := "ip:"+clientIP(r), "user:"+strings.ToLower(username)

	fail := func(msg string, code int) {
		w.WriteHeader(code)
		s.render(w, r, "login", "Log in", loginData{Next: next, Username: username, Error: msg})
	}
	if s.Limiter.Blocked(ipKey, userKey) {
		fail("Too many failed attempts. Try again in a few minutes.", http.StatusTooManyRequests)
		return
	}
	u, err := s.Store.GetUserByName(r.Context(), username)
	ok := false
	if err == nil && !u.Disabled {
		ok, err = auth.CheckPassword(u.PasswordHash, password)
	} else {
		// Same work for unknown users, so timing does not reveal valid usernames.
		auth.CheckPassword(dummyHash, password)
	}
	if !ok {
		if err != nil && err != store.ErrNotFound {
			slog.Error("login", "err", err)
		}
		s.Limiter.Fail(ipKey, userKey)
		fail("Wrong username or password.", http.StatusUnauthorized)
		return
	}
	s.Limiter.Reset(userKey)

	token := auth.Token(32)
	sess := &store.Session{
		TokenHash: auth.HashToken(token),
		UserID:    u.ID,
		CSRF:      auth.Token(24),
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	if err := s.Store.CreateSession(r.Context(), sess); err != nil {
		s.fail(w, err)
		return
	}
	s.Store.TouchLogin(r.Context(), u.ID)
	s.Store.DeleteExpiredSessions(r.Context())
	s.setSessionCookie(w, token, sess.ExpiresAt)
	slog.Info("login", "user", u.Username, "ip", clientIP(r))
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

var dummyHash, _ = auth.HashPassword("not-a-real-password")

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Store.DeleteSession(r.Context(), current(r).Session.TokenHash)
	s.setSessionCookie(w, "", time.Unix(0, 0))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type passwordData struct {
	Forced       bool
	Error, Saved string
}

func (s *Server) passwordPage(w http.ResponseWriter, r *http.Request) {
	d := takeFlash[passwordData](s, r)
	d.Forced = current(r).User.MustChangePassword
	s.render(w, r, "password", "Change password", d)
}

func (s *Server) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	c := current(r)
	d := passwordData{Forced: c.User.MustChangePassword}
	cur, pw, confirm := r.PostFormValue("current"), r.PostFormValue("new"), r.PostFormValue("confirm")

	if ok, _ := auth.CheckPassword(c.User.PasswordHash, cur); !ok {
		d.Error = "Current password is wrong."
	} else if err := auth.ValidPassword(pw); err != nil {
		d.Error = err.Error()
	} else if pw != confirm {
		d.Error = "The new passwords do not match."
	} else if pw == cur {
		d.Error = "Choose a password different from the current one."
	}
	if d.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "password", "Change password", d)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	c.User.PasswordHash, c.User.MustChangePassword = hash, false
	if err := s.Store.UpdateUser(r.Context(), c.User); err != nil {
		s.fail(w, err)
		return
	}
	// Other devices are logged out; this one stays.
	s.Store.DeleteUserSessions(r.Context(), c.User.ID, c.Session.TokenHash)
	if d.Forced {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.redirectFlash(w, r, "/account/password", passwordData{Saved: "Password changed. Other sessions were logged out."})
}
