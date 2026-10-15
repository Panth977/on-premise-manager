package model

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ───────────────────────── helpers

var jsonNull = []byte("null")

func isNull(b []byte) bool { return bytes.Equal(bytes.TrimSpace(b), jsonNull) }

// Optional is a TS "field?: T | null": Set=false → absent; Set=true, Value=nil → null.
// Parents that contain Optional fields implement MarshalJSON with marshalOmitUnset.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Some returns a set Optional with a value.
func Some[T any](v T) Optional[T] { return Optional[T]{Set: true, Value: &v} }

// Null returns a set Optional holding null.
func Null[T any]() Optional[T] { return Optional[T]{Set: true} }

// MarshalJSON emits the value or null (unset fields are removed by the parent).
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return jsonNull, nil
	}
	return json.Marshal(*o.Value)
}

// UnmarshalJSON is called for a present field (also for null).
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if isNull(b) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// marshalOmitUnset marshals v (an alias type without MarshalJSON) and removes the given keys.
func marshalOmitUnset(v any, unset map[string]bool) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(unset) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, drop := range unset {
		if drop {
			delete(m, k)
		}
	}
	return json.Marshal(m)
}

// ───────────────────────── NumOrExpr

// NumOrExpr is TS `number | Expr` (ImagePort.port) and `number | Expr | null` (ContainerPort.hostPort).
// Both nil = null.
type NumOrExpr struct {
	Num  *float64
	Expr *string
}

// IsNull reports a null value.
func (n NumOrExpr) IsNull() bool { return n.Num == nil && n.Expr == nil }

// Any returns nil, float64 or string (input for expr.ResolveNumberField).
func (n NumOrExpr) Any() any {
	if n.Num != nil {
		return *n.Num
	}
	if n.Expr != nil {
		return *n.Expr
	}
	return nil
}

// MarshalJSON emits a number, a string, or null.
func (n NumOrExpr) MarshalJSON() ([]byte, error) {
	if n.Num != nil {
		return json.Marshal(*n.Num)
	}
	if n.Expr != nil {
		return json.Marshal(*n.Expr)
	}
	return jsonNull, nil
}

// UnmarshalJSON accepts a number, a string or null.
func (n *NumOrExpr) UnmarshalJSON(b []byte) error {
	*n = NumOrExpr{}
	if isNull(b) {
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		n.Num = &f
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("NumOrExpr: want number, string or null: %s", b)
	}
	n.Expr = &s
	return nil
}

// ───────────────────────── ImageEnvValue

// ImageEnvValue is TS `Value | string[]` (ImageEnv.value). Use *ImageEnvValue with omitempty: nil = absent.
type ImageEnvValue struct {
	Str  *string
	List []string // non-nil = list of allowed values (first = default)
}

// Default returns the default value (the string, or the first list item).
func (v ImageEnvValue) Default() (string, bool) {
	if v.List != nil {
		if len(v.List) == 0 {
			return "", false
		}
		return v.List[0], true
	}
	if v.Str != nil {
		return *v.Str, true
	}
	return "", false
}

// MarshalJSON emits the string or the list.
func (v ImageEnvValue) MarshalJSON() ([]byte, error) {
	if v.List != nil {
		return json.Marshal(v.List)
	}
	if v.Str != nil {
		return json.Marshal(*v.Str)
	}
	return jsonNull, nil
}

// UnmarshalJSON accepts a string or a string list.
func (v *ImageEnvValue) UnmarshalJSON(b []byte) error {
	*v = ImageEnvValue{}
	if isNull(b) {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v.Str = &s
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return fmt.Errorf("ImageEnv.value: want string or string[]: %s", b)
	}
	if l == nil {
		l = []string{}
	}
	v.List = l
	return nil
}

// ───────────────────────── VariableValue

// VariableValue is TS VariableValue: nil, bool, float64, string, []any, map[string]any. Always emitted (null ok).
type VariableValue struct{ V any }

// MarshalJSON emits the wrapped value.
func (v VariableValue) MarshalJSON() ([]byte, error) { return json.Marshal(v.V) }

// UnmarshalJSON decodes into any (numbers as float64).
func (v *VariableValue) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &v.V) }

// ───────────────────────── HealthCheck

// HealthCheck is the TS union HealthCheckBase & (http | tcp | exec | log).
type HealthCheck struct {
	Type           string   // http | tcp | exec | log
	IntervalSec    float64  // default 10
	TimeoutSec     float64  // default 5
	Retries        float64  // default 3
	StartPeriodSec float64  // default 30
	PortName       string   // http, tcp
	Path           string   // http
	ExpectStatus   float64  // http
	Command        []string // exec
	ExpectExitCode float64  // exec
	Pattern        string   // log
}

type hcWire struct {
	Type           string    `json:"type"`
	IntervalSec    float64   `json:"intervalSec"`
	TimeoutSec     float64   `json:"timeoutSec"`
	Retries        float64   `json:"retries"`
	StartPeriodSec float64   `json:"startPeriodSec"`
	PortName       *string   `json:"portName,omitempty"`
	Path           *string   `json:"path,omitempty"`
	ExpectStatus   *float64  `json:"expectStatus,omitempty"`
	Command        *[]string `json:"command,omitempty"`
	ExpectExitCode *float64  `json:"expectExitCode,omitempty"`
	Pattern        *string   `json:"pattern,omitempty"`
}

// MarshalJSON emits exactly the fields of the variant.
func (h HealthCheck) MarshalJSON() ([]byte, error) {
	w := hcWire{Type: h.Type, IntervalSec: h.IntervalSec, TimeoutSec: h.TimeoutSec, Retries: h.Retries, StartPeriodSec: h.StartPeriodSec}
	switch h.Type {
	case "http":
		w.PortName, w.Path, w.ExpectStatus = &h.PortName, &h.Path, &h.ExpectStatus
	case "tcp":
		w.PortName = &h.PortName
	case "exec":
		c := h.Command
		if c == nil {
			c = []string{}
		}
		w.Command, w.ExpectExitCode = &c, &h.ExpectExitCode
	case "log":
		w.Pattern = &h.Pattern
	default:
		return nil, fmt.Errorf("healthcheck: unknown type %q", h.Type)
	}
	return json.Marshal(w)
}

// UnmarshalJSON rejects unknown types.
func (h *HealthCheck) UnmarshalJSON(b []byte) error {
	var w hcWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	switch w.Type {
	case "http", "tcp", "exec", "log":
	default:
		return fmt.Errorf("healthcheck: unknown type %q", w.Type)
	}
	*h = HealthCheck{Type: w.Type, IntervalSec: w.IntervalSec, TimeoutSec: w.TimeoutSec, Retries: w.Retries, StartPeriodSec: w.StartPeriodSec}
	if w.PortName != nil {
		h.PortName = *w.PortName
	}
	if w.Path != nil {
		h.Path = *w.Path
	}
	if w.ExpectStatus != nil {
		h.ExpectStatus = *w.ExpectStatus
	}
	if w.Command != nil {
		h.Command = *w.Command
	}
	if w.ExpectExitCode != nil {
		h.ExpectExitCode = *w.ExpectExitCode
	}
	if w.Pattern != nil {
		h.Pattern = *w.Pattern
	}
	return nil
}

// ───────────────────────── EdgeTls

// EdgeTls is the TS union none | internal | acme | files.
type EdgeTls struct {
	Mode               string // none | internal | acme | files
	Email              string // acme
	Challenge          string // acme: http-01 | tls-alpn-01 | dns-01
	DNSProvider        *string
	DNSTokenSecretSlug *string
	CertFileSlug       string // files
	KeySecretSlug      string // files
}

// MarshalJSON emits exactly the fields of the mode (acme always emits dnsProvider / dnsTokenSecretSlug, maybe null).
func (t EdgeTls) MarshalJSON() ([]byte, error) {
	switch t.Mode {
	case "none", "internal":
		return json.Marshal(struct {
			Mode string `json:"mode"`
		}{t.Mode})
	case "acme":
		return json.Marshal(struct {
			Mode               string  `json:"mode"`
			Email              string  `json:"email"`
			Challenge          string  `json:"challenge"`
			DNSProvider        *string `json:"dnsProvider"`
			DNSTokenSecretSlug *string `json:"dnsTokenSecretSlug"`
		}{t.Mode, t.Email, t.Challenge, t.DNSProvider, t.DNSTokenSecretSlug})
	case "files":
		return json.Marshal(struct {
			Mode          string `json:"mode"`
			CertFileSlug  string `json:"certFileSlug"`
			KeySecretSlug string `json:"keySecretSlug"`
		}{t.Mode, t.CertFileSlug, t.KeySecretSlug})
	}
	return nil, fmt.Errorf("tls: unknown mode %q", t.Mode)
}

// UnmarshalJSON rejects unknown modes.
func (t *EdgeTls) UnmarshalJSON(b []byte) error {
	var w struct {
		Mode               string  `json:"mode"`
		Email              string  `json:"email"`
		Challenge          string  `json:"challenge"`
		DNSProvider        *string `json:"dnsProvider"`
		DNSTokenSecretSlug *string `json:"dnsTokenSecretSlug"`
		CertFileSlug       string  `json:"certFileSlug"`
		KeySecretSlug      string  `json:"keySecretSlug"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	switch w.Mode {
	case "none", "internal", "acme", "files":
	default:
		return fmt.Errorf("tls: unknown mode %q", w.Mode)
	}
	*t = EdgeTls(w)
	return nil
}

// ───────────────────────── VolumeSource

// VolumeSource is the TS union volume | host | file | secret | none (ContainerMount.source).
// The round-C kind "data" ({kind:"data", name}) is still DECODED and re-encoded unchanged, so revisions written before
// round D keep their hash; readers treat it as a volume source (VolumeName) — see validate.NormalizeConfigVolumes.
type VolumeSource struct {
	Kind       string // volume | host | file | secret | none (legacy: data)
	VolumeSlug string // volume: a declared site volume <dataDir>/vol/<volumeSlug>
	Name       string // legacy data: the data volume name (= the volumeSlug since round D)
	Path       string // host: absolute host path
	FileSlug   string // file
	SecretSlug string // secret
}

// IsVolume is true for a site volume source (kind volume, or the legacy kind data).
func (v VolumeSource) IsVolume() bool { return v.Kind == "volume" || v.Kind == "data" }

// VolumeName is the volumeSlug of a volume source (legacy data: its name); "" for other kinds.
func (v VolumeSource) VolumeName() string {
	switch v.Kind {
	case "volume":
		return v.VolumeSlug
	case "data":
		return v.Name
	}
	return ""
}

// MarshalJSON emits exactly the fields of the kind.
func (v VolumeSource) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case "volume":
		return json.Marshal(struct {
			Kind       string `json:"kind"`
			VolumeSlug string `json:"volumeSlug"`
		}{v.Kind, v.VolumeSlug})
	case "data":
		return json.Marshal(struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}{v.Kind, v.Name})
	case "host":
		return json.Marshal(struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{v.Kind, v.Path})
	case "file":
		return json.Marshal(struct {
			Kind     string `json:"kind"`
			FileSlug string `json:"fileSlug"`
		}{v.Kind, v.FileSlug})
	case "secret":
		return json.Marshal(struct {
			Kind       string `json:"kind"`
			SecretSlug string `json:"secretSlug"`
		}{v.Kind, v.SecretSlug})
	case "none":
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{v.Kind})
	}
	return nil, fmt.Errorf("volume source: unknown kind %q", v.Kind)
}

// UnmarshalJSON rejects unknown kinds.
func (v *VolumeSource) UnmarshalJSON(b []byte) error {
	var w struct {
		Kind       string `json:"kind"`
		VolumeSlug string `json:"volumeSlug"`
		Name       string `json:"name"`
		Path       string `json:"path"`
		FileSlug   string `json:"fileSlug"`
		SecretSlug string `json:"secretSlug"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	switch w.Kind {
	case "volume", "data", "host", "file", "secret", "none":
	default:
		return fmt.Errorf("volume source: unknown kind %q", w.Kind)
	}
	*v = VolumeSource(w)
	return nil
}

// ───────────────────────── FetchRef

// FetchRef is TS `{ serviceSlug; version } | { fileSlug; revId }` (FetchRequest.ref).
type FetchRef struct {
	ServiceSlug string `json:"serviceSlug,omitempty"`
	Version     string `json:"version,omitempty"`
	FileSlug    string `json:"fileSlug,omitempty"`
	RevID       string `json:"revId,omitempty"`
}
