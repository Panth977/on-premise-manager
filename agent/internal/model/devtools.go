package model

// §21 #97 dev tools: Go mirror of the preset shapes of shared/src/devtools.ts (SiteConfig.devTools). The API shapes
// of the agent dev tools live in internal/devtools.

import (
	"bytes"
	"encoding/json"
)

// DevToolKinds is TS DEVTOOL_KINDS.
var DevToolKinds = []string{"http", "postgres", "sqlite", "cassandra", "redis", "mqtt"}

// DevToolTargets is TS DEVTOOL_TARGETS (allowed target kinds per connection kind).
var DevToolTargets = map[string][]string{
	"http":      {"container", "host", "url"},
	"postgres":  {"container", "host"},
	"cassandra": {"container", "host"},
	"redis":     {"container", "host"},
	"mqtt":      {"container", "host"},
	"sqlite":    {"volume-file", "container-file"},
}

// DevToolDefaultPort is TS DEVTOOL_DEFAULT_PORT.
var DevToolDefaultPort = map[string]int{"http": 80, "postgres": 5432, "cassandra": 9042, "redis": 6379, "mqtt": 1883}

// DevToolPresetsMax is TS DEVTOOL_PRESETS_MAX.
const DevToolPresetsMax = 50

// DevToolTarget is the TS union DevToolTarget (tagged by kind). Only the keys of the kind are set (nil = absent):
//
//	container      containerSlug, portName
//	host           host, port
//	url            url
//	volume-file    volumeSlug, path
//	container-file containerSlug, path
type DevToolTarget struct {
	Kind          string   `json:"kind"`
	ContainerSlug *string  `json:"containerSlug,omitempty"`
	PortName      *string  `json:"portName,omitempty"`
	Host          *string  `json:"host,omitempty"`
	Port          *float64 `json:"port,omitempty"`
	URL           *string  `json:"url,omitempty"`
	VolumeSlug    *string  `json:"volumeSlug,omitempty"`
	Path          *string  `json:"path,omitempty"`
}

// Str returns *p or "".
func Str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DevToolCredential is the TS union DevToolCredential: none | expr {expr} | literal {value} (value MASK in reads).
type DevToolCredential struct {
	Kind  string  `json:"kind"`
	Expr  *string `json:"expr,omitempty"`
	Value *string `json:"value,omitempty"`
}

// DevToolAuth is TS DevToolAuth.
type DevToolAuth struct {
	Username     *string           `json:"username"`
	Password     DevToolCredential `json:"password"`
	Database     *string           `json:"database"`
	ClientID     *string           `json:"clientId"`
	HTTPScheme   string            `json:"httpScheme"`
	Token        DevToolCredential `json:"token"`
	APIKeyHeader *string           `json:"apiKeyHeader"`
}

// DevToolHeader is one default header of DevToolOptions.
type DevToolHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DevToolOptions is TS DevToolOptions (numbers as float64 so any JSON number round-trips).
type DevToolOptions struct {
	TLS             bool            `json:"tls"`
	TLSInsecure     bool            `json:"tlsInsecure"`
	TLSServerName   *string         `json:"tlsServerName"`
	SSLMode         *string         `json:"sslMode"`
	TimeoutSec      *float64        `json:"timeoutSec"`
	MaxRows         *float64        `json:"maxRows"`
	Consistency     *string         `json:"consistency"`
	Scheme          *string         `json:"scheme"`
	BasePath        *string         `json:"basePath"`
	FollowRedirects *bool           `json:"followRedirects"`
	DefaultHeaders  []DevToolHeader `json:"defaultHeaders"`
	CleanSession    *bool           `json:"cleanSession"`
	KeepAliveSec    *float64        `json:"keepAliveSec"`
}

// DevToolPreset is TS DevToolPreset (SiteConfig.devTools[]).
//
// Hash round trip (§21 #97 a): a preset decoded from a revision keeps its original JSON and marshals it back
// unchanged, so the canonical hash of the config is the cloud's even when a newer cloud adds keys this agent does not
// know (the reason agents < 0.11 refuse revisions that carry presets). A preset built in Go marshals its fields.
type DevToolPreset struct {
	PresetSlug  string         `json:"presetSlug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"`
	Target      DevToolTarget  `json:"target"`
	Auth        DevToolAuth    `json:"auth"`
	Options     DevToolOptions `json:"options"`
	ReadOnly    bool           `json:"readOnly"`

	raw json.RawMessage
}

type devToolPresetWire struct {
	PresetSlug  string         `json:"presetSlug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Kind        string         `json:"kind"`
	Target      DevToolTarget  `json:"target"`
	Auth        DevToolAuth    `json:"auth"`
	Options     DevToolOptions `json:"options"`
	ReadOnly    bool           `json:"readOnly"`
}

// UnmarshalJSON decodes the preset and remembers the original bytes (hash round trip).
func (p *DevToolPreset) UnmarshalJSON(b []byte) error {
	var w devToolPresetWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if w.Options.DefaultHeaders == nil {
		w.Options.DefaultHeaders = []DevToolHeader{}
	}
	*p = DevToolPreset{PresetSlug: w.PresetSlug, Name: w.Name, Description: w.Description, Kind: w.Kind, Target: w.Target,
		Auth: w.Auth, Options: w.Options, ReadOnly: w.ReadOnly, raw: append(json.RawMessage(nil), bytes.TrimSpace(b)...)}
	return nil
}

// MarshalJSON returns the decoded bytes unchanged, else the fields.
func (p DevToolPreset) MarshalJSON() ([]byte, error) {
	if len(p.raw) > 0 {
		return p.raw, nil
	}
	w := devToolPresetWire{p.PresetSlug, p.Name, p.Description, p.Kind, p.Target, p.Auth, p.Options, p.ReadOnly}
	if w.Options.DefaultHeaders == nil {
		w.Options.DefaultHeaders = []DevToolHeader{}
	}
	return json.Marshal(w)
}

// Detached returns a copy that marshals from its fields (after a change made in Go).
func (p DevToolPreset) Detached() DevToolPreset {
	p.raw = nil
	return p
}

// DevToolList returns SiteConfig.devTools ([] when the key is absent; TS siteDevTools).
func (sc *SiteConfig) DevToolList() []DevToolPreset {
	if sc == nil || sc.DevTools == nil {
		return []DevToolPreset{}
	}
	return *sc.DevTools
}

// NormalizeDevTools drops an empty devTools list (the key is omitted when empty, §21 #97 a: an empty list must never
// be written into a revision, it would change the hash seen by agents < 0.11).
func (sc *SiteConfig) NormalizeDevTools() {
	if sc.DevTools != nil && len(*sc.DevTools) == 0 {
		sc.DevTools = nil
	}
}

// NewDevToolPreset is TS newDevToolPreset (defaults of a kind).
func NewDevToolPreset(kind, slug string) DevToolPreset {
	var user, db *string
	if kind == "postgres" {
		u := "postgres"
		user = &u
	}
	if kind == "redis" {
		d := "0"
		db = &d
	}
	t := DevToolTarget{Kind: "container", ContainerSlug: new(string), PortName: new(string)}
	if kind == "sqlite" {
		t = DevToolTarget{Kind: "volume-file", VolumeSlug: new(string), Path: new(string)}
	}
	return DevToolPreset{PresetSlug: slug, Name: slug, Kind: kind, Target: t,
		Auth:    DevToolAuth{Username: user, Password: DevToolCredential{Kind: "none"}, Database: db, HTTPScheme: "none", Token: DevToolCredential{Kind: "none"}},
		Options: DevToolOptions{DefaultHeaders: []DevToolHeader{}}}
}

// HasLiteral reports whether the preset's password or token is a static value (§21 #103).
func (p DevToolPreset) HasLiteral() bool {
	return p.Auth.Password.Kind == "literal" || p.Auth.Token.Kind == "literal"
}

// MaskedLiterals returns the preset with static password / token values replaced by Mask (§21 #103: config views of
// the agent never show them; Reveal on the dev-tools connection does). Presets without a static value are returned
// unchanged (raw bytes kept).
func (p DevToolPreset) MaskedLiterals() DevToolPreset {
	if !p.HasLiteral() {
		return p
	}
	m := func(c DevToolCredential) DevToolCredential {
		if c.Kind == "literal" {
			v := Mask
			return DevToolCredential{Kind: "literal", Value: &v}
		}
		return c
	}
	q := p.Detached()
	q.Auth.Password, q.Auth.Token = m(q.Auth.Password), m(q.Auth.Token)
	return q
}
