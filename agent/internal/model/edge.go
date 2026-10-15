package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ───────────────────────── round E edge (TS Edge, EdgeHost, EdgeRoute, EdgeMatch, EdgeRouteAction, EdgeResponseHeaders)
//
// Decoding normalizes legacy shapes exactly like TS normalizeEdge (validate.ts): match {pathPrefix} → kind prefix;
// proxy timeoutSec → readTimeoutSec (connect / send 60); missing round E fields → defaults (websocket missing → true,
// otherwise KEPT as stored); route responseHeaders missing → null; host responseHeaders missing → empty; edge
// maxBodyBytes missing → null. Every reader sees the round E shape.
//
// Revision hashes: Edge keeps the raw JSON it was decoded from. MarshalJSON re-emits those exact bytes as long as the
// (normalized) content was not changed since decoding, so already-published revisions in the legacy shape (or any other
// shape) keep their hash. A changed edge, or one built in Go, is emitted in the round E shape. Normalized() drops the raw
// bytes (GUI config views).

// HeaderKV is one entry of a HeaderMap.
type HeaderKV struct {
	Name  string
	Value string
}

// HeaderMap is TS Record<string, Value> for header names: a JSON object whose key order is kept (TS Object.entries
// order matters for the request header trace and for the order headers are applied).
type HeaderMap []HeaderKV

// MarshalJSON emits an object in order (nil → {}).
func (m HeaderMap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv.Name)
		v, _ := json.Marshal(kv.Value)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads an object keeping the key order (a repeated key keeps its first position and the last value, as
// JSON.parse). null → empty.
func (m *HeaderMap) UnmarshalJSON(b []byte) error {
	*m = HeaderMap{}
	if isNull(b) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("headers: want an object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := kt.(string)
		var v string
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("header %s: %w", k, err)
		}
		m.Set(k, v)
	}
	_, err = dec.Token()
	return err
}

// Set sets name (exact key) to value, keeping the position of an existing key.
func (m *HeaderMap) Set(name, value string) {
	for i := range *m {
		if (*m)[i].Name == name {
			(*m)[i].Value = value
			return
		}
	}
	*m = append(*m, HeaderKV{name, value})
}

// Get returns the value of the exact key.
func (m HeaderMap) Get(name string) (string, bool) {
	for _, kv := range m {
		if kv.Name == name {
			return kv.Value, true
		}
	}
	return "", false
}

// Keys returns the names in order.
func (m HeaderMap) Keys() []string {
	out := make([]string, 0, len(m))
	for _, kv := range m {
		out = append(out, kv.Name)
	}
	return out
}

// Clone returns a copy.
func (m HeaderMap) Clone() HeaderMap {
	if m == nil {
		return HeaderMap{}
	}
	return append(HeaderMap{}, m...)
}

// MapOf builds a HeaderMap from pairs: MapOf("A", "1", "B", "2").
func MapOf(kv ...string) HeaderMap {
	m := HeaderMap{}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Set(kv[i], kv[i+1])
	}
	return m
}

// EdgeResponseHeaders is TS EdgeResponseHeaders (nginx add_header / proxy_hide_header).
type EdgeResponseHeaders struct {
	Set    HeaderMap `json:"set"`
	Add    HeaderMap `json:"add"`
	Remove []string  `json:"remove"`
	Always []string  `json:"always"`
}

// EmptyResponseHeaders is TS emptyResponseHeaders().
func EmptyResponseHeaders() EdgeResponseHeaders {
	return EdgeResponseHeaders{Set: HeaderMap{}, Add: HeaderMap{}, Remove: []string{}, Always: []string{}}
}

// IsEmpty is TS isEmptyResponseHeaders (always alone does not count).
func (h *EdgeResponseHeaders) IsEmpty() bool {
	return h == nil || (len(h.Set) == 0 && len(h.Add) == 0 && len(h.Remove) == 0)
}

// Clone returns a deep copy.
func (h EdgeResponseHeaders) Clone() EdgeResponseHeaders {
	return EdgeResponseHeaders{Set: h.Set.Clone(), Add: h.Add.Clone(), Remove: append([]string{}, h.Remove...), Always: append([]string{}, h.Always...)}
}

// MarshalJSON emits [] for nil lists.
func (h EdgeResponseHeaders) MarshalJSON() ([]byte, error) {
	type alias EdgeResponseHeaders
	c := alias(h)
	if c.Remove == nil {
		c.Remove = []string{}
	}
	if c.Always == nil {
		c.Always = []string{}
	}
	if c.Set == nil {
		c.Set = HeaderMap{}
	}
	if c.Add == nil {
		c.Add = HeaderMap{}
	}
	return json.Marshal(c)
}

// UnmarshalJSON fills missing parts with empty values (TS normalizeEdge rh()).
func (h *EdgeResponseHeaders) UnmarshalJSON(b []byte) error {
	type alias EdgeResponseHeaders
	var w alias
	if !isNull(b) {
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
	}
	*h = EdgeResponseHeaders(w)
	if h.Set == nil {
		h.Set = HeaderMap{}
	}
	if h.Add == nil {
		h.Add = HeaderMap{}
	}
	if h.Remove == nil {
		h.Remove = []string{}
	}
	if h.Always == nil {
		h.Always = []string{}
	}
	return nil
}

// EdgeHost is TS EdgeHost.
type EdgeHost struct {
	Hostname            string              `json:"hostname"`
	TLS                 EdgeTls             `json:"tls"`
	RedirectHTTPToHTTPS bool                `json:"redirectHttpToHttps"`
	ResponseHeaders     EdgeResponseHeaders `json:"responseHeaders"` // round E: nginx server-level add_header (missing → empty)
}

// UnmarshalJSON defaults a missing responseHeaders to empty.
func (h *EdgeHost) UnmarshalJSON(b []byte) error {
	type alias EdgeHost
	var w alias
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*h = EdgeHost(w)
	if h.ResponseHeaders.Set == nil {
		h.ResponseHeaders = EmptyResponseHeaders()
	}
	return nil
}

// EdgeMatch is TS EdgeMatch (round E). Legacy {pathPrefix, methods} decodes as kind prefix.
type EdgeMatch struct {
	Kind      string   `json:"kind"` // prefix | exact | regex
	Path      string   `json:"path"`
	StopRegex bool     `json:"stopRegex"` // prefix only (nginx ^~)
	Methods   []string `json:"methods"`   // nil = all
}

// UnmarshalJSON normalizes like TS normalizeEdge.
func (m *EdgeMatch) UnmarshalJSON(b []byte) error {
	var w struct {
		Kind       *string  `json:"kind"`
		Path       *string  `json:"path"`
		PathPrefix *string  `json:"pathPrefix"`
		StopRegex  *bool    `json:"stopRegex"`
		Methods    []string `json:"methods"`
	}
	if !isNull(b) {
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
	}
	*m = EdgeMatch{Kind: "prefix", Path: "/"}
	if w.Kind != nil {
		m.Kind = *w.Kind
	}
	if w.Path != nil {
		m.Path = *w.Path
	} else if w.PathPrefix != nil {
		m.Path = *w.PathPrefix
	}
	if m.Kind == "prefix" && w.StopRegex != nil {
		m.StopRegex = *w.StopRegex
	}
	if len(w.Methods) > 0 {
		m.Methods = w.Methods
	}
	return nil
}

// EdgeRewrite is TS EdgeRewrite (nginx `rewrite <regex> <replacement>`).
type EdgeRewrite struct {
	Regex       string `json:"regex"`
	Replacement string `json:"replacement"`
}

// Round E proxy defaults (TS EDGE_DEFAULTS / newProxyAction).
const (
	DefaultConnectTimeoutSec = 60
	DefaultSendTimeoutSec    = 60
	DefaultReadTimeoutSec    = 60
)

// EdgeRouteAction is the TS union EdgeProxyAction | EdgeRedirectAction | EdgeRewriteAction.
type EdgeRouteAction struct {
	Type string // proxy | redirect | rewrite
	// proxy
	ContainerSlug     string
	PortName          string
	StripPrefix       bool
	Rewrite           *EdgeRewrite // nginx rewrite … break; exclusive with StripPrefix
	AddPrefix         *string
	SetHeaders        HeaderMap
	RemoveHeaders     []string
	Websocket         bool
	ConnectTimeoutSec float64
	SendTimeoutSec    float64
	ReadTimeoutSec    float64
	RequestBuffering  bool
	ResponseBuffering bool
	MaxBodyBytes      *int64 // nil = inherit the edge; 0 = unlimited
	// redirect
	To       string
	Status   int // 301 | 302 | 303 | 307 | 308
	KeepPath bool
	// rewrite (internal, nginx rewrite … last); Regex + Replacement
	Regex       string
	Replacement string
}

// NewProxyAction is TS newProxyAction (round E defaults).
func NewProxyAction(containerSlug, portName string) EdgeRouteAction {
	return EdgeRouteAction{Type: "proxy", ContainerSlug: containerSlug, PortName: portName, SetHeaders: HeaderMap{}, RemoveHeaders: []string{},
		Websocket: true, ConnectTimeoutSec: DefaultConnectTimeoutSec, SendTimeoutSec: DefaultSendTimeoutSec, ReadTimeoutSec: DefaultReadTimeoutSec}
}

type proxyWire struct {
	Type              string       `json:"type"`
	ContainerSlug     string       `json:"containerSlug"`
	PortName          string       `json:"portName"`
	StripPrefix       bool         `json:"stripPrefix"`
	Rewrite           *EdgeRewrite `json:"rewrite"`
	AddPrefix         *string      `json:"addPrefix"`
	SetHeaders        HeaderMap    `json:"setHeaders"`
	RemoveHeaders     []string     `json:"removeHeaders"`
	Websocket         bool         `json:"websocket"`
	ConnectTimeoutSec float64      `json:"connectTimeoutSec"`
	SendTimeoutSec    float64      `json:"sendTimeoutSec"`
	ReadTimeoutSec    float64      `json:"readTimeoutSec"`
	RequestBuffering  bool         `json:"requestBuffering"`
	ResponseBuffering bool         `json:"responseBuffering"`
	MaxBodyBytes      *int64       `json:"maxBodyBytes"`
}
type proxyWireIn struct {
	Type              string       `json:"type"`
	ContainerSlug     *string      `json:"containerSlug"`
	ServiceSlug       *string      `json:"serviceSlug"` // pre-round-C
	PortName          string       `json:"portName"`
	StripPrefix       bool         `json:"stripPrefix"`
	Rewrite           *EdgeRewrite `json:"rewrite"`
	AddPrefix         *string      `json:"addPrefix"`
	SetHeaders        HeaderMap    `json:"setHeaders"`
	RemoveHeaders     []string     `json:"removeHeaders"`
	Websocket         *bool        `json:"websocket"`
	TimeoutSec        *float64     `json:"timeoutSec"` // legacy → readTimeoutSec
	ConnectTimeoutSec *float64     `json:"connectTimeoutSec"`
	SendTimeoutSec    *float64     `json:"sendTimeoutSec"`
	ReadTimeoutSec    *float64     `json:"readTimeoutSec"`
	RequestBuffering  bool         `json:"requestBuffering"`
	ResponseBuffering bool         `json:"responseBuffering"`
	MaxBodyBytes      *int64       `json:"maxBodyBytes"`
}
type redirectWire struct {
	Type     string `json:"type"`
	To       string `json:"to"`
	Status   int    `json:"status"`
	KeepPath bool   `json:"keepPath"`
}
type rewriteWire struct {
	Type        string `json:"type"`
	Regex       string `json:"regex"`
	Replacement string `json:"replacement"`
}

// MarshalJSON emits exactly the fields of the action type (round E shape).
func (a EdgeRouteAction) MarshalJSON() ([]byte, error) {
	switch a.Type {
	case "proxy":
		sh, rh := a.SetHeaders, a.RemoveHeaders
		if sh == nil {
			sh = HeaderMap{}
		}
		if rh == nil {
			rh = []string{}
		}
		return json.Marshal(proxyWire{a.Type, a.ContainerSlug, a.PortName, a.StripPrefix, a.Rewrite, a.AddPrefix, sh, rh, a.Websocket,
			a.ConnectTimeoutSec, a.SendTimeoutSec, a.ReadTimeoutSec, a.RequestBuffering, a.ResponseBuffering, a.MaxBodyBytes})
	case "redirect":
		return json.Marshal(redirectWire{a.Type, a.To, a.Status, a.KeepPath})
	case "rewrite":
		return json.Marshal(rewriteWire{a.Type, a.Regex, a.Replacement})
	}
	return nil, fmt.Errorf("route action: unknown type %q", a.Type)
}

// UnmarshalJSON rejects unknown types and normalizes legacy proxy actions (TS normalizeEdge).
func (a *EdgeRouteAction) UnmarshalJSON(b []byte) error {
	var t struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	switch t.Type {
	case "proxy":
		var w proxyWireIn
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		x := NewProxyAction("", w.PortName)
		if w.ContainerSlug != nil {
			x.ContainerSlug = *w.ContainerSlug
		} else if w.ServiceSlug != nil {
			x.ContainerSlug = *w.ServiceSlug
		}
		x.StripPrefix, x.Rewrite, x.AddPrefix = w.StripPrefix, w.Rewrite, w.AddPrefix
		if w.SetHeaders != nil {
			x.SetHeaders = w.SetHeaders
		}
		if w.RemoveHeaders != nil {
			x.RemoveHeaders = w.RemoveHeaders
		}
		if w.Websocket != nil {
			x.Websocket = *w.Websocket
		}
		if w.ConnectTimeoutSec != nil {
			x.ConnectTimeoutSec = *w.ConnectTimeoutSec
		}
		if w.SendTimeoutSec != nil {
			x.SendTimeoutSec = *w.SendTimeoutSec
		}
		if w.ReadTimeoutSec != nil {
			x.ReadTimeoutSec = *w.ReadTimeoutSec
		} else if w.TimeoutSec != nil {
			x.ReadTimeoutSec = *w.TimeoutSec
		}
		x.RequestBuffering, x.ResponseBuffering, x.MaxBodyBytes = w.RequestBuffering, w.ResponseBuffering, w.MaxBodyBytes
		*a = x
	case "redirect":
		var w redirectWire
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		*a = EdgeRouteAction{Type: w.Type, To: w.To, Status: w.Status, KeepPath: w.KeepPath}
	case "rewrite":
		var w rewriteWire
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		*a = EdgeRouteAction{Type: w.Type, Regex: w.Regex, Replacement: w.Replacement}
	default:
		return fmt.Errorf("route action: unknown type %q", t.Type)
	}
	return nil
}

// EdgeRoute is TS EdgeRoute.
type EdgeRoute struct {
	RouteID Slug            `json:"routeId"`
	Hosts   []string        `json:"hosts"`
	Match   EdgeMatch       `json:"match"`
	Action  EdgeRouteAction `json:"action"`
	Order   int             `json:"order"`
	// ResponseHeaders: nil = inherit the host's; non-nil REPLACES them (nginx add_header inheritance). Missing → nil.
	ResponseHeaders *EdgeResponseHeaders `json:"responseHeaders"`
}

// UnmarshalJSON defaults hosts to [].
func (r *EdgeRoute) UnmarshalJSON(b []byte) error {
	type alias EdgeRoute
	var w alias
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = EdgeRoute(w)
	if r.Hosts == nil {
		r.Hosts = []string{}
	}
	return nil
}

// MarshalJSON emits [] for nil hosts.
func (r EdgeRoute) MarshalJSON() ([]byte, error) {
	type alias EdgeRoute
	c := alias(r)
	if c.Hosts == nil {
		c.Hosts = []string{}
	}
	return json.Marshal(c)
}

// Edge is TS Edge (round E). See the file comment for the raw-bytes rule that keeps revision hashes.
type Edge struct {
	HTTPPort     int         `json:"httpPort"`
	HTTPSPort    *int        `json:"httpsPort"`
	MaxBodyBytes *int64      `json:"maxBodyBytes"` // nil = unlimited (nginx client_max_body_size)
	Hosts        []EdgeHost  `json:"hosts"`
	Routes       []EdgeRoute `json:"routes"`

	raw  []byte // the JSON this edge was decoded from
	norm []byte // its round E form at decode time
}

func (e Edge) marshalNew() ([]byte, error) {
	type wire struct {
		HTTPPort     int         `json:"httpPort"`
		HTTPSPort    *int        `json:"httpsPort"`
		MaxBodyBytes *int64      `json:"maxBodyBytes"`
		Hosts        []EdgeHost  `json:"hosts"`
		Routes       []EdgeRoute `json:"routes"`
	}
	w := wire{e.HTTPPort, e.HTTPSPort, e.MaxBodyBytes, e.Hosts, e.Routes}
	if w.Hosts == nil {
		w.Hosts = []EdgeHost{}
	}
	if w.Routes == nil {
		w.Routes = []EdgeRoute{}
	}
	return json.Marshal(w)
}

// MarshalJSON re-emits the decoded bytes when the content is unchanged (hash round trip), else the round E shape.
func (e Edge) MarshalJSON() ([]byte, error) {
	cur, err := e.marshalNew()
	if err != nil {
		return nil, err
	}
	if e.raw != nil && bytes.Equal(cur, e.norm) {
		return e.raw, nil
	}
	return cur, nil
}

// UnmarshalJSON normalizes (TS normalizeEdge) and remembers the raw bytes.
func (e *Edge) UnmarshalJSON(b []byte) error {
	var w struct {
		HTTPPort     *int            `json:"httpPort"`
		HTTPSPort    json.RawMessage `json:"httpsPort"`
		MaxBodyBytes *int64          `json:"maxBodyBytes"`
		Hosts        *[]EdgeHost     `json:"hosts"`
		Routes       []EdgeRoute     `json:"routes"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	out := Edge{HTTPPort: 80, MaxBodyBytes: w.MaxBodyBytes, Routes: w.Routes}
	if w.HTTPPort != nil {
		out.HTTPPort = *w.HTTPPort
	}
	if w.HTTPSPort == nil {
		p := 443
		out.HTTPSPort = &p
	} else if !isNull(w.HTTPSPort) {
		var p int
		if err := json.Unmarshal(w.HTTPSPort, &p); err != nil {
			return fmt.Errorf("httpsPort: %w", err)
		}
		out.HTTPSPort = &p
	}
	if w.Hosts != nil {
		out.Hosts = *w.Hosts
	} else {
		out.Hosts = []EdgeHost{{Hostname: "*", TLS: EdgeTls{Mode: "none"}, ResponseHeaders: EmptyResponseHeaders()}}
	}
	if out.Hosts == nil {
		out.Hosts = []EdgeHost{}
	}
	if out.Routes == nil {
		out.Routes = []EdgeRoute{}
	}
	norm, err := out.marshalNew()
	if err != nil {
		return err
	}
	out.raw = append([]byte(nil), bytes.TrimSpace(b)...)
	out.norm = norm
	*e = out
	return nil
}

// Normalized returns the edge without its decoded bytes (always marshals in the round E shape).
func (e Edge) Normalized() Edge {
	e.raw, e.norm = nil, nil
	return e
}

// IsLegacyShape reports whether the decoded JSON was not in the round E shape (informational).
func (e Edge) IsLegacyShape() bool {
	return e.raw != nil && strings.Contains(string(e.raw), `"pathPrefix"`)
}
