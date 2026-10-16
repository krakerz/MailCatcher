package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrExists = errors.New("already exists")

type User struct {
	ID                 int64
	Username           string
	Email              string
	PasswordHash       string
	Role               string // "admin" | "qa"
	Disabled           bool
	MustChangePassword bool
	CreatedAt          time.Time
	LastLoginAt        time.Time // zero if never
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

type Session struct {
	TokenHash string
	UserID    int64
	CSRF      string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

const userCols = `id, username, email, password_hash, role, disabled, must_change_password, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u         User
		created   int64
		lastLogin sql.NullInt64
	)
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled, &u.MustChangePassword, &created, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.UnixMilli(created)
	if lastLogin.Valid {
		u.LastLoginAt = time.UnixMilli(lastLogin.Int64)
	}
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.CreatedAt = time.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, email, password_hash, role, disabled, must_change_password, created_at)
		VALUES (?,?,?,?,?,?,?)`, u.Username, u.Email, u.PasswordHash, u.Role, u.Disabled, u.MustChangePassword, u.CreatedAt.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrExists
		}
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

func (s *Store) GetUserByName(ctx context.Context, username string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", username))
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser saves the editable fields (email, role, disabled, password, must-change flag).
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email = ?, role = ?, disabled = ?, password_hash = ?, must_change_password = ?
		WHERE id = ?`, u.Email, u.Role, u.Disabled, u.PasswordHash, u.MustChangePassword, u.ID)
	return err
}

func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", time.Now().UnixMilli(), userID)
	return err
}

// CountActiveAdmins guards against disabling/demoting the last admin.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0").Scan(&n)
	return n, err
}

func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, csrf, user_agent, created_at, expires_at)
		VALUES (?,?,?,?,?,?)`, sess.TokenHash, sess.UserID, sess.CSRF, sess.UserAgent, sess.CreatedAt.UnixMilli(), sess.ExpiresAt.UnixMilli())
	return err
}

// GetSession returns a non-expired session.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (*Session, error) {
	var (
		sess             Session
		created, expires int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, user_id, csrf, user_agent, created_at, expires_at
		FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, time.Now().UnixMilli()).
		Scan(&sess.TokenHash, &sess.UserID, &sess.CSRF, &sess.UserAgent, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, sess.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// DeleteUserSessions logs a user out everywhere, except the session given (may be "").
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keepTokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token_hash != ?", userID, keepTokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", time.Now().UnixMilli())
	return err
}
