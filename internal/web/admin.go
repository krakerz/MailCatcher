package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

type usersData struct {
	Users   []*store.User
	Inboxes []*store.Inbox
	Access  map[int64][]int64 // user id → inbox ids; missing = all inboxes
	Notice  string            // e.g. a generated password, shown once
	Error   string
}

func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, d usersData) {
	var err error
	if d.Users, err = s.Store.ListUsers(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	if d.Inboxes, err = s.Store.ListInboxes(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	d.Access = map[int64][]int64{}
	for _, u := range d.Users {
		ids, err := s.Store.UserInboxIDs(r.Context(), u.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if len(ids) > 0 {
			d.Access[u.ID] = ids
		}
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
	if err := s.Store.SetUserInboxes(r.Context(), u.ID, formIDs(r, "inbox")); err != nil {
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
	case "inboxes":
		if err := s.Store.SetUserInboxes(r.Context(), u.ID, formIDs(r, "inbox")); err != nil {
			s.fail(w, err)
			return
		}
		s.renderUsers(w, r, usersData{Notice: "Saved inbox access for " + u.Username + "."})
		return
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

// formIDs reads repeated numeric form values (checkboxes).
func formIDs(r *http.Request, name string) []int64 {
	var out []int64
	for _, v := range r.PostForm[name] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
