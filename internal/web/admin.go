package web

import (
	"net/http"
	"regexp"
	"strings"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

type usersData struct {
	Users  []*store.User
	Notice string // e.g. a generated password, shown once
	Error  string
}

func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, d usersData) {
	var err error
	if d.Users, err = s.Store.ListUsers(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	if d.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "users", "Users", d)
}

func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, usersData{})
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{2,32}$`)

func validRole(role string) bool { return role == "admin" || role == "user" }

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	role := r.PostFormValue("role")
	if !usernameRe.MatchString(username) {
		s.renderUsers(w, r, usersData{Error: "Username: 2–32 characters, letters, digits, dot, dash or underscore."})
		return
	}
	if !validRole(role) {
		s.renderUsers(w, r, usersData{Error: "Unknown role."})
		return
	}
	pw := auth.Token(12)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.fail(w, err)
		return
	}
	u := &store.User{Username: username, Email: strings.TrimSpace(r.PostFormValue("email")), PasswordHash: hash, Role: role, MustChangePassword: true}
	if err := s.Store.CreateUser(r.Context(), u); err != nil {
		if err == store.ErrExists {
			s.renderUsers(w, r, usersData{Error: "That username is taken."})
			return
		}
		s.fail(w, err)
		return
	}
	s.renderUsers(w, r, usersData{Notice: "Created " + username + ". Temporary password (shown once, must be changed at first login): " + pw})
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	me := current(r).User
	action := r.PostFormValue("action")

	// Never leave the system without an active admin, and never lock yourself out.
	losesAdmin := u.IsAdmin() && !u.Disabled &&
		(action == "disable" || (action == "role" && r.PostFormValue("role") != "admin"))
	if losesAdmin {
		if u.ID == me.ID {
			s.renderUsers(w, r, usersData{Error: "You cannot disable or demote yourself."})
			return
		}
		if n, err := s.Store.CountActiveAdmins(r.Context()); err != nil || n <= 1 {
			s.renderUsers(w, r, usersData{Error: "At least one active admin must remain."})
			return
		}
	}

	notice := ""
	switch action {
	case "disable":
		u.Disabled = true
	case "enable":
		u.Disabled = false
	case "role":
		role := r.PostFormValue("role")
		if !validRole(role) {
			s.renderUsers(w, r, usersData{Error: "Unknown role."})
			return
		}
		u.Role = role
	case "reset":
		pw := auth.Token(12)
		if u.PasswordHash, err = auth.HashPassword(pw); err != nil {
			s.fail(w, err)
			return
		}
		u.MustChangePassword = true
		notice = "New temporary password for " + u.Username + " (shown once): " + pw
	default:
		s.renderUsers(w, r, usersData{Error: "Unknown action."})
		return
	}
	if err := s.Store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err)
		return
	}
	if action == "disable" || action == "reset" {
		s.Store.DeleteUserSessions(r.Context(), u.ID, "")
	}
	s.renderUsers(w, r, usersData{Notice: notice})
}
