package devtools

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/model"
	"opm/agent/internal/ulid"
	"opm/agent/internal/validate"
)

// ───────────────────────── info

func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	cfg := s.cfg.get()
	out := Info{SiteSlug: s.h.Site(), Enabled: cfg.Enabled, Reason: strOrNil(cfg.Reason()), ConfigPath: strOrNil(cfg.Path), Limits: cfg.Limits,
		Kinds: append([]string(nil), model.DevToolKinds...), Containers: []InfoContainer{}, Volumes: []InfoVolume{}, Secrets: []string{}, Variables: []string{}}
	if h := s.h.Head(); h != nil {
		for _, hc := range h.Containers {
			ic := InfoContainer{ContainerSlug: hc.ContainerSlug, ServiceSlug: hc.ServiceSlug, Image: hc.Image, Running: hc.Running, Ports: []InfoPort{}, Mounts: []InfoMount{}, Env: []InfoEnv{}}
			for _, e := range hc.Env {
				ic.Env = append(ic.Env, InfoEnv{Name: e.Name, Secret: e.Secret})
			}
			for _, p := range hc.Ports {
				ic.Ports = append(ic.Ports, InfoPort{PortName: p.Name, ContainerPort: p.ContainerPort, Protocol: p.Protocol, HostPort: p.HostPort})
			}
			for _, m := range hc.Mounts {
				if m.Kind == "secret" {
					continue
				}
				ic.Mounts = append(ic.Mounts, InfoMount{MountPath: m.Target, Kind: m.Kind, VolumeSlug: m.VolumeSlug})
			}
			out.Containers = append(out.Containers, ic)
		}
		for _, v := range h.Volumes {
			out.Volumes = append(out.Volumes, InfoVolume{VolumeSlug: v.Slug, Label: v.Label, Kind: v.Kind})
		}
		out.Secrets = append(out.Secrets, h.Secrets...)
		out.Variables = append(out.Variables, h.Variables...)
	}
	return out, 200, nil
}

// ───────────────────────── views

// maskedAuth returns the auth with literal values replaced by MASK.
func maskedAuth(a model.DevToolAuth) model.DevToolAuth {
	m := func(c model.DevToolCredential) model.DevToolCredential {
		if c.Kind == "literal" {
			v := model.Mask
			return model.DevToolCredential{Kind: "literal", Value: &v}
		}
		return c
	}
	a.Password, a.Token = m(a.Password), m(a.Token)
	return a
}

// problem says what a preset / connection references that the head does not have (nil = ok).
func (s *Service) problem(d *connDef) *string {
	h := s.h.Head()
	t := d.Target
	switch t.Kind {
	case "container", "container-file":
		if h == nil {
			return strPtr("no head revision on this site yet")
		}
		var hc *HeadContainer
		for i := range h.Containers {
			if h.Containers[i].ContainerSlug == model.Str(t.ContainerSlug) {
				hc = &h.Containers[i]
			}
		}
		if hc == nil {
			return strPtr(fmt.Sprintf("container %s is not in the head revision", model.Str(t.ContainerSlug)))
		}
		if t.Kind == "container" {
			found := false
			for _, p := range hc.Ports {
				if p.Name == model.Str(t.PortName) {
					found = true
				}
			}
			if !found {
				return strPtr(fmt.Sprintf("container %s has no port %s", hc.ContainerSlug, model.Str(t.PortName)))
			}
		}
		if !hc.Enabled {
			return strPtr(fmt.Sprintf("container %s is disabled", hc.ContainerSlug))
		}
	case "volume-file":
		found := false
		if h != nil {
			for _, v := range h.Volumes {
				if v.Slug == model.Str(t.VolumeSlug) {
					found = true
				}
			}
		}
		if !found {
			return strPtr(fmt.Sprintf("volume %s is not declared on this site", model.Str(t.VolumeSlug)))
		}
	}
	for _, sl := range refsOf(d, "secret") {
		if h == nil || !inList(h.Secrets, sl) {
			return strPtr(fmt.Sprintf("secret %s does not exist in the head revision", sl))
		}
	}
	for _, sl := range refsOf(d, "var") {
		if h == nil || !inList(h.Variables, sl) {
			return strPtr(fmt.Sprintf("variable %s does not exist in the head revision", sl))
		}
	}
	return nil
}

// refsOf lists secret.x / var.y references of the expression fields.
func refsOf(d *connDef, root string) []string {
	vals := []string{model.Str(d.Auth.Username), model.Str(d.Auth.Database), model.Str(d.Auth.ClientID), model.Str(d.Auth.APIKeyHeader), model.Str(d.Target.URL)}
	if d.Auth.Password.Kind == "expr" {
		vals = append(vals, model.Str(d.Auth.Password.Expr))
	}
	if d.Auth.Token.Kind == "expr" {
		vals = append(vals, model.Str(d.Auth.Token.Expr))
	}
	for _, h := range d.Options.DefaultHeaders {
		vals = append(vals, h.Value)
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		if !strings.Contains(v, "{{") {
			continue
		}
		segs, err := exprRefs(v)
		if err != nil {
			continue
		}
		for _, ref := range segs {
			parts := strings.SplitN(ref, ".", 3)
			if len(parts) >= 2 && parts[0] == root && !seen[parts[1]] {
				seen[parts[1]] = true
				out = append(out, parts[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) view(d *connDef) Connection {
	v := Connection{ID: d.ID, SiteSlug: s.h.Site(), Source: d.Source, PresetRevSeq: d.PresetRevSeq, Name: d.Name, Description: d.Description, Kind: d.Kind,
		Target: d.Target, Auth: maskedAuth(d.Auth), Options: d.Options, ReadOnly: d.ReadOnly, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		State: s.pool.state(d.ID), Problem: s.problem(d)}
	if v.Options.DefaultHeaders == nil {
		v.Options.DefaultHeaders = []model.DevToolHeader{}
	}
	s.lastTargetMu.Lock()
	if t, ok := s.lastTarget[d.ID]; ok {
		v.ResolvedTarget = &t
	}
	s.lastTargetMu.Unlock()
	if v.ResolvedTarget == nil {
		t := targetText(d.Target)
		v.ResolvedTarget = &t
	}
	return v
}

func (s *Service) noteTarget(id, text string) {
	s.lastTargetMu.Lock()
	s.lastTarget[id] = text
	s.lastTargetMu.Unlock()
}

// targetText is TS devToolTargetText.
func targetText(t model.DevToolTarget) string {
	q := func(p *string) string {
		if p == nil || *p == "" {
			return "?"
		}
		return *p
	}
	switch t.Kind {
	case "container":
		return q(t.ContainerSlug) + ":" + q(t.PortName)
	case "host":
		port := "?"
		if t.Port != nil {
			port = fmt.Sprint(int(*t.Port))
		}
		return model.Str(t.Host) + ":" + port
	case "url":
		return model.Str(t.URL)
	case "volume-file":
		return "volume " + q(t.VolumeSlug) + " → " + q(t.Path)
	case "container-file":
		return q(t.ContainerSlug) + ":" + q(t.Path)
	}
	return t.Kind
}

func (s *Service) allConns() ([]*connDef, error) {
	var out []*connDef
	if h := s.h.Head(); h != nil {
		for _, p := range h.Presets {
			out = append(out, presetDef(p, h.Seq))
		}
	}
	rows, err := s.loadConns()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		out = append(out, manualDef(&rows[i]))
	}
	return out, nil
}

func (s *Service) handleListConnections(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	all, err := s.allConns()
	if err != nil {
		return nil, 0, unavailable("dev tools storage: %s", err.Error())
	}
	items := []Connection{}
	for _, d := range all {
		items = append(items, s.view(d))
	}
	return model.ListResponse[Connection]{Items: items}, 200, nil
}

// ───────────────────────── manual connection input

func cleanInput(in *ConnectionInput) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Options.DefaultHeaders == nil {
		in.Options.DefaultHeaders = []model.DevToolHeader{}
	}
	if in.Auth.HTTPScheme == "" {
		in.Auth.HTTPScheme = "none"
	}
	for _, c := range []*model.DevToolCredential{&in.Auth.Password, &in.Auth.Token} {
		if c.Kind == "" {
			c.Kind = "none"
		}
		switch c.Kind {
		case "none":
			c.Expr, c.Value = nil, nil
		case "expr":
			c.Value = nil
		case "literal":
			c.Expr = nil
		}
	}
	// only the keys of the target kind
	t := in.Target
	nt := model.DevToolTarget{Kind: t.Kind}
	switch t.Kind {
	case "container":
		nt.ContainerSlug, nt.PortName = t.ContainerSlug, t.PortName
	case "host":
		nt.Host, nt.Port = t.Host, t.Port
	case "url":
		nt.URL = t.URL
	case "volume-file":
		nt.VolumeSlug, nt.Path = t.VolumeSlug, t.Path
	case "container-file":
		nt.ContainerSlug, nt.Path = t.ContainerSlug, t.Path
	}
	in.Target = nt
}

// checkInput validates a manual connection: the preset rules (validate.ValidateSiteDevTools on a stand-in preset)
// except that literal credentials are allowed, plus the references of THIS site's head.
func (s *Service) checkInput(in *ConnectionInput) *Error {
	cleanInput(in)
	p := model.DevToolPreset{PresetSlug: "manual", Name: in.Name, Description: in.Description, Kind: in.Kind, Target: in.Target, Auth: in.Auth, Options: in.Options, ReadOnly: in.ReadOnly}
	if p.Auth.Password.Kind == "literal" {
		p.Auth.Password = model.DevToolCredential{Kind: "expr", Expr: strPtr("{{ var.opm-literal }}")}
	}
	if p.Auth.Token.Kind == "literal" {
		p.Auth.Token = model.DevToolCredential{Kind: "expr", Expr: strPtr("{{ var.opm-literal }}")}
	}
	site := model.Site{SiteSlug: s.h.Site()}
	cfg := model.Config{Variables: []model.Variable{{VariableSlug: "opm-literal"}}}
	if h := s.h.Head(); h != nil {
		for _, hc := range h.Containers {
			site.Config.Containers = append(site.Config.Containers, model.Container{ContainerSlug: hc.ContainerSlug, ServiceSlug: hc.ServiceSlug, Version: "x", Enabled: true})
		}
		vols := []model.SiteVolume{}
		for _, v := range h.Volumes {
			k := v.Kind
			vols = append(vols, model.SiteVolume{VolumeSlug: v.Slug, Kind: &k})
		}
		site.Config.Volumes = &vols
		for _, sl := range h.Secrets {
			cfg.Secrets = append(cfg.Secrets, model.SecretDef{SecretSlug: sl})
		}
		for _, sl := range h.Variables {
			cfg.Variables = append(cfg.Variables, model.Variable{VariableSlug: sl})
		}
	}
	list := []model.DevToolPreset{p}
	site.Config.DevTools = &list
	is := validate.NewIssues(validate.ModePublish)
	validate.ValidateSiteDevTools(&site, &cfg, nil, is)
	errs := []model.ValidationIssue{}
	for _, x := range is.List {
		if x.Severity != "error" {
			continue
		}
		x.Path = strings.TrimPrefix(x.Path, "sites["+s.h.Site()+"].config.devTools[manual].")
		errs = append(errs, x)
	}
	// container ports (the stand-in containers have no catalog image)
	if in.Target.Kind == "container" && len(errs) == 0 {
		if hc, e := s.headContainer(model.Str(in.Target.ContainerSlug)); e == nil {
			found := false
			for _, hp := range hc.Ports {
				if hp.Name == model.Str(in.Target.PortName) {
					found = true
				}
			}
			if !found {
				errs = append(errs, model.ValidationIssue{Path: "target.portName", Code: "V_DEVTOOL_PORT", Message: fmt.Sprintf("container %s has no port %s", hc.ContainerSlug, model.Str(in.Target.PortName)), Severity: "error"})
			}
		}
	}
	if len(errs) > 0 {
		return &Error{Status: 422, Code: model.ErrInvalid, Msg: errs[0].Path + ": " + errs[0].Message, Details: map[string]any{"errors": errs}}
	}
	return nil
}

// applyLiterals keeps stored literals when the input sends MASK; returns whether a credential changed.
func applyLiterals(in *ConnectionInput, old map[string]string) (map[string]string, bool, *Error) {
	out := map[string]string{}
	changed := false
	for _, f := range []struct {
		name string
		c    *model.DevToolCredential
	}{{"password", &in.Auth.Password}, {"token", &in.Auth.Token}} {
		if f.c.Kind != "literal" {
			if _, had := old[f.name]; had {
				changed = true
			}
			continue
		}
		v := model.Str(f.c.Value)
		if v == model.Mask {
			prev, ok := old[f.name]
			if !ok {
				return nil, false, invalid("%s: %s keeps the stored value, but there is none; type the value", f.name, model.Mask)
			}
			v = prev
		} else if old[f.name] != v {
			changed = true
		}
		out[f.name] = v
		m := model.Mask
		f.c.Value = &m
	}
	return out, changed, nil
}

func (s *Service) handleCreateConnection(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var in ConnectionInput
	if e := readBody(r, &in, 1<<20); e != nil {
		return nil, 0, e
	}
	if e := s.checkInput(&in); e != nil {
		return nil, 0, e
	}
	secrets, _, e := applyLiterals(&in, map[string]string{})
	if e != nil {
		return nil, 0, e
	}
	now := ts(time.Now())
	sc := &storedConn{ID: "c_" + ulid.New(), Def: in, Secrets: secrets, CreatedBy: actorPtr(c), CreatedAt: now, UpdatedAt: now}
	if err := s.saveConn(sc); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	d := manualDef(sc)
	s.activity(c, "devtools.connection-created", fmt.Sprintf("dev tools: connection %q created (%s, %s)", in.Name, in.Kind, targetText(in.Target)),
		map[string]any{"connectionId": sc.ID, "name": in.Name, "kind": in.Kind, "target": targetText(in.Target), "readOnly": in.ReadOnly})
	return s.view(d), 201, nil
}

func (s *Service) handleUpdateConnection(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	id := r.PathValue("connectionId")
	if strings.HasPrefix(id, "p:") {
		if _, e := s.findConn(id); e != nil {
			return nil, 0, e
		}
		return nil, 0, conflict("%s is a preset of the deployment config (read-only on the site): duplicate it to edit locally", id)
	}
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	old, err := s.loadConn(id)
	if err != nil {
		return nil, 0, notFound("connection %s does not exist on site %s", id, s.h.Site())
	}
	var in ConnectionInput
	if e := readBody(r, &in, 1<<20); e != nil {
		return nil, 0, e
	}
	if e := s.checkInput(&in); e != nil {
		return nil, 0, e
	}
	secrets, credChanged, e := applyLiterals(&in, old.Secrets)
	if e != nil {
		return nil, 0, e
	}
	var changed []string
	o := old.Def
	cmp := func(name string, a, b any) {
		if jsonStr(a) != jsonStr(b) {
			changed = append(changed, name)
		}
	}
	cmp("name", o.Name, in.Name)
	cmp("description", o.Description, in.Description)
	cmp("kind", o.Kind, in.Kind)
	cmp("target", o.Target, in.Target)
	cmp("auth", maskDef(o).Auth, maskDef(in).Auth)
	cmp("options", o.Options, in.Options)
	cmp("readOnly", o.ReadOnly, in.ReadOnly)
	if credChanged && !inList(changed, "auth") {
		changed = append(changed, "auth")
	}
	sc := &storedConn{ID: id, Def: in, Secrets: secrets, CreatedBy: old.CreatedBy, CreatedAt: old.CreatedAt, UpdatedAt: ts(time.Now())}
	if err := s.saveConn(sc); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	s.pool.drop(id) // the connection changed: the next use reconnects
	s.schema.drop(id)
	if changed == nil {
		changed = []string{}
	}
	s.activity(c, "devtools.connection-updated", fmt.Sprintf("dev tools: connection %q updated (%s)", in.Name, strings.Join(changed, ", ")),
		map[string]any{"connectionId": id, "name": in.Name, "kind": in.Kind, "target": targetText(in.Target), "readOnly": in.ReadOnly, "changed": changed, "credentialChanged": credChanged})
	return s.view(manualDef(sc)), 200, nil
}

func (s *Service) handleDeleteConnection(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	id := r.PathValue("connectionId")
	if strings.HasPrefix(id, "p:") {
		if _, e := s.findConn(id); e != nil {
			return nil, 0, e
		}
		return nil, 0, conflict("%s is a preset of the deployment config: it is removed in the cloud (template / deployment editor)", id)
	}
	old, err := s.loadConn(id)
	if err != nil {
		return nil, 0, notFound("connection %s does not exist on site %s", id, s.h.Site())
	}
	if n := s.running.count(id); n > 0 {
		return nil, 0, conflict("%d executions are running on this connection; cancel them first", n)
	}
	if err := s.deleteConnRow(id); err != nil {
		return nil, 0, unavailable("delete: %s", err.Error())
	}
	s.pool.forget(id)
	s.schema.drop(id)
	s.mqtt.closeConn(id, "connection deleted")
	s.activity(c, "devtools.connection-deleted", fmt.Sprintf("dev tools: connection %q deleted", old.Def.Name),
		map[string]any{"connectionId": id, "name": old.Def.Name, "kind": old.Def.Kind})
	return map[string]bool{"deleted": true}, 200, nil
}

func (s *Service) handleDuplicate(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	if e := s.allow("write", c); e != nil {
		return nil, 0, e
	}
	var req struct {
		Name *string `json:"name"`
	}
	if e := readBody(r, &req, 64<<10); e != nil {
		return nil, 0, e
	}
	in := d.ConnectionInput
	in.Name = d.Name + " (copy)"
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		in.Name = strings.TrimSpace(*req.Name)
	}
	if n := []rune(in.Name); len(n) > 80 {
		in.Name = string(n[:80])
	}
	cleanInput(&in)
	secrets := map[string]string{}
	for k, v := range d.Secrets {
		secrets[k] = v
	}
	now := ts(time.Now())
	in = maskDef(in)
	sc := &storedConn{ID: "c_" + ulid.New(), Def: in, Secrets: secrets, CreatedBy: actorPtr(c), CreatedAt: now, UpdatedAt: now}
	if err := s.saveConn(sc); err != nil {
		return nil, 0, unavailable("save: %s", err.Error())
	}
	s.activity(c, "devtools.connection-created", fmt.Sprintf("dev tools: connection %q created from %q", in.Name, d.Name),
		map[string]any{"connectionId": sc.ID, "name": in.Name, "kind": in.Kind, "target": targetText(in.Target), "readOnly": in.ReadOnly, "duplicatedFrom": d.ID})
	return s.view(manualDef(sc)), 201, nil
}

func (s *Service) handleDisconnect(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	closed := s.pool.drop(d.ID)
	s.schema.drop(d.ID)
	return map[string]bool{"closed": closed}, 200, nil
}

// ───────────────────────── test

func (s *Service) testConn(ctx context.Context, c Caller, d *connDef) (TestConnectionResponse, *Error) {
	if e := s.allow("test", c); e != nil {
		return TestConnectionResponse{}, e
	}
	start := time.Now()
	out := TestConnectionResponse{}
	rc, e := s.resolve(ctx, d)
	if e != nil {
		out.Message = e.Msg
	} else {
		out.ResolvedTarget = strOrNil(rc.targetText)
		cl, err := s.connect(ctx, rc)
		if err != nil {
			out.Message = rc.masker.Mask(err.Error())
		} else {
			if err := s.ping(ctx, rc, cl); err != nil {
				out.Message = rc.masker.Mask(err.Error())
			} else {
				out.OK, out.Message = true, "connected"
				out.ServerVersion = strOrNil(cl.version)
			}
			cl.close()
		}
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	var cid any
	if d.ID != "" {
		cid = d.ID
	}
	var errText any
	if !out.OK {
		errText = LogError(out.Message, NewMasker(s.h.SecretValues()))
	}
	target := targetText(d.Target)
	if out.ResolvedTarget != nil {
		target = *out.ResolvedTarget
	}
	s.activity(c, "devtools.connection-tested", fmt.Sprintf("dev tools: test %q → %s", d.Name, map[bool]string{true: "ok", false: "failed"}[out.OK]),
		map[string]any{"connectionId": cid, "name": d.Name, "kind": d.Kind, "target": target, "ok": out.OK, "latencyMs": out.LatencyMs, "error": errText})
	return out, nil
}

func (s *Service) handleTestConnection(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	out, e := s.testConn(r.Context(), c, d)
	if e != nil {
		return nil, 0, e
	}
	if out.ResolvedTarget != nil {
		s.noteTarget(d.ID, *out.ResolvedTarget)
	}
	return out, 200, nil
}

func (s *Service) handleTestDraft(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var in ConnectionInput
	if e := readBody(r, &in, 1<<20); e != nil {
		return nil, 0, e
	}
	if e := s.checkInput(&in); e != nil {
		return nil, 0, e
	}
	// MASK in a draft = the stored literal of the connection being edited (?connectionId=)
	old := map[string]string{}
	if id := r.URL.Query().Get("connectionId"); id != "" && !strings.HasPrefix(id, "p:") {
		if sc, err := s.loadConn(id); err == nil {
			old = sc.Secrets
		}
	}
	secrets, _, e := applyLiterals(&in, old)
	if e != nil {
		return nil, 0, e
	}
	d := &connDef{ID: "", Source: "manual", ConnectionInput: in, Secrets: secrets}
	out, e := s.testConn(r.Context(), c, d)
	if e != nil {
		return nil, 0, e
	}
	return out, 200, nil
}

// ───────────────────────── reveal

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *Service) handleReveal(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	noStore(w)
	d, e := s.findConn(r.PathValue("connectionId"))
	if e != nil {
		return nil, 0, e
	}
	field := r.URL.Query().Get("field")
	if field == "" {
		field = "password"
	}
	if field != "password" && field != "token" {
		return nil, 0, invalid("field must be password | token")
	}
	cred := d.Auth.Password
	if field == "token" {
		cred = d.Auth.Token
	}
	if cred.Kind != "literal" {
		return nil, 0, invalid("the %s of %q is not a stored literal (expressions are revealed through the secret itself)", field, d.Name)
	}
	if e := s.allow("reveal", c); e != nil {
		return nil, 0, e
	}
	v, ok := d.Secrets[field]
	if !ok {
		return nil, 0, notFound("no stored %s", field)
	}
	s.activity(c, "devtools.credential-revealed", fmt.Sprintf("dev tools: %s of connection %q revealed", field, d.Name),
		map[string]any{"connectionId": d.ID, "name": d.Name, "field": field})
	return RevealResponse{Value: v, HideAfterSec: model.RevealSeconds}, 200, nil
}
