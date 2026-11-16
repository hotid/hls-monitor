package store

import (
	"context"
	"database/sql"
	"time"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// User is a web interface account.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
}

const userCols = "id, username, password_hash, role, disabled, created_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Disabled, &created); err != nil {
		return nil, notFound(err)
	}
	u.CreatedAt = fromMs(created)
	return &u, nil
}

// CreateUser adds an account. It returns ErrConflict when the name is taken.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string, now time.Time) (*User, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)",
		username, passwordHash, role, toMs(now))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetUser(ctx, id)
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
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

// UserUpdate lists the fields to change; nil fields are left alone.
type UserUpdate struct {
	Role         *string
	Disabled     *bool
	PasswordHash *string
}

// UpdateUser changes an account. It refuses, with ErrLastAdmin, a change
// that would leave no enabled administrator.
func (s *Store) UpdateUser(ctx context.Context, id int64, up UserUpdate) (*User, error) {
	err := s.Tx(ctx, func(tx *Tx) error {
		u, err := scanUser(tx.q.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
		if err != nil {
			return err
		}
		role, disabled, hash := u.Role, u.Disabled, u.PasswordHash
		if up.Role != nil {
			role = *up.Role
		}
		if up.Disabled != nil {
			disabled = *up.Disabled
		}
		if up.PasswordHash != nil {
			hash = *up.PasswordHash
		}
		if u.Role == RoleAdmin && !u.Disabled && (role != RoleAdmin || disabled) {
			if err := tx.requireOtherAdmin(ctx, id); err != nil {
				return err
			}
		}
		if _, err := tx.q.ExecContext(ctx, "UPDATE users SET role = ?, disabled = ?, password_hash = ? WHERE id = ?", role, disabled, hash, id); err != nil {
			return err
		}
		// Credentials or access changed: existing sessions must not outlive it.
		if up.PasswordHash != nil || disabled {
			_, err = tx.q.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// DeleteUser removes an account unless it is the last enabled administrator.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.Tx(ctx, func(tx *Tx) error {
		u, err := scanUser(tx.q.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && !u.Disabled {
			if err := tx.requireOtherAdmin(ctx, id); err != nil {
				return err
			}
		}
		_, err = tx.q.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
		return err
	})
}

// ErrLastAdmin is returned when a change would leave no enabled administrator.
var ErrLastAdmin = errorString("this is the last enabled administrator")

type errorString string

func (e errorString) Error() string { return string(e) }

func (tx *Tx) requireOtherAdmin(ctx context.Context, exceptID int64) error {
	var n int
	if err := tx.q.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id <> ?", exceptID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

// CreateSession stores the hash of a session token.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		tokenHash, userID, toMs(now), toMs(expires))
	return err
}

// SessionUser returns the enabled user owning a live session.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.password_hash, u.role, u.disabled, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`, tokenHash, toMs(now)))
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// APIToken is a read-only token for external systems.
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Store) CreateAPIToken(ctx context.Context, name string, tokenHash []byte, now time.Time) (*APIToken, error) {
	res, err := s.db.ExecContext(ctx, "INSERT INTO api_tokens (name, token_hash, created_at) VALUES (?, ?, ?)", name, tokenHash, toMs(now))
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &APIToken{ID: id, Name: name, CreatedAt: fromMs(toMs(now))}, nil
}

func (s *Store) ListAPITokens(ctx context.Context) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, created_at, last_used_at FROM api_tokens ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIToken{}
	for rows.Next() {
		var t APIToken
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = fromMs(created)
		t.LastUsedAt = fromMsPtr(used)
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ?", id))
}

// APITokenValid reports whether a token hash is known.
func (s *Store) APITokenValid(ctx context.Context, tokenHash []byte) (bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM api_tokens WHERE token_hash = ?", tokenHash).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// TouchAPIToken records when a token was last used.
func (s *Store) TouchAPIToken(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?", toMs(now), tokenHash)
	return err
}
