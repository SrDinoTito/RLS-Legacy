package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User is a dashboard account (admin or viewer). PasswordHash is exported for
// internal use; API handlers must never serialize it.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	CreatedAt    int64
}

var ErrNotFound = errors.New("not found")

// ErrBootstrapComplete means the initial administrator already exists.
// CreateFirstAdmin uses a transaction so concurrent first-run requests cannot
// create multiple administrators.
var ErrBootstrapComplete = errors.New("bootstrap already completed")

// CountUsers returns the number of stored users (0 signals bootstrap mode).
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstAdmin atomically checks that the users table is empty and inserts
// the first administrator. The handler may do an earlier CountUsers check for
// friendly errors, but this transaction is the authoritative concurrency
// guard.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, passwordHash string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin bootstrap transaction: %w", err)
	}
	defer tx.Rollback()

	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users during bootstrap: %w", err)
	}
	if n > 0 {
		return 0, ErrBootstrapComplete
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, 'admin', ?)`,
		username, passwordHash, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrBootstrapComplete
		}
		return 0, fmt.Errorf("create first administrator: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read first administrator id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit bootstrap transaction: %w", err)
	}
	return id, nil
}

// CountAdmins returns the number of users with role='admin'.
func (s *Store) CountAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// CreateUser inserts a new user. It returns ErrDuplicate when the username
// already exists (checked up front, plus a defensive string match on the
// UNIQUE constraint error to tolerate concurrent insert races).
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (int64, error) {
	n, err := s.CountUsersByUsername(ctx, username)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrDuplicate
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, role, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return res.LastInsertId()
}

// CountUsersByUsername counts rows matching a username (dup pre-check).
func (s *Store) CountUsersByUsername(ctx context.Context, username string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n)
	return n, err
}

// GetUserByUsername looks a user up by exact username. ErrNotFound when absent.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`,
		username))
}

// GetUserByID looks a user up by primary key. ErrNotFound when absent.
func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE id = ?`,
		id))
}

// ListUsers returns all users ordered by creation time.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user by id. ErrNotFound when the id does not exist.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword replaces the stored bcrypt hash for the given user
// (admin resetting another user, or a user changing their own password).
// ErrNotFound when the id does not exist.
func (s *Store) UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("update user password %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return &u, nil
}
