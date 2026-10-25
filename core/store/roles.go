package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// ErrLastSuperAdmin stops the platform from being left with nobody who can manage it.
var ErrLastSuperAdmin = errors.New("store: cannot remove the last super admin")

// NormEmail is how emails are stored: trimmed and lower case.
func NormEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Admin is a super admin or an app admin.
type Admin struct {
	Email   string
	AddedBy string
	AddedAt time.Time
}

// AuditEntry is one change to roles, admins or attributes.
type AuditEntry struct {
	ID     int64
	At     time.Time
	Actor  string
	Action string
	AppID  string
	Target string
	Detail string
}

// audit is written in the same transaction as the change it describes.
func audit(ctx context.Context, tx *sql.Tx, actor, action, app, target string, detail any) error {
	d := ""
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		d = string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log (at, actor, action, app_id, target, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, app, target, d)
	return err
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) admins(ctx context.Context, query string, args ...any) ([]Admin, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		var at int64
		if err := rows.Scan(&a.Email, &a.AddedBy, &at); err != nil {
			return nil, err
		}
		a.AddedAt = time.Unix(at, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- super admins ----

func (s *Store) IsSuperAdmin(ctx context.Context, email string) (bool, error) {
	return s.exists(ctx, `SELECT 1 FROM super_admins WHERE email = ?`, NormEmail(email))
}

func (s *Store) SuperAdmins(ctx context.Context) ([]Admin, error) {
	return s.admins(ctx, `SELECT email, added_by, added_at FROM super_admins ORDER BY email`)
}

func (s *Store) AddSuperAdmin(ctx context.Context, actor, email string) error {
	email = NormEmail(email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO super_admins (email, added_by, added_at) VALUES (?, ?, ?)`, email, actor, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return audit(ctx, tx, actor, "superadmin.add", "", email, nil)
	})
}

func (s *Store) RemoveSuperAdmin(ctx context.Context, actor, email string) error {
	email = NormEmail(email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM super_admins`).Scan(&n); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM super_admins WHERE email = ?`, email)
		if err != nil {
			return err
		}
		if removed, _ := res.RowsAffected(); removed == 0 {
			return nil
		}
		if n <= 1 {
			return ErrLastSuperAdmin
		}
		return audit(ctx, tx, actor, "superadmin.remove", "", email, nil)
	})
}

// ---- app admins ----

func (s *Store) IsAppAdmin(ctx context.Context, app, email string) (bool, error) {
	return s.exists(ctx, `SELECT 1 FROM app_admins WHERE app_id = ? AND email = ?`, app, NormEmail(email))
}

func (s *Store) AppAdmins(ctx context.Context, app string) ([]Admin, error) {
	return s.admins(ctx, `SELECT email, added_by, added_at FROM app_admins WHERE app_id = ? ORDER BY email`, app)
}

// AdminOf lists the apps email is an app admin of.
func (s *Store) AdminOf(ctx context.Context, email string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app_id FROM app_admins WHERE email = ? ORDER BY app_id`, NormEmail(email))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) AddAppAdmin(ctx context.Context, actor, app, email string) error {
	email = NormEmail(email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO app_admins (app_id, email, added_by, added_at) VALUES (?, ?, ?, ?)`, app, email, actor, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return audit(ctx, tx, actor, "appadmin.add", app, email, nil)
	})
}

func (s *Store) RemoveAppAdmin(ctx context.Context, actor, app, email string) error {
	email = NormEmail(email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM app_admins WHERE app_id = ? AND email = ?`, app, email)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return audit(ctx, tx, actor, "appadmin.remove", app, email, nil)
	})
}

// ---- roles ----

// Roles returns email's roles in app, sorted.
func (s *Store) Roles(ctx context.Context, app, email string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role FROM role_grants WHERE app_id = ? AND email = ? ORDER BY role`, app, NormEmail(email))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Grant is one person's roles in an app.
type Grant struct {
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

// Grants lists everyone with a role in app, by email.
func (s *Store) Grants(ctx context.Context, app string) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email, role FROM role_grants WHERE app_id = ? ORDER BY email, role`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var email, role string
		if err := rows.Scan(&email, &role); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Email == email {
			out[n-1].Roles = append(out[n-1].Roles, role)
		} else {
			out = append(out, Grant{email, []string{role}})
		}
	}
	return out, rows.Err()
}

// SetRoles replaces email's roles in app with roles (empty removes them all). The caller checks
// that the roles are declared in the app's manifest.
func (s *Store) SetRoles(ctx context.Context, actor, app, email string, roles []string) error {
	email = NormEmail(email)
	want := slices.Clone(roles)
	slices.Sort(want)
	want = slices.Compact(want)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT role FROM role_grants WHERE app_id = ? AND email = ? ORDER BY role`, app, email)
		if err != nil {
			return err
		}
		var have []string
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return err
			}
			have = append(have, r)
		}
		rows.Close()
		if slices.Equal(have, want) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_grants WHERE app_id = ? AND email = ?`, app, email); err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, r := range want {
			if _, err := tx.ExecContext(ctx, `INSERT INTO role_grants (app_id, email, role, granted_by, granted_at) VALUES (?, ?, ?, ?, ?)`, app, email, r, actor, now); err != nil {
				return err
			}
		}
		if have == nil {
			have = []string{}
		}
		if want == nil {
			want = []string{}
		}
		return audit(ctx, tx, actor, "roles.set", app, email, map[string][]string{"from": have, "to": want})
	})
}

// ---- attribute overrides ----

type Override struct {
	Email       string `json:"email"`
	Affiliation string `json:"affiliation,omitempty"`
	Programme   string `json:"programme,omitempty"`
	Batch       int    `json:"batch,omitempty"`
	Note        string `json:"note,omitempty"`
	UpdatedBy   string `json:"updatedBy,omitempty"`
}

// AttributeOverride returns the override for email, or nil.
func (s *Store) AttributeOverride(ctx context.Context, email string) (*Override, error) {
	o := Override{Email: NormEmail(email)}
	err := s.db.QueryRowContext(ctx, `SELECT affiliation, programme, batch, note, updated_by FROM attribute_overrides WHERE email = ?`, o.Email).
		Scan(&o.Affiliation, &o.Programme, &o.Batch, &o.Note, &o.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) Overrides(ctx context.Context) ([]Override, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email, affiliation, programme, batch, note, updated_by FROM attribute_overrides ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Override
	for rows.Next() {
		var o Override
		if err := rows.Scan(&o.Email, &o.Affiliation, &o.Programme, &o.Batch, &o.Note, &o.UpdatedBy); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetOverride stores o, or removes the override when every field is empty.
func (s *Store) SetOverride(ctx context.Context, actor string, o Override) error {
	o.Email = NormEmail(o.Email)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if o.Affiliation == "" && o.Programme == "" && o.Batch == 0 {
			res, err := tx.ExecContext(ctx, `DELETE FROM attribute_overrides WHERE email = ?`, o.Email)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			return audit(ctx, tx, actor, "attributes.clear", "", o.Email, nil)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO attribute_overrides (email, affiliation, programme, batch, note, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (email) DO UPDATE SET affiliation = excluded.affiliation, programme = excluded.programme,
				batch = excluded.batch, note = excluded.note, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			o.Email, o.Affiliation, o.Programme, o.Batch, o.Note, actor, time.Now().Unix())
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "attributes.set", "", o.Email, map[string]any{"affiliation": o.Affiliation, "programme": o.Programme, "batch": o.Batch, "note": o.Note})
	})
}

// ---- audit ----

// Audit returns the newest entries first; app == "" means every app and platform-wide changes.
func (s *Store) Audit(ctx context.Context, app string, limit int) ([]AuditEntry, error) {
	q := `SELECT id, at, actor, action, app_id, target, detail FROM audit_log`
	args := []any{}
	if app != "" {
		q += ` WHERE app_id = ?`
		args = append(args, app)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.AppID, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
