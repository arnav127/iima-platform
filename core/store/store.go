// Package store is core's database: users, sessions, pending logins and token signing keys.
// SQLite for phase 1 (one core instance); the plan is to move to Postgres in phase 7.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go, so it builds on Windows without a C compiler
)

// ErrNotFound is returned when a session, login state or user does not exist (or has expired).
var ErrNotFound = errors.New("store: not found")

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id           TEXT PRIMARY KEY,
	provider     TEXT NOT NULL,
	provider_sub TEXT NOT NULL,
	email        TEXT NOT NULL,
	name         TEXT NOT NULL DEFAULT '',
	picture      TEXT NOT NULL DEFAULT '',
	member       INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	UNIQUE (provider, provider_sub)
);
CREATE INDEX IF NOT EXISTS users_email ON users (email);

-- token_hash is sha256 of the cookie value, so a copy of the database cannot be used to sign in.
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	sid        TEXT NOT NULL UNIQUE,
	user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS login_states (
	state      TEXT PRIMARY KEY,
	nonce      TEXT NOT NULL,
	verifier   TEXT NOT NULL,
	rd         TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS signing_keys (
	kid         TEXT PRIMARY KEY,
	private_key BLOB NOT NULL, -- PKCS#8 DER
	created_at  INTEGER NOT NULL
);

-- Roles and admins are keyed by email, so they can be granted before someone first signs in.
CREATE TABLE IF NOT EXISTS super_admins (
	email      TEXT PRIMARY KEY,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS app_admins (
	app_id     TEXT NOT NULL,
	email      TEXT NOT NULL,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL,
	PRIMARY KEY (app_id, email)
);

CREATE TABLE IF NOT EXISTS role_grants (
	app_id     TEXT NOT NULL,
	email      TEXT NOT NULL,
	role       TEXT NOT NULL,
	granted_by TEXT NOT NULL,
	granted_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, email, role)
);
CREATE INDEX IF NOT EXISTS role_grants_app ON role_grants (app_id, email);

-- Super-admin corrections to the attributes derived from an email. Empty means "derive".
CREATE TABLE IF NOT EXISTS attribute_overrides (
	email       TEXT PRIMARY KEY,
	affiliation TEXT NOT NULL DEFAULT '',
	programme   TEXT NOT NULL DEFAULT '',
	batch       INTEGER NOT NULL DEFAULT 0,
	note        TEXT NOT NULL DEFAULT '',
	updated_by  TEXT NOT NULL,
	updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	actor   TEXT NOT NULL,
	action  TEXT NOT NULL,
	app_id  TEXT NOT NULL DEFAULT '',
	target  TEXT NOT NULL DEFAULT '',
	detail  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_log_app ON audit_log (app_id, at);
`

// Open opens (and creates if needed) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// RandomID returns prefix plus n random bytes, base64url encoded.
func RandomID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// User is a person as core knows them. Attributes and roles arrive in phase 3.
type User struct {
	ID       string
	Provider string
	Email    string
	Name     string
	Picture  string
	Member   bool
}

// UpsertUser creates the user on first sign-in, or refreshes name, email, picture and membership.
func (s *Store) UpsertUser(ctx context.Context, provider, providerSub string, u User) (*User, error) {
	now := time.Now().Unix()
	id := RandomID("u_", 12)
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO users (id, provider, provider_sub, email, name, picture, member, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, provider_sub) DO UPDATE SET
			email = excluded.email, name = excluded.name, picture = excluded.picture,
			member = excluded.member, updated_at = excluded.updated_at
		RETURNING id`,
		id, provider, providerSub, u.Email, u.Name, u.Picture, u.Member, now, now).Scan(&id)
	if err != nil {
		return nil, err
	}
	u.ID, u.Provider = id, provider
	return &u, nil
}

// Session is a signed-in browser.
type Session struct {
	SID       string // public id, goes into tokens as sid
	User      User
	ExpiresAt time.Time
}

// CreateSession stores a new session and returns the cookie value. Only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (token string, sid string, err error) {
	token = RandomID("", 32)
	sid = RandomID("s_", 12)
	now := time.Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, sid, user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), sid, userID, now.Unix(), now.Add(ttl).Unix())
	return token, sid, err
}

// Session looks up an unexpired session by its cookie value.
func (s *Store) Session(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var exp int64
	err := s.db.QueryRowContext(ctx, `
		SELECT s.sid, s.expires_at, u.id, u.provider, u.email, u.name, u.picture, u.member
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), time.Now().Unix()).
		Scan(&sess.SID, &exp, &sess.User.ID, &sess.User.Provider, &sess.User.Email, &sess.User.Name, &sess.User.Picture, &sess.User.Member)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt = time.Unix(exp, 0)
	return &sess, nil
}

// ExtendSession moves an unexpired session's expiry to now + ttl.
func (s *Store) ExtendSession(ctx context.Context, token string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ? AND expires_at > ?`,
		time.Now().Add(ttl).Unix(), hashToken(token), time.Now().Unix())
	return err
}

// DeleteSession signs a browser out. Deleting a session that does not exist is not an error.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// DeleteExpired removes old sessions and abandoned logins. Called periodically.
func (s *Store) DeleteExpired(ctx context.Context, loginTTL time.Duration) error {
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_states WHERE created_at <= ?`, now.Add(-loginTTL).Unix())
	return err
}

// LoginState is a sign-in in progress: what we sent to Google and where to go afterwards.
type LoginState struct {
	State    string
	Nonce    string
	Verifier string // PKCE code verifier
	RD       string
}

func (s *Store) CreateLoginState(ctx context.Context, ls LoginState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO login_states (state, nonce, verifier, rd, created_at) VALUES (?, ?, ?, ?, ?)`,
		ls.State, ls.Nonce, ls.Verifier, ls.RD, time.Now().Unix())
	return err
}

// TakeLoginState returns and deletes a login state, so each one can be used only once.
func (s *Store) TakeLoginState(ctx context.Context, state string, maxAge time.Duration) (*LoginState, error) {
	ls := LoginState{State: state}
	err := s.db.QueryRowContext(ctx, `DELETE FROM login_states WHERE state = ? AND created_at > ? RETURNING nonce, verifier, rd`,
		state, time.Now().Add(-maxAge).Unix()).Scan(&ls.Nonce, &ls.Verifier, &ls.RD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ls, nil
}

// SigningKey is a stored Ed25519 private key in PKCS#8 DER form.
type SigningKey struct {
	KID        string
	PrivateKey []byte
	CreatedAt  time.Time
}

// SigningKeys returns all keys, newest first.
func (s *Store) SigningKeys(ctx context.Context) ([]SigningKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kid, private_key, created_at FROM signing_keys ORDER BY created_at DESC, kid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SigningKey
	for rows.Next() {
		var k SigningKey
		var created int64
		if err := rows.Scan(&k.KID, &k.PrivateKey, &created); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(created, 0)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) AddSigningKey(ctx context.Context, k SigningKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO signing_keys (kid, private_key, created_at) VALUES (?, ?, ?)`, k.KID, k.PrivateKey, k.CreatedAt.Unix())
	return err
}
