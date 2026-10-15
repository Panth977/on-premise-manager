package devtools

// Saved queries / requests, HTTP environments (secret values stored like literal passwords) and history (= the
// devtools.query activities of the site; MQTT publishes are listed too).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"opm/agent/internal/model"
	"opm/agent/internal/ulid"
)

func checkSaved(in *SavedInput) *Error {
	in.Name = strings.TrimSpace(in.Name)
	in.Collection = strings.TrimSpace(in.Collection)
	switch {
	case !inList(model.DevToolKinds, in.Kind):
		return invalid("kind must be one of %s", strings.Join(model.DevToolKinds, ", "))
	case in.Name == "" || utf8.RuneCountInString(in.Name) > 120:
		return invalid("name must be 1–120 characters")
	case utf8.RuneCountInString(in.Collection) > 80:
		return invalid("collection must be at most 80 characters")
	case utf8.RuneCountInString(in.Description) > 2000:
		return invalid("description must be at most 2000 characters")
	case len(in.Content) > 1<<20:
		return invalid("content must be at most 1 MiB")
	}
	if in.ConnectionID != nil && *in.ConnectionID == "" {
		in.ConnectionID = nil
	}
	return nil
}

func (s *Service) handleListSaved(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	l, err := s.listSaved(r.URL.Query().Get("kind"), r.URL.Query().Get("connectionId"))
	if err != nil {
		return nil, 0, unavailable("saved: %s", err.Error())
	}
	return model.ListResponse[Saved]{Items: l}, 200, nil
}

func (s *Service) handleCreateSaved(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var in SavedInput
	if e := readBody(r, &in, 2<<20); e != nil {
		return nil, 0, e
	}
	if e := checkSaved(&in); e != nil {
		return nil, 0, e
	}
	now := ts(time.Now())
	x := &Saved{ID: "s_" + ulid.New(), Kind: in.Kind, ConnectionID: in.ConnectionID, Collection: in.Collection, Name: in.Name, Description: in.Description,
		Content: in.Content, CreatedBy: actorPtr(c), CreatedAt: now, UpdatedAt: now}
	if err := s.putSaved(x); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	return x, 201, nil
}

func (s *Service) handleUpdateSaved(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	old, err := s.getSaved(r.PathValue("savedId"))
	if err != nil {
		return nil, 0, notFound("saved item %s does not exist", r.PathValue("savedId"))
	}
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var in SavedInput
	if e := readBody(r, &in, 2<<20); e != nil {
		return nil, 0, e
	}
	if e := checkSaved(&in); e != nil {
		return nil, 0, e
	}
	x := &Saved{ID: old.ID, Kind: in.Kind, ConnectionID: in.ConnectionID, Collection: in.Collection, Name: in.Name, Description: in.Description,
		Content: in.Content, CreatedBy: old.CreatedBy, CreatedAt: old.CreatedAt, UpdatedAt: ts(time.Now())}
	if err := s.putSaved(x); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	return x, 200, nil
}

func (s *Service) handleDeleteSaved(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, err := s.h.DB().Exec(`DELETE FROM devtools_saved WHERE id = ?`, r.PathValue("savedId"))
	if err != nil {
		return nil, 0, unavailable("delete: %s", err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, 0, notFound("saved item %s does not exist", r.PathValue("savedId"))
	}
	return map[string]bool{"deleted": true}, 200, nil
}

// ───────────────────────── environments

func envView(e *storedEnv) Environment {
	v := e.Env
	vars := make([]EnvVar, len(v.Variables))
	for i, x := range v.Variables {
		if x.Secret {
			x.Value = model.Mask
		}
		vars[i] = x
	}
	v.Variables = vars
	return v
}

func (s *Service) envFromInput(in *EnvironmentInput, old *storedEnv) (*storedEnv, *Error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 {
		return nil, invalid("name must be 1–80 characters")
	}
	if len(in.Variables) > 500 {
		return nil, invalid("at most 500 variables")
	}
	out := &storedEnv{Secrets: map[string]string{}}
	seen := map[string]bool{}
	for _, v := range in.Variables {
		n := strings.TrimSpace(v.Name)
		if n == "" || strings.ContainsAny(n, "{}") || len(n) > 200 {
			return nil, invalid("variable name %q must be 1–200 characters without { }", v.Name)
		}
		if seen[n] {
			return nil, invalid("variable %s is defined twice", n)
		}
		seen[n] = true
		if len(v.Value) > 64<<10 {
			return nil, invalid("variable %s: at most 64 KiB", n)
		}
		ev := EnvVar{Name: n, Value: v.Value, Secret: v.Secret, Enabled: v.Enabled}
		if v.Secret {
			val := v.Value
			if val == model.Mask {
				prev, ok := "", false
				if old != nil {
					prev, ok = old.Secrets[n]
				}
				if !ok {
					return nil, invalid("variable %s: %s keeps the stored value, but there is none", n, model.Mask)
				}
				val = prev
			}
			out.Secrets[n] = val
			ev.Value = model.Mask
		}
		out.Env.Variables = append(out.Env.Variables, ev)
	}
	if out.Env.Variables == nil {
		out.Env.Variables = []EnvVar{}
	}
	out.Env.Name = in.Name
	return out, nil
}

func (s *Service) handleListEnvironments(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	l, err := s.listEnvs()
	if err != nil {
		return nil, 0, unavailable("environments: %s", err.Error())
	}
	items := []Environment{}
	for i := range l {
		items = append(items, envView(&l[i]))
	}
	return model.ListResponse[Environment]{Items: items}, 200, nil
}

func (s *Service) handleCreateEnvironment(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var in EnvironmentInput
	if e := readBody(r, &in, 8<<20); e != nil {
		return nil, 0, e
	}
	e, de := s.envFromInput(&in, nil)
	if de != nil {
		return nil, 0, de
	}
	e.Env.ID, e.Env.CreatedBy, e.Env.UpdatedAt = "e_"+ulid.New(), actorPtr(c), ts(time.Now())
	if err := s.putEnv(e); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	return envView(e), 201, nil
}

func (s *Service) handleUpdateEnvironment(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	old, err := s.getEnv(r.PathValue("environmentId"))
	if err != nil {
		return nil, 0, notFound("environment %s does not exist", r.PathValue("environmentId"))
	}
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var in EnvironmentInput
	if e := readBody(r, &in, 8<<20); e != nil {
		return nil, 0, e
	}
	e, de := s.envFromInput(&in, old)
	if de != nil {
		return nil, 0, de
	}
	e.Env.ID, e.Env.CreatedBy, e.Env.UpdatedAt = old.Env.ID, old.Env.CreatedBy, ts(time.Now())
	if err := s.putEnv(e); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	return envView(e), 200, nil
}

func (s *Service) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	res, err := s.h.DB().Exec(`DELETE FROM devtools_environments WHERE id = ?`, r.PathValue("environmentId"))
	if err != nil {
		return nil, 0, unavailable("delete: %s", err.Error())
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, 0, notFound("environment %s does not exist", r.PathValue("environmentId"))
	}
	return map[string]bool{"deleted": true}, 200, nil
}

func (s *Service) handleRevealEnvironment(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	noStore(w)
	e, err := s.getEnv(r.PathValue("environmentId"))
	if err != nil {
		return nil, 0, notFound("environment %s does not exist", r.PathValue("environmentId"))
	}
	name := r.URL.Query().Get("name")
	v, ok := e.Secrets[name]
	if !ok {
		return nil, 0, notFound("environment %q has no secret variable %q", e.Env.Name, name)
	}
	if de := s.allow("reveal", c); de != nil {
		return nil, 0, de
	}
	s.activity(c, "devtools.credential-revealed", fmt.Sprintf("dev tools: secret variable %s of environment %q revealed", name, e.Env.Name),
		map[string]any{"environmentId": e.Env.ID, "name": e.Env.Name, "field": name})
	return RevealResponse{Value: v, HideAfterSec: model.RevealSeconds}, 200, nil
}

// ───────────────────────── history

func (s *Service) handleHistory(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	before := 1 << 62
	if cur := q.Get("cursor"); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil {
			return nil, 0, invalid("bad cursor")
		}
		before = n
	}
	since := time.Now().AddDate(0, 0, -s.limits().HistoryDays).UTC().Format("2006-01-02T15:04:05")
	sqlq := `SELECT seq, activityId, ts, actor, type, data FROM activities WHERE type IN ('devtools.query', 'devtools.mqtt-published') AND seq < ? AND ts >= ?`
	args := []any{before, since}
	if v := q.Get("connectionId"); v != "" {
		sqlq += ` AND json_extract(data, '$.connectionId') = ?`
		args = append(args, v)
	}
	if v := q.Get("kind"); v != "" {
		sqlq += ` AND (json_extract(data, '$.kind') = ? OR (type = 'devtools.mqtt-published' AND ? = 'mqtt'))`
		args = append(args, v, v)
	}
	if v := q.Get("actor"); v != "" {
		sqlq += ` AND actor = ?`
		args = append(args, strings.ToLower(v))
	}
	if m := q.Get("mine"); m == "1" || m == "true" {
		a := c.Actor
		if a == "" && c.Via == "" {
			a = s.h.SessionActor(c.SessionID)
		}
		if a != "" {
			sqlq += ` AND actor = ?`
			args = append(args, strings.ToLower(a))
		} else {
			sqlq += ` AND json_extract(data, '$.sessionId') = ?`
			args = append(args, c.SessionID)
		}
	}
	sqlq += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.h.DB().Query(sqlq, args...)
	if err != nil {
		return nil, 0, unavailable("history: %s", err.Error())
	}
	type raw struct {
		seq   int
		id    string
		ts    string
		actor sql.NullString
		typ   string
		data  string
	}
	var rs []raw
	for rows.Next() {
		var x raw
		if rows.Scan(&x.seq, &x.id, &x.ts, &x.actor, &x.typ, &x.data) == nil {
			rs = append(rs, x)
		}
	}
	rows.Close()
	var cursor *string
	if len(rs) > limit {
		rs = rs[:limit]
		cs := strconv.Itoa(rs[len(rs)-1].seq)
		cursor = &cs
	}
	items := []HistoryItem{}
	for _, x := range rs {
		var d map[string]any
		_ = json.Unmarshal([]byte(x.data), &d)
		str := func(k string) string {
			v, _ := d[k].(string)
			return v
		}
		num := func(k string) *int64 {
			if f, ok := d[k].(float64); ok {
				n := int64(f)
				return &n
			}
			return nil
		}
		it := HistoryItem{ActivityID: x.id, Ts: x.ts, Actor: scanNull(x.actor), ConnectionID: str("connectionId"), ConnectionName: str("connectionName"),
			Kind: str("kind"), Op: str("op"), Statement: str("statement"), Rows: num("rows"), Error: strOrNil(str("error")), Via: strOrNil(str("via"))}
		it.OK, _ = d["ok"].(bool)
		if n := num("durationMs"); n != nil {
			it.DurationMs = *n
		}
		if n := num("status"); n != nil {
			st := int(*n)
			it.Status = &st
		}
		if x.typ == "devtools.mqtt-published" {
			it.Kind, it.Op = "mqtt", "mqtt-publish"
			retain := ""
			if b, _ := d["retain"].(bool); b {
				retain = " retain"
			}
			q, _ := d["qos"].(float64)
			b, _ := d["bytes"].(float64)
			it.Statement = fmt.Sprintf("PUBLISH %s qos %d%s (%d bytes)", str("topic"), int(q), retain, int64(b))
		}
		items = append(items, it)
	}
	return model.PageResponse[HistoryItem]{Items: items, Cursor: cursor}, 200, nil
}
