package devtools

// Connection definitions (presets of the head + manual rows) and their resolution on the site: target address
// (container IP on the site network, else the published loopback port), SQLite files (volume / container mount, never
// through a symlink) and credentials (expressions resolved from the head config, literals from the store).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/expr"
	"opm/agent/internal/model"
)

// connDef is a connection definition with its real literal credentials.
type connDef struct {
	ID           string
	Source       string // preset | manual
	PresetRevSeq *int
	ConnectionInput
	Secrets   map[string]string // literal password / token (manual: the store; preset: the config's static value, §21 #103)
	CreatedBy *string
	CreatedAt *string
	UpdatedAt *string
}

func presetID(slug string) string { return "p:" + slug }

func presetDef(p model.DevToolPreset, seq int) *connDef {
	q := seq
	// §21 #103: a preset may hold a static value (literal) — it lives in the config; here it is treated like a manual
	// connection's stored literal: every API read returns MASK (maskedAuth), Reveal returns it, Duplicate copies it, and the
	// resolved value joins the activity / log redaction set like any credential.
	secrets := map[string]string{}
	if p.Auth.Password.Kind == "literal" && p.Auth.Password.Value != nil {
		secrets["password"] = *p.Auth.Password.Value
	}
	if p.Auth.Token.Kind == "literal" && p.Auth.Token.Value != nil {
		secrets["token"] = *p.Auth.Token.Value
	}
	return &connDef{ID: presetID(p.PresetSlug), Source: "preset", PresetRevSeq: &q, Secrets: secrets,
		ConnectionInput: ConnectionInput{Name: p.Name, Description: p.Description, Kind: p.Kind, Target: p.Target, Auth: p.Auth, Options: p.Options, ReadOnly: p.ReadOnly}}
}

func manualDef(c *storedConn) *connDef {
	ca, ua := c.CreatedAt, c.UpdatedAt
	return &connDef{ID: c.ID, Source: "manual", ConnectionInput: c.Def, Secrets: c.Secrets, CreatedBy: c.CreatedBy, CreatedAt: &ca, UpdatedAt: &ua}
}

// findConn returns the definition of a connection id (p:<presetSlug> | c_<ULID>).
func (s *Service) findConn(id string) (*connDef, *Error) {
	if strings.HasPrefix(id, "p:") {
		h := s.h.Head()
		if h != nil {
			for _, p := range h.Presets {
				if presetID(p.PresetSlug) == id {
					return presetDef(p, h.Seq), nil
				}
			}
		}
		return nil, notFound("preset %s is not in the head revision of site %s", strings.TrimPrefix(id, "p:"), s.h.Site())
	}
	c, err := s.loadConn(id)
	if err != nil {
		return nil, notFound("connection %s does not exist on site %s", id, s.h.Site())
	}
	return manualDef(c), nil
}

// resolvedConn is a connection ready to dial.
type resolvedConn struct {
	def          *connDef
	host         string
	port         int
	addr         string // host:port (network kinds)
	baseURL      string // http
	file         string // sqlite host path
	root         string // sqlite: ATTACH stays inside this directory
	user         string
	password     string
	database     string
	clientID     string
	token        string
	apiKeyHeader string
	headers      [][2]string
	masker       *Masker
	targetText   string
	fingerprint  string
}

func (rc *resolvedConn) timeout(def int) time.Duration {
	if rc.def.Options.TimeoutSec != nil && *rc.def.Options.TimeoutSec >= 1 {
		return time.Duration(*rc.def.Options.TimeoutSec) * time.Second
	}
	return time.Duration(def) * time.Second
}

// resolveValue resolves a value with {{ }} on the site (plain text stays). secret = the result came from a secret.
func (s *Service) resolveValue(src, what string) (string, bool, *Error) {
	if !expr.IsExpr(src) {
		return src, false, nil
	}
	if s.h.Head() == nil {
		return "", false, invalid("%s uses an expression but site %s has no head revision yet", what, s.h.Site())
	}
	r, err := s.h.ResolveExpr(src)
	if err != nil {
		e := &Error{Status: 422, Code: model.ErrInvalid, Msg: fmt.Sprintf("%s: %s", what, err.Error())}
		if xe, ok := err.(*expr.Error); ok {
			e.Details = map[string]any{"expression": src, "code": xe.Code, "pos": xe.Pos}
			e.Msg = fmt.Sprintf("%s: %s (%s)", what, xe.Message, xe.Code)
		}
		return "", false, e
	}
	if r.Deferred {
		return "", false, invalid("%s: a required variable / secret has no value yet", what)
	}
	return r.Value, r.Secret, nil
}

func (s *Service) resolveCred(d *connDef, c model.DevToolCredential, field string) (string, *Error) {
	switch c.Kind {
	case "", "none":
		return "", nil
	case "expr":
		v, _, e := s.resolveValue(model.Str(c.Expr), field)
		return v, e
	case "literal": // manual: stored on the agent; preset (§21 #103): the static value of the config (presetDef)
		return d.Secrets[field], nil
	}
	return "", invalid("unknown credential kind %q", c.Kind)
}

// resolve turns a definition into an address + credentials (no network I/O except the container reachability probe).
func (s *Service) resolve(ctx context.Context, d *connDef) (*resolvedConn, *Error) {
	rc := &resolvedConn{def: d}
	secretVals := []string{}
	str := func(p *string, what string) (string, *Error) {
		if p == nil {
			return "", nil
		}
		v, sec, e := s.resolveValue(*p, what)
		if sec {
			secretVals = append(secretVals, v)
		}
		return v, e
	}
	var e *Error
	if rc.user, e = str(d.Auth.Username, "username"); e != nil {
		return nil, e
	}
	if rc.database, e = str(d.Auth.Database, "database"); e != nil {
		return nil, e
	}
	if rc.clientID, e = str(d.Auth.ClientID, "clientId"); e != nil {
		return nil, e
	}
	if rc.apiKeyHeader, e = str(d.Auth.APIKeyHeader, "apiKeyHeader"); e != nil {
		return nil, e
	}
	if rc.password, e = s.resolveCred(d, d.Auth.Password, "password"); e != nil {
		return nil, e
	}
	if rc.token, e = s.resolveCred(d, d.Auth.Token, "token"); e != nil {
		return nil, e
	}
	for _, hdr := range d.Options.DefaultHeaders {
		v, e := str(&hdr.Value, "header "+hdr.Name)
		if e != nil {
			return nil, e
		}
		rc.headers = append(rc.headers, [2]string{hdr.Name, v})
	}
	secretVals = append(secretVals, rc.password, rc.token)
	rc.masker = NewMasker(s.h.SecretValues(), secretVals)
	if e := s.resolveTarget(ctx, rc); e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(jsonStr(map[string]any{"def": d.ConnectionInput, "addr": rc.addr, "url": rc.baseURL, "file": rc.file,
		"u": rc.user, "p": rc.password, "db": rc.database, "cid": rc.clientID, "t": rc.token, "h": rc.headers})))
	rc.fingerprint = hex.EncodeToString(sum[:])
	return rc, nil
}

func (s *Service) headContainer(slug string) (*HeadContainer, *Error) {
	h := s.h.Head()
	if h == nil {
		return nil, invalid("site %s has no head revision yet: container targets need one", s.h.Site())
	}
	for i := range h.Containers {
		if h.Containers[i].ContainerSlug == slug {
			return &h.Containers[i], nil
		}
	}
	return nil, invalid("container %s does not exist on site %s", slug, s.h.Site())
}

func (s *Service) resolveTarget(ctx context.Context, rc *resolvedConn) *Error {
	d := rc.def
	t := d.Target
	if allowed := model.DevToolTargets[d.Kind]; !inList(allowed, t.Kind) {
		return invalid("a %s connection cannot use a %q target", d.Kind, t.Kind)
	}
	scheme := "http"
	if d.Options.Scheme != nil && *d.Options.Scheme != "" {
		scheme = *d.Options.Scheme
	} else if d.Options.TLS {
		scheme = "https"
	}
	basePath := strings.TrimRight(model.Str(d.Options.BasePath), "/")
	switch t.Kind {
	case "container":
		hc, e := s.headContainer(model.Str(t.ContainerSlug))
		if e != nil {
			return e
		}
		var port *HeadPort
		for i := range hc.Ports {
			if hc.Ports[i].Name == model.Str(t.PortName) {
				port = &hc.Ports[i]
			}
		}
		if port == nil {
			return invalid("container %s has no port %s", hc.ContainerSlug, model.Str(t.PortName))
		}
		addr, text, e := s.containerAddr(ctx, hc, port)
		if e != nil {
			return e
		}
		rc.addr, rc.targetText = addr, text
		rc.host, rc.port = splitHostPort(addr)
		if d.Kind == "http" {
			rc.baseURL = scheme + "://" + addr + basePath
		}
	case "host":
		h := strings.TrimSpace(model.Str(t.Host))
		if h == "" || t.Port == nil || *t.Port < 1 || *t.Port > 65535 {
			return invalid("host target needs a host and a port 1–65535")
		}
		rc.host, rc.port = h, int(*t.Port)
		rc.addr = net.JoinHostPort(h, strconv.Itoa(rc.port))
		rc.targetText = rc.addr
		if d.Kind == "http" {
			rc.baseURL = scheme + "://" + rc.addr + basePath
		}
	case "url":
		u, sec, e := s.resolveValue(strings.TrimSpace(model.Str(t.URL)), "url")
		if e != nil {
			return e
		}
		if sec {
			rc.masker = rc.masker.Add(u)
		}
		if !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			return invalid("url must be an absolute http:// or https:// URL")
		}
		rc.baseURL = strings.TrimRight(u, "/") + basePath
		rc.targetText = rc.masker.Mask(u)
	case "volume-file":
		var vol *HeadVolume
		if h := s.h.Head(); h != nil {
			for i := range h.Volumes {
				if h.Volumes[i].Slug == model.Str(t.VolumeSlug) {
					vol = &h.Volumes[i]
				}
			}
		}
		if vol == nil {
			return invalid("volume %s is not declared on site %s", model.Str(t.VolumeSlug), s.h.Site())
		}
		if vol.Kind == "file" {
			rc.file, rc.root = vol.Path, filepath.Dir(vol.Path)
		} else {
			p := model.Str(t.Path)
			if !safeRel(p) {
				return invalid("the SQLite file must be a relative path inside the volume (no .., no leading /)")
			}
			f, e := underRoot(vol.Path, p)
			if e != nil {
				return e
			}
			rc.file, rc.root = f, vol.Path
		}
		rc.targetText = fmt.Sprintf("%s (volume %s)", model.Str(t.Path), vol.Slug)
	case "container-file":
		hc, e := s.headContainer(model.Str(t.ContainerSlug))
		if e != nil {
			return e
		}
		p := filepath.Clean(model.Str(t.Path))
		if !strings.HasPrefix(p, "/") || p == "/" {
			return invalid("the SQLite file must be an absolute path inside the container")
		}
		var best *HeadMount
		for i := range hc.Mounts {
			m := &hc.Mounts[i]
			tg := filepath.Clean(m.Target)
			if (p == tg || strings.HasPrefix(p, tg+"/")) && (best == nil || len(tg) > len(filepath.Clean(best.Target))) {
				best = m
			}
		}
		if best == nil || best.Kind == "secret" {
			return invalid("%s is not on a volume / host mount of container %s (container-local storage cannot be opened from outside)", p, hc.ContainerSlug)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, filepath.Clean(best.Target)), "/")
		if rel == "" {
			rc.file, rc.root = best.Source, filepath.Dir(best.Source)
		} else {
			f, e := underRoot(best.Source, rel)
			if e != nil {
				return e
			}
			rc.file, rc.root = f, best.Source
		}
		rc.targetText = fmt.Sprintf("%s:%s", hc.ContainerSlug, p)
	}
	return nil
}

func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// underRoot joins a relative path under root and refuses any symlink on the way (the root itself may be one).
func underRoot(root, rel string) (string, *Error) {
	if !safeRel(rel) {
		return "", invalid("%q is not a safe relative path", rel)
	}
	cur := root
	for _, seg := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return "", invalid("%s does not exist", rel)
			}
			return "", invalid("%s: %s", rel, err.Error())
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", invalid("%s goes through a symlink (refused)", rel)
		}
	}
	return cur, nil
}

func splitHostPort(addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// reach caches the reachability of container IPs from the agent (Colima / Docker Desktop: not routable).
var reach = struct {
	sync.Mutex
	m map[string]reachEntry
}{m: map[string]reachEntry{}}

type reachEntry struct {
	ok bool
	at time.Time
}

func probe(ctx context.Context, addr string) bool {
	reach.Lock()
	if e, ok := reach.m[addr]; ok && time.Since(e.at) < time.Minute {
		reach.Unlock()
		return e.ok
	}
	reach.Unlock()
	d := net.Dialer{Timeout: 600 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", addr)
	ok := err == nil
	if c != nil {
		c.Close()
	}
	reach.Lock()
	reach.m[addr] = reachEntry{ok: ok, at: time.Now()}
	reach.Unlock()
	return ok
}

// containerAddr: the container IP on its site network when it answers, else the published host port on loopback.
func (s *Service) containerAddr(ctx context.Context, hc *HeadContainer, p *HeadPort) (string, string, *Error) {
	if hc.DockerID == "" {
		return "", "", unavailable("container %s is not created on site %s", hc.ContainerSlug, s.h.Site())
	}
	dc := s.h.Docker()
	if dc == nil {
		return "", "", unavailable("docker is not available")
	}
	cj, err := dc.ContainerInspect(ctx, hc.DockerID)
	if err != nil {
		return "", "", unavailable("docker inspect %s: %s", hc.ContainerSlug, err.Error())
	}
	if !cj.State.Running {
		return "", "", unavailable("container %s is not running (%s)", hc.ContainerSlug, cj.State.Status)
	}
	proto := p.Protocol
	if proto == "" {
		proto = "tcp"
	}
	ip := ""
	if ep, ok := cj.NetworkSettings.Networks[hc.Network]; ok && ep.IPAddress != "" {
		ip = ep.IPAddress
	}
	if ip == "" {
		for _, ep := range cj.NetworkSettings.Networks {
			if ep.IPAddress != "" {
				ip = ep.IPAddress
				break
			}
		}
	}
	direct := ""
	if ip != "" {
		direct = net.JoinHostPort(ip, strconv.Itoa(p.ContainerPort))
		if probe(ctx, direct) {
			return direct, fmt.Sprintf("%s → %s", hc.ContainerSlug, direct), nil
		}
	}
	for _, b := range cj.NetworkSettings.Ports[fmt.Sprintf("%d/%s", p.ContainerPort, proto)] {
		if b.HostPort == "" {
			continue
		}
		hip := b.HostIP
		if hip == "" || hip == "0.0.0.0" || hip == "::" {
			hip = "127.0.0.1"
		}
		a := net.JoinHostPort(hip, b.HostPort)
		return a, fmt.Sprintf("%s → %s (published port; the container IP is not reachable from the agent)", hc.ContainerSlug, a), nil
	}
	if direct != "" {
		return direct, fmt.Sprintf("%s → %s", hc.ContainerSlug, direct), nil
	}
	return "", "", unavailable("container %s has no address on the site network and port %s is not published", hc.ContainerSlug, p.Name)
}

// ContainerPortAddr is the TCP address of a container port (by port name) as the agent reaches it: the container IP
// on its site network when it answers, else the published host port (§21 #125 port tunnels).
func (s *Service) ContainerPortAddr(ctx context.Context, containerSlug, portName string) (string, error) {
	hc, e := s.headContainer(containerSlug)
	if e != nil {
		return "", e
	}
	for i := range hc.Ports {
		if hc.Ports[i].Name == portName {
			addr, _, e := s.containerAddr(ctx, hc, &hc.Ports[i])
			if e != nil {
				return "", e
			}
			return addr, nil
		}
	}
	return "", invalid("container %s has no port %s", containerSlug, portName)
}
