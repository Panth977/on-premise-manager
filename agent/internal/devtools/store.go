package devtools

// Agent SQLite tables of the dev tools (created with IF NOT EXISTS like intent_waits — no schema bump; an older agent
// opening the DB ignores them). Literal credentials / secret environment values live in secret_json only; every
// other column holds MASK in their place.

import (
	"database/sql"
	"encoding/json"
	"errors"

	"opm/agent/internal/model"
)

const tablesSQL = `
CREATE TABLE IF NOT EXISTS devtools_connections (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL, def TEXT NOT NULL, secret_json TEXT NOT NULL DEFAULT '{}',
  created_by TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS devtools_saved (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, connection_id TEXT, collection TEXT NOT NULL, name TEXT NOT NULL,
  description TEXT NOT NULL, content TEXT NOT NULL, created_by TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS devtools_environments (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, variables TEXT NOT NULL, secret_json TEXT NOT NULL DEFAULT '{}',
  created_by TEXT, updated_at TEXT NOT NULL);
`

func createTables(db *sql.DB) error {
	if db == nil {
		return errors.New("no database")
	}
	_, err := db.Exec(tablesSQL)
	return err
}

// storedConn is one row of devtools_connections.
type storedConn struct {
	ID        string
	Def       ConnectionInput   // literal values = MASK
	Secrets   map[string]string // password / token literals
	CreatedBy *string
	CreatedAt string
	UpdatedAt string
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func scanNull(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

// maskDef returns the definition with literal credential values replaced by MASK.
func maskDef(in ConnectionInput) ConnectionInput {
	mask := func(c model.DevToolCredential) model.DevToolCredential {
		if c.Kind == "literal" {
			m := model.Mask
			return model.DevToolCredential{Kind: "literal", Value: &m}
		}
		return c
	}
	in.Auth.Password = mask(in.Auth.Password)
	in.Auth.Token = mask(in.Auth.Token)
	return in
}

func (s *Service) loadConns() ([]storedConn, error) {
	rows, err := s.h.DB().Query(`SELECT id, def, secret_json, created_by, created_at, updated_at FROM devtools_connections ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []storedConn
	for rows.Next() {
		var c storedConn
		var def, sec string
		var by sql.NullString
		if rows.Scan(&c.ID, &def, &sec, &by, &c.CreatedAt, &c.UpdatedAt) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(def), &c.Def)
		_ = json.Unmarshal([]byte(sec), &c.Secrets)
		if c.Secrets == nil {
			c.Secrets = map[string]string{}
		}
		c.CreatedBy = scanNull(by)
		out = append(out, c)
	}
	rows.Close()
	return out, rows.Err()
}

func (s *Service) loadConn(id string) (*storedConn, error) {
	var c storedConn
	var def, sec string
	var by sql.NullString
	err := s.h.DB().QueryRow(`SELECT id, def, secret_json, created_by, created_at, updated_at FROM devtools_connections WHERE id = ?`, id).
		Scan(&c.ID, &def, &sec, &by, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(def), &c.Def)
	_ = json.Unmarshal([]byte(sec), &c.Secrets)
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	c.CreatedBy = scanNull(by)
	return &c, nil
}

func (s *Service) saveConn(c *storedConn) error {
	def := maskDef(c.Def)
	_, err := s.h.DB().Exec(`INSERT INTO devtools_connections(id, name, kind, def, secret_json, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET name = excluded.name, kind = excluded.kind, def = excluded.def, secret_json = excluded.secret_json, updated_at = excluded.updated_at`,
		c.ID, def.Name, def.Kind, jsonStr(def), jsonStr(c.Secrets), nullable(c.CreatedBy), c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *Service) deleteConnRow(id string) error {
	_, err := s.h.DB().Exec(`DELETE FROM devtools_connections WHERE id = ?`, id)
	return err
}

// ───────────────────────── saved

func scanSaved(rows *sql.Rows) []Saved {
	out := []Saved{}
	for rows.Next() {
		var x Saved
		var conn, by sql.NullString
		if rows.Scan(&x.ID, &x.Kind, &conn, &x.Collection, &x.Name, &x.Description, &x.Content, &by, &x.CreatedAt, &x.UpdatedAt) != nil {
			continue
		}
		x.ConnectionID, x.CreatedBy = scanNull(conn), scanNull(by)
		out = append(out, x)
	}
	rows.Close()
	return out
}

const savedCols = `id, kind, connection_id, collection, name, description, content, created_by, created_at, updated_at`

func (s *Service) listSaved(kind, connID string) ([]Saved, error) {
	q := `SELECT ` + savedCols + ` FROM devtools_saved WHERE 1=1`
	var args []any
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if connID != "" {
		q += ` AND (connection_id = ? OR connection_id IS NULL)`
		args = append(args, connID)
	}
	q += ` ORDER BY collection, name, id`
	rows, err := s.h.DB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanSaved(rows), nil
}

func (s *Service) getSaved(id string) (*Saved, error) {
	rows, err := s.h.DB().Query(`SELECT `+savedCols+` FROM devtools_saved WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	l := scanSaved(rows)
	if len(l) == 0 {
		return nil, sql.ErrNoRows
	}
	return &l[0], nil
}

func (s *Service) putSaved(x *Saved) error {
	_, err := s.h.DB().Exec(`INSERT INTO devtools_saved(`+savedCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, connection_id = excluded.connection_id, collection = excluded.collection,
	  name = excluded.name, description = excluded.description, content = excluded.content, updated_at = excluded.updated_at`,
		x.ID, x.Kind, nullable(x.ConnectionID), x.Collection, x.Name, x.Description, x.Content, nullable(x.CreatedBy), x.CreatedAt, x.UpdatedAt)
	return err
}

// ───────────────────────── environments

type storedEnv struct {
	Env     Environment       // secret values = MASK
	Secrets map[string]string // name → value of secret variables
}

func (s *Service) listEnvs() ([]storedEnv, error) {
	rows, err := s.h.DB().Query(`SELECT id, name, variables, secret_json, created_by, updated_at FROM devtools_environments ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	var out []storedEnv
	for rows.Next() {
		var e storedEnv
		var vars, sec string
		var by sql.NullString
		if rows.Scan(&e.Env.ID, &e.Env.Name, &vars, &sec, &by, &e.Env.UpdatedAt) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(vars), &e.Env.Variables)
		_ = json.Unmarshal([]byte(sec), &e.Secrets)
		if e.Env.Variables == nil {
			e.Env.Variables = []EnvVar{}
		}
		if e.Secrets == nil {
			e.Secrets = map[string]string{}
		}
		e.Env.CreatedBy = scanNull(by)
		out = append(out, e)
	}
	rows.Close()
	return out, rows.Err()
}

func (s *Service) getEnv(id string) (*storedEnv, error) {
	l, err := s.listEnvs()
	if err != nil {
		return nil, err
	}
	for i := range l {
		if l[i].Env.ID == id {
			return &l[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Service) putEnv(e *storedEnv) error {
	_, err := s.h.DB().Exec(`INSERT INTO devtools_environments(id, name, variables, secret_json, created_by, updated_at) VALUES(?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET name = excluded.name, variables = excluded.variables, secret_json = excluded.secret_json, updated_at = excluded.updated_at`,
		e.Env.ID, e.Env.Name, jsonStr(e.Env.Variables), jsonStr(e.Secrets), nullable(e.Env.CreatedBy), e.Env.UpdatedAt)
	return err
}
