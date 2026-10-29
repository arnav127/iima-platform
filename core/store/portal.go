package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The deploy portal's tables. An app's settings are stored as JSON (a registry.App); this package
// does not interpret them. Approved holds what is live, Pending what waits for a super admin: for a
// new app both the first settings, for a live app a change to its path, login, auth or shape.
const portalSchema = `
CREATE TABLE IF NOT EXISTS portal_apps (
	id              TEXT PRIMARY KEY,
	approved        TEXT NOT NULL DEFAULT '',
	pending         TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,          -- pending | approved | archived
	repo            TEXT NOT NULL,          -- owner/name on GitHub
	installation_id INTEGER NOT NULL,
	branch          TEXT NOT NULL DEFAULT 'main',
	auto_deploy     INTEGER NOT NULL DEFAULT 1,
	created_by      TEXT NOT NULL,
	created_at      INTEGER NOT NULL,
	approved_by     TEXT NOT NULL DEFAULT '',
	approved_at     INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS portal_apps_repo ON portal_apps (repo) WHERE status != 'archived';

CREATE TABLE IF NOT EXISTS portal_owners (
	app_id   TEXT NOT NULL REFERENCES portal_apps (id) ON DELETE CASCADE,
	email    TEXT NOT NULL,
	added_by TEXT NOT NULL,
	added_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, email)
);
CREATE INDEX IF NOT EXISTS portal_owners_email ON portal_owners (email);

-- Plain env vars keep their value here. Secrets live only in the app's Nomad Variable; this table
-- remembers their names so the portal can list them.
CREATE TABLE IF NOT EXISTS portal_env (
	app_id     TEXT NOT NULL REFERENCES portal_apps (id) ON DELETE CASCADE,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL DEFAULT '',
	secret     INTEGER NOT NULL DEFAULT 0,
	updated_by TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, key)
);

-- Builds the portal has fetched. File is the artifact on core's volume ("" once pruned).
CREATE TABLE IF NOT EXISTS portal_releases (
	app_id     TEXT NOT NULL REFERENCES portal_apps (id) ON DELETE CASCADE,
	tag        TEXT NOT NULL,
	commit_sha TEXT NOT NULL DEFAULT '',
	sha256     TEXT NOT NULL,
	size       INTEGER NOT NULL DEFAULT 0,
	file       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, tag)
);

CREATE TABLE IF NOT EXISTS portal_deploys (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	app_id     TEXT NOT NULL REFERENCES portal_apps (id) ON DELETE CASCADE,
	tag        TEXT NOT NULL,
	actor      TEXT NOT NULL,              -- an email, or "auto"
	at         INTEGER NOT NULL,
	status     TEXT NOT NULL,              -- submitted | failed
	message    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS portal_deploys_app ON portal_deploys (app_id, at);
`

// App statuses.
const (
	AppPending  = "pending"
	AppApproved = "approved"
	AppArchived = "archived"
)

// ErrExists is returned when an app id or repo is already taken.
var ErrExists = errors.New("store: already exists")

type PortalApp struct {
	ID             string
	Approved       string // JSON settings that are live; "" until first approved
	Pending        string // JSON settings waiting for a super admin; "" when none
	Status         string
	Repo           string
	InstallationID int64
	Branch         string
	AutoDeploy     bool
	CreatedBy      string
	CreatedAt      time.Time
	ApprovedBy     string
	ApprovedAt     time.Time
}

type EnvVar struct {
	Key       string
	Value     string // "" for secrets
	Secret    bool
	UpdatedBy string
	UpdatedAt time.Time
}

type Release struct {
	AppID     string
	Tag       string
	Commit    string
	SHA256    string
	Size      int64
	File      string
	CreatedAt time.Time
}

type Deploy struct {
	ID      int64
	AppID   string
	Tag     string
	Actor   string
	At      time.Time
	Status  string
	Message string
}

const portalCols = `id, approved, pending, status, repo, installation_id, branch, auto_deploy, created_by, created_at, approved_by, approved_at`

func scanApp(row interface{ Scan(...any) error }) (*PortalApp, error) {
	var a PortalApp
	var created, approved int64
	var auto int
	if err := row.Scan(&a.ID, &a.Approved, &a.Pending, &a.Status, &a.Repo, &a.InstallationID, &a.Branch, &auto, &a.CreatedBy, &created, &a.ApprovedBy, &approved); err != nil {
		return nil, err
	}
	a.AutoDeploy = auto == 1
	a.CreatedAt = time.Unix(created, 0)
	if approved > 0 {
		a.ApprovedAt = time.Unix(approved, 0)
	}
	return &a, nil
}

// CreatePortalApp records a requested app and its first owner.
func (s *Store) CreatePortalApp(ctx context.Context, a PortalApp, owner string) error {
	owner = NormEmail(owner)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		_, err := tx.ExecContext(ctx, `INSERT INTO portal_apps (id, pending, status, repo, installation_id, branch, auto_deploy, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, a.ID, a.Pending, AppPending, a.Repo, a.InstallationID, orStr(a.Branch, "main"), owner, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrExists
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO portal_owners (app_id, email, added_by, added_at) VALUES (?, ?, ?, ?)`, a.ID, owner, owner, now); err != nil {
			return err
		}
		return audit(ctx, tx, owner, "app.request", a.ID, a.Repo, nil)
	})
}

func (s *Store) PortalApp(ctx context.Context, id string) (*PortalApp, error) {
	a, err := scanApp(s.db.QueryRowContext(ctx, `SELECT `+portalCols+` FROM portal_apps WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// PortalApps returns every app, newest first.
func (s *Store) PortalApps(ctx context.Context) ([]*PortalApp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+portalCols+` FROM portal_apps ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PortalApp
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetPortalSettings stores new settings. approved and pending are written as given; detail goes into
// the audit log.
func (s *Store) SetPortalSettings(ctx context.Context, actor, id, approved, pending string, detail any) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE portal_apps SET approved = ?, pending = ? WHERE id = ?`, approved, pending, id); err != nil {
			return err
		}
		return audit(ctx, tx, NormEmail(actor), "app.settings", id, "", detail)
	})
}

// ApprovePortalApp makes the pending settings live.
func (s *Store) ApprovePortalApp(ctx context.Context, actor, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE portal_apps SET approved = pending, pending = '', status = ?, approved_by = ?, approved_at = ?
			WHERE id = ? AND pending != ''`, AppApproved, NormEmail(actor), time.Now().Unix(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, NormEmail(actor), "app.approve", id, "", nil)
	})
}

// RejectPending drops pending settings. A new app that was never approved is archived.
func (s *Store) RejectPending(ctx context.Context, actor, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE portal_apps SET pending = '', status = CASE WHEN approved = '' THEN ? ELSE status END WHERE id = ?`, AppArchived, id); err != nil {
			return err
		}
		return audit(ctx, tx, NormEmail(actor), "app.reject", id, "", nil)
	})
}

func (s *Store) SetAutoDeploy(ctx context.Context, actor, id string, on bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE portal_apps SET auto_deploy = ? WHERE id = ?`, b2i(on), id); err != nil {
			return err
		}
		return audit(ctx, tx, NormEmail(actor), "app.autodeploy", id, "", map[string]bool{"on": on})
	})
}

// ---- owners ----

func (s *Store) Owners(ctx context.Context, id string) ([]string, error) {
	return s.strings(ctx, `SELECT email FROM portal_owners WHERE app_id = ? ORDER BY email`, id)
}

func (s *Store) IsOwner(ctx context.Context, id, email string) (bool, error) {
	return s.exists(ctx, `SELECT 1 FROM portal_owners WHERE app_id = ? AND email = ?`, id, NormEmail(email))
}

// OwnedApps returns the ids of apps email owns.
func (s *Store) OwnedApps(ctx context.Context, email string) ([]string, error) {
	return s.strings(ctx, `SELECT app_id FROM portal_owners WHERE email = ? ORDER BY app_id`, NormEmail(email))
}

func (s *Store) SetOwner(ctx context.Context, actor, id, email string, add bool) error {
	email = NormEmail(email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if add {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO portal_owners (app_id, email, added_by, added_at) VALUES (?, ?, ?, ?)`, id, email, NormEmail(actor), time.Now().Unix()); err != nil {
				return err
			}
			return audit(ctx, tx, NormEmail(actor), "owner.add", id, email, nil)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM portal_owners WHERE app_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return errors.New("store: an app keeps at least one owner")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM portal_owners WHERE app_id = ? AND email = ?`, id, email); err != nil {
			return err
		}
		return audit(ctx, tx, NormEmail(actor), "owner.remove", id, email, nil)
	})
}

// ---- env ----

func (s *Store) Env(ctx context.Context, id string) ([]EnvVar, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, secret, updated_by, updated_at FROM portal_env WHERE app_id = ? ORDER BY key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var e EnvVar
		var secret int
		var at int64
		if err := rows.Scan(&e.Key, &e.Value, &secret, &e.UpdatedBy, &at); err != nil {
			return nil, err
		}
		e.Secret, e.UpdatedAt = secret == 1, time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetEnv stores a plain value, or only the name of a secret (its value goes to Nomad, not here).
func (s *Store) SetEnv(ctx context.Context, actor, id string, e EnvVar) error {
	if e.Secret {
		e.Value = ""
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO portal_env (app_id, key, value, secret, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (app_id, key) DO UPDATE SET value = excluded.value, secret = excluded.secret, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			id, e.Key, e.Value, b2i(e.Secret), NormEmail(actor), time.Now().Unix())
		if err != nil {
			return err
		}
		// Values stay out of the audit log: a plain value can still be sensitive enough.
		return audit(ctx, tx, NormEmail(actor), "env.set", id, e.Key, map[string]bool{"secret": e.Secret})
	})
}

func (s *Store) DeleteEnv(ctx context.Context, actor, id, key string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM portal_env WHERE app_id = ? AND key = ?`, id, key); err != nil {
			return err
		}
		return audit(ctx, tx, NormEmail(actor), "env.delete", id, key, nil)
	})
}

// ---- releases and deploys ----

// AddRelease records a fetched build; adding the same tag again is a no-op.
func (s *Store) AddRelease(ctx context.Context, r Release) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO portal_releases (app_id, tag, commit_sha, sha256, size, file, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.AppID, r.Tag, r.Commit, r.SHA256, r.Size, r.File, r.CreatedAt.Unix())
	return err
}

// Releases returns an app's releases, newest first.
func (s *Store) Releases(ctx context.Context, id string) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app_id, tag, commit_sha, sha256, size, file, created_at FROM portal_releases WHERE app_id = ? ORDER BY created_at DESC, tag DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		var at int64
		if err := rows.Scan(&r.AppID, &r.Tag, &r.Commit, &r.SHA256, &r.Size, &r.File, &at); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReleaseFile returns the artifact file for a SHA-256, for serving to Nomad.
func (s *Store) ReleaseFile(ctx context.Context, sha string) (string, error) {
	var f string
	err := s.db.QueryRowContext(ctx, `SELECT file FROM portal_releases WHERE sha256 = ? AND file != '' LIMIT 1`, sha).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return f, err
}

// ForgetReleaseFile marks an artifact as pruned from disk.
func (s *Store) ForgetReleaseFile(ctx context.Context, id, tag string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE portal_releases SET file = '' WHERE app_id = ? AND tag = ?`, id, tag)
	return err
}

func (s *Store) AddDeploy(ctx context.Context, d Deploy) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO portal_deploys (app_id, tag, actor, at, status, message) VALUES (?, ?, ?, ?, ?, ?)`,
			d.AppID, d.Tag, d.Actor, time.Now().Unix(), d.Status, d.Message); err != nil {
			return err
		}
		return audit(ctx, tx, d.Actor, "deploy."+d.Status, d.AppID, d.Tag, nil)
	})
}

// Deploys returns an app's deploys, newest first.
func (s *Store) Deploys(ctx context.Context, id string, limit int) ([]Deploy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, app_id, tag, actor, at, status, message FROM portal_deploys WHERE app_id = ? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deploy
	for rows.Next() {
		var d Deploy
		var at int64
		if err := rows.Scan(&d.ID, &d.AppID, &d.Tag, &d.Actor, &at, &d.Status, &d.Message); err != nil {
			return nil, err
		}
		d.At = time.Unix(at, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
