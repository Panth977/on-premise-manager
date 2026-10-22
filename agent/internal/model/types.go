// Package model mirrors shared/src/types.ts (the normative OPM wire format). JSON tags are identical to the TS names.
//
// Conventions: TS `T | null` → pointer (or nil slice / nil map) and the field is always emitted (no omitempty);
// TS `field?: T` → omitempty; TS `field?: T | null` → Optional[T] with a custom parent MarshalJSON.
// Slices that TS declares as `T[]` (never null) must be initialised (non-nil) by Go code before marshalling.
package model

import (
	"encoding/json"
	"fmt"
)

// Scalars (TS common scalars).
type (
	ID     = string // Firestore auto id
	Slug   = string // ^[a-z][a-z0-9-]{1,39}$
	UID    = string
	Ts     = string // ISO 8601 UTC
	Sha256 = string // "sha256:" + 64 hex
	ULID   = string
	RevID  = string
	Value  = string // plain string or Expr
	// Icon is a non-null TS Icon value ("lucide:<name>", "upload:<iconId>", legacy bare Lucide name, legacy https URL,
	// or a data:image/… URL inside Revision.catalog). TS `Icon = string | null` → use *Icon without omitempty.
	Icon = string
)

// Mask is the placeholder for secret values in reads (TS MASK).
const Mask = "•••"

// Audit is TS Audit (embedded).
type Audit struct {
	CreatedAt Ts  `json:"createdAt"`
	CreatedBy UID `json:"createdBy"`
	UpdatedAt Ts  `json:"updatedAt"`
	UpdatedBy UID `json:"updatedBy"`
}

// ───────────────────────── errors

// ErrorCode values (TS ErrorCode).
const (
	ErrUnauthenticated  = "unauthenticated"
	ErrSessionExpired   = "session-expired"
	ErrInvalidKey       = "invalid-key"
	ErrActorRequired    = "actor-required"
	ErrForbidden        = "forbidden"
	ErrNotFound         = "not-found"
	ErrMethodNotAllowed = "method-not-allowed"
	ErrConflict         = "conflict"
	ErrStale            = "stale"
	ErrGone             = "gone"
	ErrTooLarge         = "too-large"
	ErrInvalid          = "invalid"
	ErrGateClosed       = "gate-closed"
	ErrLocked           = "locked"
	ErrNoInternet       = "no-internet"
	ErrUnavailable      = "unavailable"
	ErrRateLimited      = "rate-limited"
	ErrInternal         = "internal"
)

// ErrorHTTPStatus is TS ERROR_HTTP_STATUS.
var ErrorHTTPStatus = map[string]int{
	ErrUnauthenticated: 401, ErrSessionExpired: 401, ErrInvalidKey: 401, ErrActorRequired: 401,
	ErrForbidden: 403, ErrNotFound: 404, ErrMethodNotAllowed: 405, ErrConflict: 409, ErrStale: 409,
	ErrGone: 410, ErrTooLarge: 413, ErrInvalid: 422, ErrGateClosed: 409, ErrLocked: 409,
	ErrNoInternet: 503, ErrUnavailable: 503, ErrRateLimited: 429, ErrInternal: 500,
}

// ValidationIssue is TS ValidationIssue.
type ValidationIssue struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error | warning
}

// ValidationReport is TS ValidationReport.
type ValidationReport struct {
	Ok     bool              `json:"ok"`
	Issues []ValidationIssue `json:"issues"`
}

// APIError is the inner object of TS ApiErrorBody.
type APIError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// APIErrorBody is TS ApiErrorBody.
type APIErrorBody struct {
	Error APIError `json:"error"`
}

// ───────────────────────── images

// ImagePort is TS ImagePort.
type ImagePort struct {
	Name        Slug      `json:"name"`
	Port        NumOrExpr `json:"port"`
	Protocol    string    `json:"protocol"` // tcp | udp
	Description string    `json:"description"`
}

// ImageEnv is TS ImageEnv (Value nil = absent). Value may still hold the legacy string[] form (read it through
// validate.NormalizeImageEnv). Options is TS `options?: string[] | null`: absent stays absent, null stays null and a
// list stays a list (MarshalJSON), so Revision.catalogHash re-computes byte-exactly for old and new catalogs.
type ImageEnv struct {
	Name        string             `json:"name"`
	Value       *ImageEnvValue     `json:"value,omitempty"`
	Options     Optional[[]string] `json:"options"`
	Required    bool               `json:"required"`
	Secret      bool               `json:"secret"`
	Description string             `json:"description"`
}

// MarshalJSON drops "options" when it was absent.
func (e ImageEnv) MarshalJSON() ([]byte, error) {
	type alias ImageEnv
	return marshalOmitUnset(alias(e), map[string]bool{"options": !e.Options.Set})
}

// OptionList returns the allowed values (nil = free text).
func (e ImageEnv) OptionList() []string {
	if e.Options.Value == nil {
		return nil
	}
	return *e.Options.Value
}

// ImageVolume is TS ImageVolume.
type ImageVolume struct {
	Name      Slug   `json:"name"`
	MountPath string `json:"mountPath"`
	// Kind (§21 #83): dir | file. nil = key absent (older revision catalogs keep their hash) = inferred from the
	// mount path. Read it with validate.ImageVolumeKind().
	Kind        *string `json:"kind,omitempty"`
	Description string  `json:"description"`
}

// ImageLayer is TS ImageLayer.
type ImageLayer struct {
	Digest    Sha256 `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
	MediaType string `json:"mediaType"`
}

// ImageImport is TS ImageImport.
type ImageImport struct {
	Mode              string       `json:"mode"`   // copy | gateway
	Status            string       `json:"status"` // pending | copying | ready | failed
	Error             *string      `json:"error"`
	SizeBytes         *int64       `json:"sizeBytes"`
	ManifestMediaType *string      `json:"manifestMediaType"`
	ConfigDigest      *Sha256      `json:"configDigest"`
	Layers            []ImageLayer `json:"layers"`
	StartedAt         *Ts          `json:"startedAt"`
	FinishedAt        *Ts          `json:"finishedAt"`
}

// Image is TS Image.
type Image struct {
	Audit
	Version        string        `json:"version"`     // the document id (services/{serviceSlug}/images/{version})
	ServiceSlug    Slug          `json:"serviceSlug"` // parent
	Reference      string        `json:"reference"`   // full docker reference as entered
	Source         ImageRefParts `json:"source"`
	RegistryID     *ID           `json:"registryId"` // null = public
	Digest         *Sha256       `json:"digest"`
	Platform       string        `json:"platform"`
	Import         ImageImport   `json:"import"`
	Kind           string        `json:"kind"` // service | job
	Entrypoint     []string      `json:"entrypoint"`
	Command        []string      `json:"command"`
	Workdir        *string       `json:"workdir"`
	User           *string       `json:"user"`
	Ports          []ImagePort   `json:"ports"`
	Env            []ImageEnv    `json:"env"`
	Volumes        []ImageVolume `json:"volumes"`
	Healthcheck    *HealthCheck  `json:"healthcheck"`
	StopTimeoutSec float64       `json:"stopTimeoutSec"`
	Description    string        `json:"description"`
}

// ImageRefParts is TS ImageRefParts (a parsed docker reference).
type ImageRefParts struct {
	Host       string  `json:"host"`
	Repository string  `json:"repository"`
	Tag        *string `json:"tag"`
	Digest     *Sha256 `json:"digest"`
}

// ImageSpec is TS ImageSpec (copied into Revision.catalog).
type ImageSpec struct {
	ServiceSlug       Slug          `json:"serviceSlug"`
	Version           string        `json:"version"`
	Digest            Sha256        `json:"digest"`
	ConfigDigest      Sha256        `json:"configDigest"`
	Platform          string        `json:"platform"`
	ManifestMediaType string        `json:"manifestMediaType"`
	Layers            []ImageLayer  `json:"layers"`
	SizeBytes         int64         `json:"sizeBytes"`
	Kind              string        `json:"kind"`
	Entrypoint        []string      `json:"entrypoint"`
	Command           []string      `json:"command"`
	Workdir           *string       `json:"workdir"`
	User              *string       `json:"user"`
	Ports             []ImagePort   `json:"ports"`
	Env               []ImageEnv    `json:"env"`
	Volumes           []ImageVolume `json:"volumes"`
	Healthcheck       *HealthCheck  `json:"healthcheck"`
	StopTimeoutSec    float64       `json:"stopTimeoutSec"`
	// Reference (§21 #113) of a SITE image (Digest "", version local-<hex>): the pullable docker reference it came from
	// (pinned with @sha256:<repo digest> when the registry gave one). An agent that does not have the image pulls it.
	// Absent for catalog images and for images built on the host.
	Reference *string `json:"reference,omitempty"`
}

// IsSiteImage reports a §21 #104 / #113 local image (no blobs in the cloud: drift-adopted or added on the site).
func (s ImageSpec) IsSiteImage() bool { return s.Digest == "" }

// NotFoundPage is TS NotFoundPage.
type NotFoundPage struct {
	NotFoundPageID ID     `json:"notFoundPageId"`
	Label          string `json:"label"`
	HTMLCode       string `json:"htmlCode"`
	CreatedAt      Ts     `json:"createdAt"`
	CreatedBy      UID    `json:"createdBy"`
}

// ───────────────────────── config

// VariableValidation is TS Variable.validation.
type VariableValidation struct {
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
	Pattern *string  `json:"pattern"`
}

// Variable is TS Variable.
type Variable struct {
	VariableSlug Slug                `json:"variableSlug"`
	Label        string              `json:"label"`
	Description  string              `json:"description"`
	Type         string              `json:"type"` // string | number | boolean | enum | list | json
	Value        VariableValue       `json:"value"`
	Required     bool                `json:"required"`
	Options      []string            `json:"options"`
	Validation   *VariableValidation `json:"validation"`
}

// SecretGenerate is TS SecretDef.generate.
type SecretGenerate struct {
	Charset string `json:"charset"` // alnum | hex | base64url
	Length  int    `json:"length"`
}

// SecretDef is TS SecretDef (plain value).
type SecretDef struct {
	SecretSlug  Slug            `json:"secretSlug"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Generate    *SecretGenerate `json:"generate"`
	Value       *string         `json:"value"`
	RotatedAt   *Ts             `json:"rotatedAt"`
}

// SecretDefView is TS SecretDefView (Value is "•••" or nil).
type SecretDefView struct {
	SecretSlug  Slug            `json:"secretSlug"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Generate    *SecretGenerate `json:"generate"`
	Value       *string         `json:"value"`
	IsSet       bool            `json:"isSet"`
	RotatedAt   *Ts             `json:"rotatedAt"`
}

// SecretDefInput is TS SecretDefInput. PlainValue unset = keep, null = clear, string = set.
type SecretDefInput struct {
	SecretSlug  Slug             `json:"secretSlug"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Required    bool             `json:"required"`
	Generate    *SecretGenerate  `json:"generate"`
	PlainValue  Optional[string] `json:"plainValue"`
	GenerateNow *bool            `json:"generateNow,omitempty"`
}

// MarshalJSON omits plainValue when unset.
func (s SecretDefInput) MarshalJSON() ([]byte, error) {
	type alias SecretDefInput
	return marshalOmitUnset(alias(s), map[string]bool{"plainValue": !s.PlainValue.Set})
}

// BlobRef is TS BlobRef / FileDef.content.
type BlobRef struct {
	Sha256    Sha256 `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// FileDef is TS FileDef.
type FileDef struct {
	FileSlug    Slug     `json:"fileSlug"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Mime        string   `json:"mime"`
	Editor      string   `json:"editor"` // text | binary
	MaxBytes    int64    `json:"maxBytes"`
	Content     *BlobRef `json:"content"`
	// Render is TS FileDef.render ('expressions' | 'none'). Legacy documents without the key keep it absent (hash
	// round-trip); readers use RenderMode().
	Render Optional[string] `json:"render"`
	// Location is TS FileDef.location (round D): where the ONE copy per site lives. nil = the key is absent (older
	// documents; hash round-trip) = auto. Read it with FileLocation().
	Location *StorageLocation `json:"location,omitempty"`
}

// FileLocation is the normalized location (missing = auto).
func (f FileDef) FileLocation() StorageLocation { return f.Location.Normalized() }

// StorageLocation is TS StorageLocation: {kind:"auto"} | {kind:"path", path} ("./x" = relative to agentHome, or an
// absolute path).
type StorageLocation struct {
	Kind string `json:"kind"`           // auto | path
	Path string `json:"path,omitempty"` // kind path
}

// Normalized returns the location (nil / unknown kind = auto).
func (l *StorageLocation) Normalized() StorageLocation {
	if l == nil || l.Kind != "path" {
		return StorageLocation{Kind: "auto"}
	}
	return StorageLocation{Kind: "path", Path: l.Path}
}

// MarshalJSON omits render only when the decoded document had no render key.
func (f FileDef) MarshalJSON() ([]byte, error) {
	type alias FileDef
	return marshalOmitUnset(alias(f), map[string]bool{"render": !f.Render.Set})
}

// RenderMode is TS fileRenderMode: binary → none; text → render (default expressions).
func (f FileDef) RenderMode() string {
	if f.Editor == "binary" {
		return "none"
	}
	if f.Render.Value != nil && *f.Render.Value != "" {
		return *f.Render.Value
	}
	return "expressions"
}

// ContainerEnv is TS ContainerEnv.
type ContainerEnv struct {
	Name      string  `json:"name"`
	Value     *string `json:"value"`
	SecretRef *Slug   `json:"secretRef"`
}

// ContainerPort is TS ContainerPort (HostPort may be null = not published).
type ContainerPort struct {
	PortName Slug      `json:"portName"`
	HostPort NumOrExpr `json:"hostPort"`
	HostIP   string    `json:"hostIp"` // 0.0.0.0 | 127.0.0.1
	Edge     bool      `json:"edge"`   // the edge may proxy to this port
}

// ContainerMount is TS ContainerMount (one row per image volume, or an extra mount when ImageVolume is nil).
type ContainerMount struct {
	ImageVolume *Slug        `json:"imageVolume"`
	MountPath   *string      `json:"mountPath"`
	Source      VolumeSource `json:"source"`
	ReadOnly    bool         `json:"readOnly"`
}

// ContainerResources is TS Container.resources.
type ContainerResources struct {
	Cpus     *float64 `json:"cpus"`
	MemoryMb *float64 `json:"memoryMb"`
}

// ServiceRef is TS { serviceSlug }.
type ServiceRef struct {
	ServiceSlug Slug `json:"serviceSlug"`
}

// ContainerRef is TS { containerSlug } (Container.depends).
type ContainerRef struct {
	ContainerSlug Slug `json:"containerSlug"`
}

// SiteRef is TS { siteSlug }.
type SiteRef struct {
	SiteSlug Slug `json:"siteSlug"`
}

// Container is TS Container: one container INSTANCE on a site (containerSlug is its only identifier).
type Container struct {
	ContainerSlug Slug               `json:"containerSlug"` // unique per site; DNS alias, docker name opm_<dep>_<site>_<containerSlug>
	ServiceSlug   Slug               `json:"serviceSlug"`
	Version       string             `json:"version"` // Image.version of that service
	Enabled       bool               `json:"enabled"`
	Env           []ContainerEnv     `json:"env"`
	Ports         []ContainerPort    `json:"ports"`
	Volumes       []ContainerMount   `json:"volumes"`
	Command       []string           `json:"command"`
	Entrypoint    []string           `json:"entrypoint"`
	Restart       string             `json:"restart"` // always | on-failure | never
	Resources     ContainerResources `json:"resources"`
	Healthcheck   *HealthCheck       `json:"healthcheck"` // nil = the image health check
	Depends       []ContainerRef     `json:"depends"`
}

// SiteConfig is TS SiteConfig.
type SiteConfig struct {
	Host              *Value `json:"host"` // nil = auto (advertised / detected address)
	AgentPort         int    `json:"agentPort"`
	AgentGuiAliasPort *int   `json:"agentGuiAliasPort"`
	// Volumes are the declared data folders of the site (round D). nil = the key is ABSENT (a config written before
	// round D: kept absent so the revision hash round-trips; validate.NormalizeConfigVolumes declares them implicitly).
	Volumes        *[]SiteVolume `json:"volumes,omitempty"`
	Containers     []Container   `json:"containers"`
	Depends        []SiteRef     `json:"depends"`
	NotFoundPageID *ID           `json:"notFoundPageId"`
	Edge           Edge          `json:"edge"`
	// DevTools (§21 #97): dev-tool presets of the site. nil = the key is ABSENT (it is omitted when empty, §21 #97 a:
	// never write an empty list into a revision); read it with DevToolList().
	DevTools *[]DevToolPreset `json:"devTools,omitempty"`
}

// SiteVolume is TS SiteVolume: a declared data folder of a site, <dataDir>/vol/<volumeSlug>/ on the agent.
type SiteVolume struct {
	VolumeSlug  string `json:"volumeSlug"` // the only identifier, unique per site (never renamed)
	Label       string `json:"label"`
	Description string `json:"description"`
	Backup      bool   `json:"backup"` // included in manual / scheduled / pre-update snapshots
	// Location (round D part 2): where the folder lives (nil = key absent = auto = <agentHome>/volumes/<volumeSlug>).
	Location *StorageLocation `json:"location,omitempty"`
	// Kind (round E): dir | file. nil = key absent (older revisions keep their hash) = dir. Read it with VolumeKind().
	Kind *string `json:"kind,omitempty"`
	// Seed (round E): starter content; absent (older revisions) / null = start empty. Read it with SeedOf().
	Seed Optional[VolumeSeed] `json:"seed"`
	// Persist (§21 #101): persist-to-cloud settings of a folder volume; nil = key ABSENT = off (revisions of agents < 0.12
	// keep their hash).
	Persist *VolumePersist `json:"persist,omitempty"`
	// BackupPolicy (§21 #112): automatic backups of this volume; nil = key ABSENT = today's behaviour (in the daily
	// scheduled site snapshot, its 7 newest kept) — older revisions keep their hash.
	BackupPolicy *VolumeBackupPolicy `json:"backupPolicy,omitempty"`
}

// ───────────────────────── §21 #112 per-volume automatic backups

// VolumeBackupSchedules are the valid VolumeBackupPolicy.Schedule values.
var VolumeBackupSchedules = map[string]int64{"off": 0, "hourly": 3600, "6h": 6 * 3600, "daily": 24 * 3600, "weekly": 7 * 24 * 3600}

// VolumeBackupPolicy is TS VolumeBackupPolicy: how often the agent snapshots this one volume and how many it keeps.
type VolumeBackupPolicy struct {
	Schedule string `json:"schedule"` // off | hourly | 6h | daily | weekly
	Keep     int    `json:"keep"`     // 1 … 100 newest automatic backups of this volume kept
}

// DefaultVolumeBackupPolicy is the behaviour without a policy (the daily site snapshot keeps 7).
var DefaultVolumeBackupPolicy = VolumeBackupPolicy{Schedule: "daily", Keep: 7}

// BackupPolicyOf is the effective policy (backup = false → off).
func (v SiteVolume) BackupPolicyOf() VolumeBackupPolicy {
	if !v.Backup {
		return VolumeBackupPolicy{Schedule: "off", Keep: 0}
	}
	if v.BackupPolicy == nil {
		return DefaultVolumeBackupPolicy
	}
	return *v.BackupPolicy
}

// VolumeBackup is TS VolumeBackup: one snapshot on this site that holds the volume (any reason) — restorable.
type VolumeBackup struct {
	SnapshotID string `json:"snapshotId"`
	CreatedAt  Ts     `json:"createdAt"`
	Reason     string `json:"reason"` // volume-auto | scheduled | pre-update | manual | imported
	Note       string `json:"note"`
	CreatedBy  string `json:"createdBy"`
	SizeBytes  int64  `json:"sizeBytes"` // the archive of this volume
	Auto       bool   `json:"auto"`      // taken by the volume's own schedule
}

// VolumeBackupsResponse is TS VolumeBackupsResponse (GET …/volumes/{volumeSlug}/backups).
type VolumeBackupsResponse struct {
	VolumeSlug string             `json:"volumeSlug"`
	Policy     VolumeBackupPolicy `json:"policy"`
	Explicit   bool               `json:"explicit"` // the volume has its own policy (else the daily site snapshot)
	NextAt     *Ts                `json:"nextAt"`
	Items      []VolumeBackup     `json:"items"`
}

// MarshalJSON omits seed only when the decoded document had no seed key.
func (v SiteVolume) MarshalJSON() ([]byte, error) {
	type wire struct {
		VolumeSlug  string              `json:"volumeSlug"`
		Label       string              `json:"label"`
		Description string              `json:"description"`
		Backup      bool                `json:"backup"`
		Location    *StorageLocation    `json:"location,omitempty"`
		Kind        *string             `json:"kind,omitempty"`
		Seed        *json.RawMessage    `json:"seed,omitempty"`
		Persist     *VolumePersist      `json:"persist,omitempty"`
		Policy      *VolumeBackupPolicy `json:"backupPolicy,omitempty"`
	}
	w := wire{v.VolumeSlug, v.Label, v.Description, v.Backup, v.Location, v.Kind, nil, v.Persist, v.BackupPolicy}
	if v.Seed.Set {
		raw := json.RawMessage("null")
		if v.Seed.Value != nil {
			b, err := json.Marshal(*v.Seed.Value)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		w.Seed = &raw
	}
	return json.Marshal(w)
}

// VolumeKind is the normalized kind (missing = dir).
func (v SiteVolume) VolumeKind() string {
	if v.Kind == nil || *v.Kind == "" {
		return "dir"
	}
	return *v.Kind
}

// SeedOf is the starter content (nil = none).
func (v SiteVolume) SeedOf() *VolumeSeed { return v.Seed.Value }

// SeedEntry is one entry of a files seed.
type SeedEntry struct {
	Path     string `json:"path"`
	FileSlug Slug   `json:"fileSlug"`
}

// VolumeSeed is the TS union tree | single | files | file | archive (SiteVolume.seed, round E + §21 #101).
// tree / single (§21 #101) are the volume's OWN content (manifest; bytes = blobs files/<hex>); files / file are the
// legacy seeds that reference Files (only revisions published before 0.12 hold them; still applied).
type VolumeSeed struct {
	Kind       string            // tree | single | files | file | archive
	Tree       []VolumeTreeEntry // tree: entries
	Dirs       []string          // tree: empty folders
	Sha256     Sha256            // single
	Mode       string            // single
	Render     string            // single
	Entries    []SeedEntry       // files (legacy)
	FileSlug   Slug              // file (legacy)
	BlobSha256 Sha256            // archive
	Name       string            // archive
	Size       int64             // archive, single
}

// TreeOf is the content of a tree seed as a VolumeTree (not normalized).
func (s VolumeSeed) TreeOf() VolumeTree { return VolumeTree{Entries: s.Tree, Dirs: s.Dirs} }

// MarshalJSON emits exactly the fields of the kind.
func (s VolumeSeed) MarshalJSON() ([]byte, error) {
	switch s.Kind {
	case "tree":
		e, d := s.Tree, s.Dirs
		if e == nil {
			e = []VolumeTreeEntry{}
		}
		if d == nil {
			d = []string{}
		}
		return json.Marshal(struct {
			Kind    string            `json:"kind"`
			Entries []VolumeTreeEntry `json:"entries"`
			Dirs    []string          `json:"dirs"`
		}{s.Kind, e, d})
	case "single":
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			Sha256 Sha256 `json:"sha256"`
			Size   int64  `json:"size"`
			Mode   string `json:"mode"`
			Render string `json:"render"`
		}{s.Kind, s.Sha256, s.Size, s.Mode, s.Render})
	case "files":
		e := s.Entries
		if e == nil {
			e = []SeedEntry{}
		}
		return json.Marshal(struct {
			Kind    string      `json:"kind"`
			Entries []SeedEntry `json:"entries"`
		}{s.Kind, e})
	case "file":
		return json.Marshal(struct {
			Kind     string `json:"kind"`
			FileSlug Slug   `json:"fileSlug"`
		}{s.Kind, s.FileSlug})
	case "archive":
		return json.Marshal(struct {
			Kind       string `json:"kind"`
			BlobSha256 Sha256 `json:"blobSha256"`
			Name       string `json:"name"`
			Size       int64  `json:"size"`
		}{s.Kind, s.BlobSha256, s.Name, s.Size})
	}
	return nil, fmt.Errorf("volume seed: unknown kind %q", s.Kind)
}

// UnmarshalJSON reads every kind (an unknown kind is kept for the validator: V_VOLUME_SEED).
func (s *VolumeSeed) UnmarshalJSON(b []byte) error {
	var k struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return err
	}
	if k.Kind == "tree" {
		var w struct {
			Entries []VolumeTreeEntry `json:"entries"`
			Dirs    []string          `json:"dirs"`
		}
		if err := json.Unmarshal(b, &w); err != nil {
			return err
		}
		*s = VolumeSeed{Kind: k.Kind, Tree: w.Entries, Dirs: w.Dirs}
		return nil
	}
	var w struct {
		Entries    []SeedEntry `json:"entries"`
		FileSlug   Slug        `json:"fileSlug"`
		BlobSha256 Sha256      `json:"blobSha256"`
		Name       string      `json:"name"`
		Size       int64       `json:"size"`
		Sha256     Sha256      `json:"sha256"`
		Mode       string      `json:"mode"`
		Render     string      `json:"render"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = VolumeSeed{Kind: k.Kind, Entries: w.Entries, FileSlug: w.FileSlug, BlobSha256: w.BlobSha256, Name: w.Name, Size: w.Size, Sha256: w.Sha256, Mode: w.Mode, Render: w.Render}
	return nil
}

// VolumeLocation is the normalized location (missing = auto).
func (v SiteVolume) VolumeLocation() StorageLocation { return v.Location.Normalized() }

// VolumeList returns the declared volumes (nil-safe; a pre-round-D config has none).
func (sc *SiteConfig) VolumeList() []SiteVolume {
	if sc == nil || sc.Volumes == nil {
		return nil
	}
	return *sc.Volumes
}

// FindVolume returns the declared volume with this slug or nil.
func (sc *SiteConfig) FindVolume(slug string) *SiteVolume {
	if sc == nil || sc.Volumes == nil {
		return nil
	}
	for i := range *sc.Volumes {
		if (*sc.Volumes)[i].VolumeSlug == slug {
			return &(*sc.Volumes)[i]
		}
	}
	return nil
}

// Site is TS Site.
type Site struct {
	SiteSlug Slug       `json:"siteSlug"`
	Label    string     `json:"label"`
	Config   SiteConfig `json:"config"`
}

// Config is TS Config (plain secrets).
type Config struct {
	Variables []Variable  `json:"variables"`
	Secrets   []SecretDef `json:"secrets"`
	Files     []FileDef   `json:"files"`
	Sites     []Site      `json:"sites"`
}

// ConfigView is TS ConfigView (masked secrets).
type ConfigView struct {
	Variables []Variable      `json:"variables"`
	Secrets   []SecretDefView `json:"secrets"`
	Files     []FileDef       `json:"files"`
	Sites     []Site          `json:"sites"`
}

// ConfigInput is TS ConfigInput.
type ConfigInput struct {
	Variables []Variable       `json:"variables"`
	Secrets   []SecretDefInput `json:"secrets"`
	Files     []FileDef        `json:"files"`
	Sites     []Site           `json:"sites"`
}

// ───────────────────────── revisions + proposals

// HeadRef is TS HeadRef.
type HeadRef struct {
	RevID      RevID  `json:"revId"`
	Seq        int    `json:"seq"`
	Hash       Sha256 `json:"hash"`
	ReportedAt Ts     `json:"reportedAt"`
}

// RevRef is TS { revId; seq }.
type RevRef struct {
	RevID RevID `json:"revId"`
	Seq   int   `json:"seq"`
}

// HeadHash is TS { revId; seq; hash }.
type HeadHash struct {
	RevID RevID  `json:"revId"`
	Seq   int    `json:"seq"`
	Hash  Sha256 `json:"hash"`
}

// CatalogService is an item of RevisionCatalog.services.
type CatalogService struct {
	ServiceSlug Slug   `json:"serviceSlug"`
	Label       string `json:"label"`
	Icon        *Icon  `json:"icon"` // null = the default service icon; "upload:…" icons are embedded as data:image/… URLs
}

// CatalogNotFoundPage is an item of RevisionCatalog.notFoundPages.
type CatalogNotFoundPage struct {
	NotFoundPageID ID     `json:"notFoundPageId"`
	Label          string `json:"label"`
	HTMLCode       string `json:"htmlCode"`
}

// RevisionCatalog is TS RevisionCatalog (services keyed by serviceSlug; images key = serviceSlug + ":" + version).
type RevisionCatalog struct {
	Services      map[Slug]CatalogService    `json:"services"`
	Images        map[string]ImageSpec       `json:"images"`
	NotFoundPages map[ID]CatalogNotFoundPage `json:"notFoundPages"`
}

// RollbackRef is TS Revision.rollbackOf.
type RollbackRef struct {
	SnapshotID ULID `json:"snapshotId"`
	Seq        int  `json:"seq"`
}

// Revision is TS Revision.
type Revision struct {
	RevID       RevID           `json:"revId"`
	Seq         int             `json:"seq"`
	ParentRevID *RevID          `json:"parentRevId"`
	Origin      string          `json:"origin"` // cloud | agent
	Actor       string          `json:"actor"`
	CreatedAt   Ts              `json:"createdAt"`
	Config      Config          `json:"config"`
	Hash        Sha256          `json:"hash"`
	Catalog     RevisionCatalog `json:"catalog"`
	CatalogHash Sha256          `json:"catalogHash"`
	Summary     string          `json:"summary"`
	Kind        string          `json:"kind"` // change | rollback
	RollbackOf  *RollbackRef    `json:"rollbackOf"`
}

// RevisionView is TS RevisionView.
type RevisionView struct {
	RevID       RevID           `json:"revId"`
	Seq         int             `json:"seq"`
	ParentRevID *RevID          `json:"parentRevId"`
	Origin      string          `json:"origin"`
	Actor       string          `json:"actor"`
	CreatedAt   Ts              `json:"createdAt"`
	Config      ConfigView      `json:"config"`
	Hash        Sha256          `json:"hash"`
	Catalog     RevisionCatalog `json:"catalog"`
	CatalogHash Sha256          `json:"catalogHash"`
	Summary     string          `json:"summary"`
	Kind        string          `json:"kind"`
	RollbackOf  *RollbackRef    `json:"rollbackOf"`
}

// RevisionSummary is TS RevisionSummary.
type RevisionSummary struct {
	RevID       RevID  `json:"revId"`
	Seq         int    `json:"seq"`
	ParentRevID *RevID `json:"parentRevId"`
	Origin      string `json:"origin"`
	Actor       string `json:"actor"`
	CreatedAt   Ts     `json:"createdAt"`
	Hash        Sha256 `json:"hash"`
	Summary     string `json:"summary"`
	Kind        string `json:"kind"`
}

// ProposalStatus values.
const (
	ProposalPublished        = "published"
	ProposalStaging          = "staging"
	ProposalReady            = "ready"
	ProposalApproved         = "approved"
	ProposalApplying         = "applying"
	ProposalApplied          = "applied"
	ProposalFailedRolledBack = "failed-rolled-back"
	ProposalStale            = "stale"
	ProposalSuperseded       = "superseded"
)

// ProposalTerminal is TS PROPOSAL_TERMINAL.
var ProposalTerminal = []string{ProposalApplied, ProposalFailedRolledBack, ProposalStale, ProposalSuperseded}

// IsTerminal reports a terminal proposal status.
func IsTerminal(status string) bool {
	for _, s := range ProposalTerminal {
		if s == status {
			return true
		}
	}
	return false
}

// StagingState values.
const (
	StagingPending     = "pending"
	StagingDownloading = "downloading"
	StagingReady       = "ready"
	StagingFailed      = "failed"
)

// SiteStaging is TS SiteStaging.
type SiteStaging struct {
	Staging   string  `json:"staging"`
	Progress  float64 `json:"progress"`
	Error     *string `json:"error"`
	UpdatedAt *Ts     `json:"updatedAt"`
}

// ProposalResult is TS Proposal.result / UpdateRun.result.
type ProposalResult struct {
	Ok         bool   `json:"ok"`
	Message    string `json:"message"`
	SnapshotID *ULID  `json:"snapshotId"`
}

// Proposal is TS Proposal.
type Proposal struct {
	RevID       RevID                `json:"revId"`
	ParentRevID *RevID               `json:"parentRevId"`
	Seq         int                  `json:"seq"`
	Summary     string               `json:"summary"`
	Origin      string               `json:"origin"`
	Kind        string               `json:"kind"`
	CreatedAt   Ts                   `json:"createdAt"`
	CreatedBy   string               `json:"createdBy"`
	Status      string               `json:"status"`
	Sites       map[Slug]SiteStaging `json:"sites"`
	ApprovedBy  *string              `json:"approvedBy"`
	ApprovedAt  *Ts                  `json:"approvedAt"`
	// ApprovedFastForward (§21 #95, agent-local): the approval wait was fast-forwarded.
	ApprovedFastForward *FastForwardUse `json:"approvedFastForward,omitempty"`
	UpdateID            *ULID           `json:"updateId"`
	Result              *ProposalResult `json:"result"`
	SupersededBy        *RevID          `json:"supersededBy"`
	UpdatedAt           Ts              `json:"updatedAt"`
}

// ProposalMeta is TS ProposalMeta.
type ProposalMeta struct {
	RevID       RevID  `json:"revId"`
	ParentRevID *RevID `json:"parentRevId"`
	Seq         int    `json:"seq"`
	Summary     string `json:"summary"`
	Status      string `json:"status"`
	Origin      string `json:"origin"`
	Kind        string `json:"kind"`
	CreatedAt   Ts     `json:"createdAt"`
	CreatedBy   string `json:"createdBy"`
}

// ───────────────────────── agents

// PeerInfo is TS PeerInfo.
type PeerInfo struct {
	SiteSlug      Slug   `json:"siteSlug"`
	PublicKey     string `json:"publicKey"`
	AdvertiseHost string `json:"advertiseHost"`
	AgentPort     int    `json:"agentPort"`
	EnrolledAt    Ts     `json:"enrolledAt"`
}

// CertStatus is TS CertStatus.
type CertStatus struct {
	SiteSlug      Slug    `json:"siteSlug"`
	Hostname      string  `json:"hostname"`
	Mode          string  `json:"mode"`
	Challenge     *string `json:"challenge"`
	Issuer        *string `json:"issuer"`
	NotBefore     *Ts     `json:"notBefore"`
	NotAfter      *Ts     `json:"notAfter"`
	LastRenewalAt *Ts     `json:"lastRenewalAt"`
	LastError     *string `json:"lastError"`
	NextRenewalAt *Ts     `json:"nextRenewalAt"`
}

// BindError is an item of EdgeReport.bindErrors.
type BindError struct {
	Port    int    `json:"port"`
	Purpose string `json:"purpose"` // edge-http | edge-https | gui-alias | agent
	Error   string `json:"error"`
	// Owner: process holding the port (§21 #86; Linux /proc), nil = unknown
	Owner *string `json:"owner,omitempty"`
}

// GuiPort is TS GuiPort (§21 #86; kind: edge | gui-alias | agent-port).
type GuiPort struct {
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
	Kind   string `json:"kind"`
}

// EdgeReport is TS EdgeReport.
type EdgeReport struct {
	RevSeq          *int         `json:"revSeq"`
	ReloadedAt      *Ts          `json:"reloadedAt"`
	LastReloadError *string      `json:"lastReloadError"`
	BoundPorts      []int        `json:"boundPorts"`
	BindErrors      []BindError  `json:"bindErrors"`
	Certs           []CertStatus `json:"certs"`
	// DefaultEdge: the built-in default edge runs (no head / site not in the head; §21 #86)
	DefaultEdge bool `json:"defaultEdge"`
}

// HeartbeatStaging is TS HeartbeatStaging.
type HeartbeatStaging struct {
	RevID    RevID   `json:"revId"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
}

// HostMetricsSummary is TS HostMetricsSummary (fractions 0..1).
type HostMetricsSummary struct {
	CPU     float64 `json:"cpu"`
	MemPct  float64 `json:"memPct"`
	DiskPct float64 `json:"diskPct"`
	// §21 #112 the agent process itself (the cloud stores metrics as sent; older clouds just keep them)
	AgentRSSBytes *int64   `json:"agentRssBytes,omitempty"`
	AgentCPUPct   *float64 `json:"agentCpuPct,omitempty"`
	AgentWarnings int      `json:"agentWarnings,omitempty"`
}

// PeerSeen is TS { siteSlug; lastSeenAt }.
type PeerSeen struct {
	SiteSlug   Slug `json:"siteSlug"`
	LastSeenAt Ts   `json:"lastSeenAt"`
}

// AgentDoc is TS AgentDoc (cloud document).
type AgentDoc struct {
	SiteSlug      Slug                `json:"siteSlug"`
	PublicKey     string              `json:"publicKey"`
	EnrolledAt    Ts                  `json:"enrolledAt"`
	AgentVersion  string              `json:"agentVersion"`
	LastSeenAt    Ts                  `json:"lastSeenAt"`
	Online        bool                `json:"online"`
	IP            string              `json:"ip"`
	Hostname      string              `json:"hostname"`
	AdvertiseHost string              `json:"advertiseHost"`
	AgentPort     int                 `json:"agentPort"`
	OS            string              `json:"os"`
	Arch          string              `json:"arch"`
	RelayVia      *Slug               `json:"relayVia"`
	HeadRevID     *RevID              `json:"headRevId"`
	Staging       *HeartbeatStaging   `json:"staging"`
	KeyVersion    int                 `json:"keyVersion"`
	AckSeq        int                 `json:"ackSeq"`
	Metrics       *HostMetricsSummary `json:"metrics"`
	Edge          *EdgeReport         `json:"edge"`
	PeersSeen     []PeerSeen          `json:"peersSeen"`
	// DetectedAddresses: non-loopback IPs of the host (IPv4 first) + the hostname; [] on old agents.
	DetectedAddresses []string `json:"detectedAddresses"`
	// GuiPorts: host ports the GUI answers on (§21 #86; absent on old agents)
	GuiPorts []GuiPort `json:"guiPorts,omitempty"`
	// §21 #90 the queued cloud upgrade request + the last reported upgrade progress
	UpgradeRequest *AgentUpgradeRequest  `json:"upgradeRequest,omitempty"`
	Upgrade        *AgentUpgradeProgress `json:"upgrade,omitempty"`
}

// ───────────────────────── activities

// Activity is TS Activity.
type Activity struct {
	ActivityID   string         `json:"activityId"`
	DeploymentID ID             `json:"deploymentId"`
	SiteSlug     string         `json:"siteSlug"`
	Seq          int            `json:"seq"`
	Ts           Ts             `json:"ts"`
	Actor        *string        `json:"actor"`
	Origin       string         `json:"origin"` // agent | cloud | system
	Type         string         `json:"type"`
	Summary      string         `json:"summary"`
	Data         map[string]any `json:"data"`
	PrevHash     *Sha256        `json:"prevHash"`
	Hash         Sha256         `json:"hash"`
	UploadedVia  *string        `json:"uploadedVia"`
	UploadedAt   *Ts            `json:"uploadedAt"`
}

// ActivityTypes is TS ACTIVITY_TYPES.
var ActivityTypes = []string{
	"install.enrolled", "agent.started", "agent.port-bind-failed", "login.succeeded", "login.failed", "logout",
	"actor.declared", "access-key.rotated", "access-key.received", "access-key.revealed", "proposal.published", "proposal.created",
	"proposal.staged", "proposal.stale", "proposal.superseded", "proposal.approved", "update.started",
	"update.snapshot", "update.applied", "update.failed", "update.rolled-back", "update.abandoned", "update.terminated", "update.recovered",
	"config.local-change", "snapshot.created", "snapshot.downloaded", "snapshot.imported", "snapshot.deleted",
	"snapshot.rolled-back", "volume.seeded", "volume.reset-to-seed", "transfer.sent", "transfer.received", "transfer.saved",
	"file.published", "file.fetched", "file.downloaded", "file.saved", "file.copy-dropped", "file.unpublished", // §21 #123
	"secret.revealed",
	"secret.regenerated", "secret.fetched", "internet.tunnel.started", "internet.tunnel.stopped", "internet.tunnel.revoked",
	"internet.tunnel.expired", "debug.downloaded", "edge.reloaded", "edge.reload-failed", "cert.issued",
	"cert.renewed", "cert.failed", "cert.local-ca-downloaded", "site.unreachable", "site.reachable",
	"deployment.created", "deployment.updated", "install-token.created", "install-token.revoked",
	"install-token.revealed", "install-command.revealed", "install-command.regenerated", "agent.revoked", "draft.reset-to-head", "draft.updated", "transfer.deleted", "activities.uploaded",
	"deployment.deleted",
	// §21 #89 operator toolbox
	"terminal.opened", "terminal.closed", "container.restarted", "container.stopped", "container.started",
	"tool.netcheck", "volume.downloaded", "docker.images-pruned",
	// §21 #90 agent self-management
	"agent.upgrade-started", "agent.upgraded", "agent.upgrade-failed", "agent.rolled-back", "agent.restarted",
	"agent.config-changed", "agent.support-bundle-downloaded", "agent.upgrade-requested", "agent.upgrade-cancelled",
	"remote-gui.opened", "remote-gui.session", "agent.remote-gui-changed", // §21 #118
	"agent.home-move-started", "agent.home-moved", "agent.home-move-failed", "agent.home-old-deleted", // §21 #119
	// §21 #94 Template sync (cloud-only types; mirrored for TestActivityTypesMirror)
	"draft.template-synced", "deployment.template-unlinked",
	// §21 #96 volume manager
	"volume.emptied", "volume.copied", "volume.restored", "volume.op-failed", "volume.copy-deleted", "volume.action-forwarded",
	"volume.file-written", "volume.file-uploaded", "volume.file-folder-created", "volume.file-moved", "volume.file-deleted",
	// §21 #101 volume content + persist-to-cloud
	"volume.placeholder-filled", "volume-content.snapshot", "volume-content.snapshot-uploaded", "volume-content.snapshot-failed",
	"volume-content.conflict", "volume-content.applied", "volume-content.kept-live", "volume-content.apply-failed",
	"volume-content.version-created", "volume-content.snapshot-requested", "volume-content.sent", "volume-content.push-cancelled",
	// §21 #97 dev tools
	"devtools.query", "devtools.query-cancelled", "devtools.connection-created", "devtools.connection-updated",
	"devtools.connection-deleted", "devtools.connection-tested", "devtools.credential-revealed", "devtools.mqtt-published",
	"devtools.mqtt-subscribed", "devtools.mqtt-unsubscribed", "devtools.action-forwarded",
	// §21 #104 branches + commits
	"branch.created", "branch.committed", "branch.merged", "branch.cherry-picked", "branch.reverted", "branch.reset",
	"branch.merge-failed", "working.discarded", "drift.detected", "drift.committed", "drift.discarded", "drift.accepted",
	"agent-branch.pushed",
}

// ───────────────────────── transfers + uploads

// Transfer is TS Transfer.
type Transfer struct {
	TransferID   ID      `json:"transferId"`
	DeploymentID ID      `json:"deploymentId"`
	SiteSlug     Slug    `json:"siteSlug"`
	Direction    string  `json:"direction"` // to-site | to-cloud
	FileName     string  `json:"fileName"`
	SizeBytes    int64   `json:"sizeBytes"`
	Sha256       Sha256  `json:"sha256"`
	StoragePath  string  `json:"storagePath"`
	UploadID     *ID     `json:"uploadId"`
	CreatedAt    Ts      `json:"createdAt"`
	CreatedBy    string  `json:"createdBy"`
	Note         string  `json:"note"`
	ExpiresAt    *Ts     `json:"expiresAt"` // null: transfers never expire (a date on older rows is ignored)
	Status       string  `json:"status"`
	SavedPath    *string `json:"savedPath"`
	Error        *string `json:"error"`
	// §21 #100
	FetchedAt  *Ts               `json:"fetchedAt,omitempty"`  // cloud, to-site: stored on the site
	SourcePath *string           `json:"sourcePath,omitempty"` // agent, to-cloud: sent from this server path
	Delivery   *TransferDelivery `json:"delivery,omitempty"`   // agent API only (never stored in the cloud)
}

// TransferDelivery is TS TransferDelivery (§21 #100 store-and-forward state on the agent).
type TransferDelivery struct {
	Phase         string  `json:"phase"` // queued | uploading | stored | pending | downloading | on-site
	BytesDone     int64   `json:"bytesDone"`
	Attempts      int     `json:"attempts"`
	LastError     *string `json:"lastError"`
	LastAttemptAt *Ts     `json:"lastAttemptAt"`
	NextAttemptAt *Ts     `json:"nextAttemptAt"`
}

// HostFsEntry is TS HostFsEntry (§21 #100).
type HostFsEntry struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Type       string  `json:"type"`       // dir | file | symlink | other
	TargetType *string `json:"targetType"` // symlinks: dir | file | other | missing
	LinkTarget *string `json:"linkTarget"`
	SizeBytes  *int64  `json:"sizeBytes"`
	Mtime      *Ts     `json:"mtime"`
	Mode       string  `json:"mode"`
	Hidden     bool    `json:"hidden"`
	Readable   bool    `json:"readable"`
	Blocked    *string `json:"blocked"`
}

// HostFsListResponse is TS HostFsListResponse (§21 #100).
type HostFsListResponse struct {
	Path        string        `json:"path"`
	Parent      *string       `json:"parent"`
	Entries     []HostFsEntry `json:"entries"`
	Truncated   bool          `json:"truncated"`
	HiddenCount int           `json:"hiddenCount"`
	AgentUser   string        `json:"agentUser"`
	Blocked     *string       `json:"blocked"`
}

// HostFsMaxEntries is TS HOST_FS_MAX_ENTRIES.
const HostFsMaxEntries = 5000

// TransferMeta is TS TransferMeta.
type TransferMeta struct {
	TransferID ID     `json:"transferId"`
	SiteSlug   Slug   `json:"siteSlug"`
	Direction  string `json:"direction"`
	FileName   string `json:"fileName"`
	SizeBytes  int64  `json:"sizeBytes"`
	Sha256     Sha256 `json:"sha256"`
	Status     string `json:"status"`
	CreatedAt  Ts     `json:"createdAt"`
	CreatedBy  string `json:"createdBy"`
	Note       string `json:"note"`
	ExpiresAt  *Ts    `json:"expiresAt"` // always null: transfers never expire
}

// Upload is TS Upload.
type Upload struct {
	UploadID      ID      `json:"uploadId"`
	Purpose       string  `json:"purpose"` // blob | transfer
	DeploymentID  *ID     `json:"deploymentId"`
	TransferID    *ID     `json:"transferId"`
	SizeBytes     int64   `json:"sizeBytes"`
	Sha256        Sha256  `json:"sha256"`
	PartSize      int64   `json:"partSize"`
	PartCount     int     `json:"partCount"`
	PartsReceived []int   `json:"partsReceived"`
	Status        string  `json:"status"` // open | complete | failed
	Error         *string `json:"error"`
	CreatedAt     Ts      `json:"createdAt"`
	CreatedBy     string  `json:"createdBy"`
	ExpiresAt     Ts      `json:"expiresAt"`
}

// Upload limits (TS UPLOAD_PART_SIZE, SMALL_BLOB_MAX, TRANSFER_MAX).
const (
	UploadPartSize = 8 * 1024 * 1024
	SmallBlobMax   = 24 * 1024 * 1024
	TransferMax    = 2 * 1024 * 1024 * 1024
)

// ───────────────────────── cloud view + mesh

// AccessKeyRef is TS { hash; version }.
type AccessKeyRef struct {
	Hash    string `json:"hash"`
	Version int    `json:"version"`
}

// CloudView is TS CloudView (= the poll response).
type CloudView struct {
	FetchedAt       Ts             `json:"fetchedAt"`
	Source          string         `json:"source"` // cloud | peer:<slug>
	Cursor          string         `json:"cursor"`
	DeploymentID    ID             `json:"deploymentId"`
	Deleted         bool           `json:"deleted"`
	Proposals       []ProposalMeta `json:"proposals"`
	TransfersToSite []TransferMeta `json:"transfersToSite"`
	DraftHead       *RevRef        `json:"draftHead"`
	AccessKey       AccessKeyRef   `json:"accessKey"`
	Peers           []PeerInfo     `json:"peers"`
	// optional (§21 #86 add-on): console origin + deployment label for the agent GUI
	ConsoleURL      *string `json:"consoleUrl,omitempty"`
	DeploymentLabel *string `json:"deploymentLabel,omitempty"`
	// §21 #90 the pinned agent release + queued cloud upgrade requests (absent on old clouds)
	AgentRelease  *AgentReleaseInfo     `json:"agentRelease,omitempty"`
	AgentUpgrades []AgentUpgradeRequest `json:"agentUpgrades,omitempty"`
	// §21 #101 persist-to-cloud volumes with a pending push or an open snapshot request (all sites; absent on old clouds)
	VolumeContent []VolumeContentCloudItem `json:"volumeContent,omitempty"`
	// §21 #104 the deployment branch head + newest commits (absent on old clouds → legacy proposal mode)
	Branch *CloudBranchView `json:"branch,omitempty"`
	// §21 #118 sites with an open remote GUI session (absent on old clouds)
	RemoteGui []RemoteGuiCloudItem `json:"remoteGui,omitempty"`
	// §21 #123 shared files: the listing (+ tombstones) and the copies the cloud wants from a site; nil on old clouds
	Files       []SharedFile    `json:"files,omitempty"`
	FileFetches []FileFetchItem `json:"fileFetches,omitempty"`
	// to-site transfers deleted in the cloud (all sites; each agent drops its own copy; absent on old clouds)
	DeletedTransfersToSite []DeletedTransfer `json:"deletedTransfersToSite,omitempty"`
}

// DeletedTransfer is one entry of TS CloudView.deletedTransfersToSite.
type DeletedTransfer struct {
	TransferID ID   `json:"transferId"`
	SiteSlug   Slug `json:"siteSlug"`
}

// HaveList is TS HaveList.
type HaveList struct {
	SiteSlug    Slug     `json:"siteSlug"`
	GeneratedAt Ts       `json:"generatedAt"`
	Internet    bool     `json:"internet"`
	Blobs       []Sha256 `json:"blobs"`
}

// FetchRequest is TS FetchRequest (Kind: layer | config | manifest | file).
type FetchRequest struct {
	Sha256    Sha256   `json:"sha256"`
	SizeBytes int64    `json:"sizeBytes"`
	Kind      string   `json:"kind"`
	Ref       FetchRef `json:"ref"`
}

// FetchRequestResponse is TS FetchRequestResponse.
type FetchRequestResponse struct {
	Fetcher Slug    `json:"fetcher"`
	Status  string  `json:"status"` // queued | downloading | have | failed
	Error   *string `json:"error"`
}

// UpdateLock is TS UpdateLock.
type UpdateLock struct {
	UpdateID      ULID   `json:"updateId"`
	Kind          string `json:"kind"` // update | rollback
	RevID         RevID  `json:"revId"`
	Coordinator   Slug   `json:"coordinator"`
	StartedAt     Ts     `json:"startedAt"`
	LastContactAt Ts     `json:"lastContactAt"`
}

// PeerProposalState is an item of PeerPing.proposals.
type PeerProposalState struct {
	RevID   RevID       `json:"revId"`
	Status  string      `json:"status"`
	Staging SiteStaging `json:"staging"`
}

// PeerEdge is PeerPing.edge.
type PeerEdge struct {
	Running   bool    `json:"running"`
	HTTPPort  *int    `json:"httpPort"`
	HTTPSPort *int    `json:"httpsPort"`
	LastError *string `json:"lastError"`
}

// PeerPing is TS PeerPing.
type PeerPing struct {
	DeploymentID   ID                  `json:"deploymentId"`
	SiteSlug       Slug                `json:"siteSlug"`
	AgentVersion   string              `json:"agentVersion"`
	ServerTime     Ts                  `json:"serverTime"`
	Head           *HeadHash           `json:"head"`
	Lock           *UpdateLock         `json:"lock"`
	Proposals      []PeerProposalState `json:"proposals"`
	Internet       bool                `json:"internet"`
	CloudRoute     string              `json:"cloudRoute"` // direct | relay | tunnel | none
	CloudFetchedAt *Ts                 `json:"cloudFetchedAt"`
	KeyVersion     int                 `json:"keyVersion"`
	AgentPort      int                 `json:"agentPort"`
	GuiAliasPort   *int                `json:"guiAliasPort"`
	Edge           PeerEdge            `json:"edge"`
	InConfig       bool                `json:"inConfig"`
	// §21 #104 agents ≥ 0.14
	Branch *AgentBranchReport `json:"branch,omitempty"`
}

// UpdateStep names.
const (
	StepLock     = "lock"
	StepSnapshot = "snapshot"
	StepApply    = "apply"
	StepVerify   = "verify"
	StepCommit   = "commit"
	StepRollback = "rollback"
	StepUnlock   = "unlock"
)

// UpdatePlan is TS UpdatePlan.
type UpdatePlan struct {
	UpdateID         ULID     `json:"updateId"`
	Kind             string   `json:"kind"`
	Revision         Revision `json:"revision"`
	FromRevID        *RevID   `json:"fromRevId"`
	PreSnapshotID    ULID     `json:"preSnapshotId"`
	TargetSnapshotID *ULID    `json:"targetSnapshotId"`
	Sites            []Slug   `json:"sites"`
	Order            []Slug   `json:"order"`
	Coordinator      Slug     `json:"coordinator"`
	ApprovedBy       string   `json:"approvedBy"`
	ApprovedAt       Ts       `json:"approvedAt"`
	// §21 #104 per-site targets + the commit each site writes at COMMIT (0.14 merge plans)
	Targets map[Slug]UpdatePlanTarget `json:"targets,omitempty"`
	Commits map[Slug]CommitMeta       `json:"commits,omitempty"`
}

// RevisionFor is the target revision of a site (Targets, else Revision).
func (p *UpdatePlan) RevisionFor(site string) *Revision {
	if t, ok := p.Targets[site]; ok {
		return &t.Revision
	}
	return &p.Revision
}

// FromFor is the head a site must have at LOCK.
func (p *UpdatePlan) FromFor(site string) *RevID {
	if t, ok := p.Targets[site]; ok {
		return t.FromRevID
	}
	return p.FromRevID
}

// UpdateStepRequest is TS UpdateStepRequest.
type UpdateStepRequest struct {
	Step string      `json:"step"`
	Plan *UpdatePlan `json:"plan,omitempty"`
}

// UpdateContainerState is an item of UpdateSiteState.containers.
type UpdateContainerState struct {
	ContainerSlug Slug    `json:"containerSlug"`
	ServiceSlug   Slug    `json:"serviceSlug"`
	Action        string  `json:"action"` // keep | recreate | create | remove | stop
	State         string  `json:"state"`  // pending | running | ok | failed
	Error         *string `json:"error"`
	// Failure: diagnostics captured before the rollback (§21 #87)
	Failure *ContainerFailure `json:"failure,omitempty"`
}

// HealthCheckRecord is TS HealthCheckRecord.
type HealthCheckRecord struct {
	At      Ts     `json:"at"`
	OK      bool   `json:"ok"`
	Counted bool   `json:"counted"`
	Message string `json:"message"`
}

// ContainerFailureState is ContainerFailure.state.
type ContainerFailureState struct {
	Status       string `json:"status"`
	ExitCode     int    `json:"exitCode"`
	OOMKilled    bool   `json:"oomKilled"`
	RestartCount int    `json:"restartCount"`
	Error        string `json:"error"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
}

// ContainerFailure is TS ContainerFailure (§21 #87).
type ContainerFailure struct {
	ContainerSlug Slug                   `json:"containerSlug"`
	DockerName    string                 `json:"dockerName"`
	KeptAs        *string                `json:"keptAs"`
	Error         string                 `json:"error"`
	State         *ContainerFailureState `json:"state"`
	Ports         string                 `json:"ports"`
	LogTail       []string               `json:"logTail"`
	HealthHistory []HealthCheckRecord    `json:"healthHistory"`
	CapturedAt    Ts                     `json:"capturedAt"`
	// Hint (§21 #93): DNS-failure hint naming a container of this site; nil = none
	Hint *string `json:"hint,omitempty"`
}

// UpdateSiteState is TS UpdateSiteState.
type UpdateSiteState struct {
	UpdateID      ULID                   `json:"updateId"`
	SiteSlug      Slug                   `json:"siteSlug"`
	Step          *string                `json:"step"`
	State         string                 `json:"state"` // idle | running | ok | failed
	Error         *string                `json:"error"`
	PreSnapshotID *ULID                  `json:"preSnapshotId"`
	StartedAt     *Ts                    `json:"startedAt"`
	FinishedAt    *Ts                    `json:"finishedAt"`
	Containers    []UpdateContainerState `json:"containers"`
}

// UpdateRun is TS UpdateRun.
type UpdateRun struct {
	UpdateID    ULID                     `json:"updateId"`
	Kind        string                   `json:"kind"`
	RevID       RevID                    `json:"revId"`
	Coordinator Slug                     `json:"coordinator"`
	ApprovedBy  string                   `json:"approvedBy"`
	ApprovedAt  Ts                       `json:"approvedAt"`
	Phase       string                   `json:"phase"` // a step name | done | failed
	Result      *ProposalResult          `json:"result"`
	Sites       map[Slug]UpdateSiteState `json:"sites"`
	StartedAt   Ts                       `json:"startedAt"`
	FinishedAt  *Ts                      `json:"finishedAt"`
	// FastForward (§21 #95): the approval / rollback wait was fast-forwarded.
	FastForward *FastForwardUse `json:"fastForward,omitempty"`
}

// ApproveIntent is TS ApproveIntent.
type ApproveIntent struct {
	IntentID   ULID   `json:"intentId"`
	Kind       string `json:"kind"`
	RevID      *RevID `json:"revId"`
	SnapshotID *ULID  `json:"snapshotId"`
	Actor      string `json:"actor"`
	SessionID  ULID   `json:"sessionId"`
	NotBefore  Ts     `json:"notBefore"`
	ExpiresAt  Ts     `json:"expiresAt"`
	// Volume (round E, kind volume-reset): the volume to reset to its starter content.
	Volume *IntentVolume `json:"volume,omitempty"`
	// WaitSec (§21 #95): the effective wait the agent requires.
	WaitSec float64 `json:"waitSec,omitempty"`
}

// Hold-to-fast-forward of an intent wait (§21 #95; TS intent-wait.ts).
const (
	IntentMaxSpeed         = 4
	IntentSpeedHeartbeatMs = 500
	IntentSpeedDecayMs     = 1500
)

// IntentSpeedRequest is TS IntentSpeedRequest.
type IntentSpeedRequest struct {
	Speed float64 `json:"speed"`
}

// IntentProgress is TS IntentProgress.
type IntentProgress struct {
	IntentID     ULID    `json:"intentId"`
	Kind         string  `json:"kind"`
	WaitSec      float64 `json:"waitSec"`
	EffectiveSec float64 `json:"effectiveSec"`
	RemainingSec float64 `json:"remainingSec"`
	Speed        int     `json:"speed"`
	SpeedUntil   *Ts     `json:"speedUntil"`
	MaxSpeed     int     `json:"maxSpeed"`
	Ready        bool    `json:"ready"`
	CreatedAt    Ts      `json:"createdAt"`
	ServerTime   Ts      `json:"serverTime"`
}

// FastForwardUse is TS FastForwardUse.
type FastForwardUse struct {
	UsedBy           string  `json:"usedBy"`
	MaxSpeed         int     `json:"maxSpeed"`
	RealSeconds      float64 `json:"realSeconds"`
	EffectiveSeconds float64 `json:"effectiveSeconds"`
}

// IntentVolume is ApproveIntent.volume.
type IntentVolume struct {
	SiteSlug   Slug   `json:"siteSlug"`
	VolumeSlug string `json:"volumeSlug"`
}

// GateCondition is an item of GateCheck.conditions (key: parent | reachable | staged | idle | status).
type GateCondition struct {
	Key    string `json:"key"`
	Ok     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Warn: ok, but worth a look (§21 #110: a running update never closes a gate; starting stops it)
	Warn bool `json:"warn,omitempty"`
}

// GateCheck is TS GateCheck.
type GateCheck struct {
	Open       bool            `json:"open"`
	Conditions []GateCondition `json:"conditions"`
	Sites      []Slug          `json:"sites"`
}

// ───────────────────────── snapshots + bundles

// SnapshotVolume is TS SnapshotVolume.
type SnapshotVolume struct {
	VolumeSlug string `json:"volumeSlug"` // the declared site volume; restore is keyed by it
	Containers []Slug `json:"containers"` // containerSlugs that mount it at snapshot time (informational)
	SizeBytes  int64  `json:"sizeBytes"`
	Sha256     Sha256 `json:"sha256"`
	Path       string `json:"path"` // "sites/<siteSlug>/volumes/<volumeSlug>.tar.zst"
}

// UnmarshalJSON also reads snapshots written before round D, which carry `name` instead of `volumeSlug`.
func (v *SnapshotVolume) UnmarshalJSON(b []byte) error {
	type plain SnapshotVolume
	var w struct {
		plain
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*v = SnapshotVolume(w.plain)
	if v.VolumeSlug == "" {
		v.VolumeSlug = w.Name
	}
	return nil
}

// SnapshotImage is an item of SnapshotSite.images.
type SnapshotImage struct {
	ServiceSlug  Slug   `json:"serviceSlug"`
	Version      string `json:"version"`
	Digest       Sha256 `json:"digest"`
	ConfigDigest Sha256 `json:"configDigest"`
}

// SnapshotSite is TS SnapshotSite.
type SnapshotSite struct {
	Config    Config           `json:"config"`
	Images    []SnapshotImage  `json:"images"`
	Volumes   []SnapshotVolume `json:"volumes"`
	SizeBytes int64            `json:"sizeBytes"`
}

// Snapshot is TS Snapshot.
type Snapshot struct {
	SnapshotID ULID                  `json:"snapshotId"`
	CreatedAt  Ts                    `json:"createdAt"`
	Reason     string                `json:"reason"` // pre-update | manual | scheduled | imported
	RevID      *RevID                `json:"revId"`
	Seq        int                   `json:"seq"`
	Sites      map[Slug]SnapshotSite `json:"sites"`
	CreatedBy  string                `json:"createdBy"`
	Note       string                `json:"note"`
	Revision   *Revision             `json:"revision"`
}

// SnapshotSummary is TS SnapshotSummary.
type SnapshotSummary struct {
	SnapshotID   ULID   `json:"snapshotId"`
	CreatedAt    Ts     `json:"createdAt"`
	Reason       string `json:"reason"`
	RevID        *RevID `json:"revId"`
	Seq          int    `json:"seq"`
	CreatedBy    string `json:"createdBy"`
	Note         string `json:"note"`
	SizeBytes    int64  `json:"sizeBytes"`
	Sites        []Slug `json:"sites"`
	MissingSites []Slug `json:"missingSites"`
	HasImages    bool   `json:"hasImages"`
}

// BundlePart is a file entry (path, size, sha256) of a bundle manifest.
type BundlePart struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Sha256    Sha256 `json:"sha256"`
}

// SignedBy is the signer of a bundle.
type SignedBy struct {
	SiteSlug  Slug   `json:"siteSlug"`
	PublicKey string `json:"publicKey"`
}

// SnapImageBlobs is an item of OpmsnapManifest.images.
type SnapImageBlobs struct {
	ServiceSlug  Slug     `json:"serviceSlug"`
	Version      string   `json:"version"`
	Digest       Sha256   `json:"digest"`
	ConfigDigest Sha256   `json:"configDigest"`
	Blobs        []Sha256 `json:"blobs"`
}

// OpmsnapManifest is TS OpmsnapManifest (format "opmsnap/1").
type OpmsnapManifest struct {
	Format        string           `json:"format"`
	DeploymentID  ID               `json:"deploymentId"`
	Snapshot      Snapshot         `json:"snapshot"`
	Parts         []BundlePart     `json:"parts"`
	IncludeImages bool             `json:"includeImages"`
	Images        []SnapImageBlobs `json:"images"`
	CreatedAt     Ts               `json:"createdAt"`
	CreatedBy     string           `json:"createdBy"`
	SignedBy      SignedBy         `json:"signedBy"`
}

// LogSiteRange is an item of OpmlogManifest.sites.
type LogSiteRange struct {
	SiteSlug Slug `json:"siteSlug"`
	FromSeq  int  `json:"fromSeq"`
	ToSeq    int  `json:"toSeq"`
	Count    int  `json:"count"`
}

// LogDebugRef is an item of OpmlogManifest.debug.
type LogDebugRef struct {
	SiteSlug  Slug   `json:"siteSlug"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Sha256    Sha256 `json:"sha256"`
}

// OpmlogManifest is TS OpmlogManifest (format "opmlog/1"; activities.path = "activities.ndjson").
type OpmlogManifest struct {
	Format       string         `json:"format"`
	DeploymentID ID             `json:"deploymentId"`
	CreatedAt    Ts             `json:"createdAt"`
	CreatedBy    string         `json:"createdBy"`
	SignedBy     SignedBy       `json:"signedBy"`
	Sites        []LogSiteRange `json:"sites"`
	Activities   BundlePart     `json:"activities"`
	Debug        []LogDebugRef  `json:"debug"`
}

// ───────────────────────── tunnel, actors, sessions

// TunnelLease is TS TunnelLease.
type TunnelLease struct {
	LeaseID    ULID    `json:"leaseId"`
	Actor      string  `json:"actor"`
	SiteSlug   Slug    `json:"siteSlug"`
	UserAgent  string  `json:"userAgent"`
	StartedAt  Ts      `json:"startedAt"`
	LastPollAt Ts      `json:"lastPollAt"`
	Status     string  `json:"status"` // active | stopped | revoked | expired
	Reqs       int     `json:"reqs"`
	Bytes      int64   `json:"bytes"`
	LastError  *string `json:"lastError"`
}

// TunnelRequest is TS TunnelRequest.
type TunnelRequest struct {
	ReqID      ULID              `json:"reqId"`
	Method     string            `json:"method"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	BodyB64    *string           `json:"bodyB64"`
	CreatedAt  Ts                `json:"createdAt"`
	TimeoutSec int               `json:"timeoutSec"`
}

// TunnelResult is TS TunnelResult.
type TunnelResult struct {
	LeaseID ULID              `json:"leaseId"`
	ReqID   ULID              `json:"reqId"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 *string           `json:"bodyB64"`
	Chunked bool              `json:"chunked"`
	Error   *string           `json:"error"`
}

// ActorTokenClaims is TS ActorTokenClaims (JWT payload).
type ActorTokenClaims struct {
	Email string `json:"email"`
	Sid   ULID   `json:"sid"`
	Dep   ID     `json:"dep"`
	Site  Slug   `json:"site"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

// ActorRow is TS ActorRow.
type ActorRow struct {
	Email      string `json:"email"`
	LastUsedAt Ts     `json:"lastUsedAt"`
	Count      int    `json:"count"`
}

// AgentSession is TS AgentSession.
type AgentSession struct {
	SessionID  ULID   `json:"sessionId"`
	CreatedAt  Ts     `json:"createdAt"`
	ExpiresAt  Ts     `json:"expiresAt"`
	KeyVersion int    `json:"keyVersion"`
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
}

// ───────────────────────── status + logs

// ContainerPortStatus is an item of ContainerStatus.ports.
type ContainerPortStatus struct {
	PortName      Slug   `json:"portName"`
	ContainerPort int    `json:"containerPort"`
	HostPort      *int   `json:"hostPort"`
	Protocol      string `json:"protocol"`
}

// ContainerStatus is TS ContainerStatus.
type ContainerStatus struct {
	SiteSlug Slug `json:"siteSlug"`
	// managed: the containerSlug; unmanaged: the docker container name
	ContainerSlug Slug                  `json:"containerSlug"`
	ServiceSlug   Slug                  `json:"serviceSlug"`
	Version       string                `json:"version"`
	Image         string                `json:"image"` // the readable docker reference on the site: <serviceSlug>:<version>
	Kind          string                `json:"kind"`
	Enabled       bool                  `json:"enabled"`
	Managed       bool                  `json:"managed"`
	State         string                `json:"state"`
	ExitCode      *int                  `json:"exitCode"`
	Health        string                `json:"health"`
	Ready         bool                  `json:"ready"`
	Restarts      int                   `json:"restarts"`
	CPUPct        *float64              `json:"cpuPct"`
	MemMb         *float64              `json:"memMb"`
	StartedAt     *Ts                   `json:"startedAt"`
	Ports         []ContainerPortStatus `json:"ports"`
	SpecHash      *string               `json:"specHash"`
	RevSeq        *int                  `json:"revSeq"`
}

// HostMetrics is TS HostMetrics.
type HostMetrics struct {
	CPUPct      float64  `json:"cpuPct"`
	MemUsedMb   float64  `json:"memUsedMb"`
	MemTotalMb  float64  `json:"memTotalMb"`
	DiskUsedPct float64  `json:"diskUsedPct"`
	DiskFreeMb  float64  `json:"diskFreeMb"`
	LoadAvg1    *float64 `json:"loadAvg1"`
}

// UptimeCell is TS UptimeCell.
type UptimeCell struct {
	T       Ts      `json:"t"`
	Up      float64 `json:"up"`
	Samples int     `json:"samples"`
}

// UptimeSeries is TS { subject; cells }.
type UptimeSeries struct {
	Subject string       `json:"subject"`
	Cells   []UptimeCell `json:"cells"`
}

// SiteStatus is TS SiteStatus.
type SiteStatus struct {
	SiteSlug     Slug              `json:"siteSlug"`
	Reachable    bool              `json:"reachable"`
	LastSeenAt   *Ts               `json:"lastSeenAt"`
	AgentVersion *string           `json:"agentVersion"`
	Host         *HostMetrics      `json:"host"`
	Containers   []ContainerStatus `json:"containers"`
	Uptime24h    []UptimeSeries    `json:"uptime24h"`
	Uptime30d    []UptimeSeries    `json:"uptime30d"`
	Certs        []CertStatus      `json:"certs"`
	Edge         *EdgeReport       `json:"edge"`
	// AgentWarnings (§21 #112): the agent's own problems (disk floor / snapshot budget, store writes, cloud key, dead
	// letters, stuck loops); omitted when none
	AgentWarnings []string `json:"agentWarnings,omitempty"`
}

// DebugLine is TS DebugLine.
type DebugLine struct {
	Ts     Ts             `json:"ts"`
	Level  string         `json:"level"`
	Module string         `json:"module"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields"`
	Site   string         `json:"site,omitempty"`
}

// AgentConfigFile is TS AgentConfigFile (/etc/opm/agent.json).
type AgentConfigFile struct {
	// SnapshotBudgetPct (§21 #112): share of the agent-home filesystem snapshots may use (1 … 90; absent = 30)
	SnapshotBudgetPct *int             `json:"snapshotBudgetPct,omitempty"`
	APIBase           string           `json:"apiBase"`
	DeploymentID      ID               `json:"deploymentId"`
	SiteSlug          Slug             `json:"siteSlug"`
	DataDir           string           `json:"dataDir,omitempty"`
	AgentPort         int              `json:"agentPort,omitempty"`
	GuiAliasPort      Optional[int]    `json:"guiAliasPort"`
	AdvertiseHost     string           `json:"advertiseHost,omitempty"`
	DockerHost        string           `json:"dockerHost,omitempty"`
	Proxy             Optional[string] `json:"proxy"`
	InstallTokenFile  string           `json:"installTokenFile,omitempty"`
	// §21 #86 HTTP port of the built-in default edge (no head / site not in the head): default 80; null = none
	EdgeHTTPPort Optional[int] `json:"edgeHttpPort"`
	// release signing public key (base64), written by the install script (opm-agent upgrade)
	ReleasePublicKey string `json:"releasePublicKey,omitempty"`
	// cloud console origin (fallback when the cloud does not send consoleUrl)
	ConsoleURL string `json:"consoleUrl,omitempty"`
	// §21 #88 docker container names: "plain" (default; the name = the containerSlug) | "prefixed"
	// (opm_<deploymentId>_<siteSlug>_<containerSlug>, for several sites on one Docker host)
	ContainerNames string `json:"containerNames,omitempty"`
	// §21 #89 operator toolbox switches (defaults true) + host shell user / shell
	Tools     *AgentConfigTools     `json:"tools,omitempty"`
	HostShell *AgentConfigHostShell `json:"hostShell,omitempty"`
}

// AgentConfigTools is AgentConfigFile.tools.
type AgentConfigTools struct {
	HostShell     *bool `json:"hostShell,omitempty"`
	ContainerExec *bool `json:"containerExec,omitempty"`
}

// AgentConfigHostShell is AgentConfigFile.hostShell.
type AgentConfigHostShell struct {
	User  string `json:"user,omitempty"`
	Shell string `json:"shell,omitempty"`
}

// MarshalJSON omits unset optional nullable fields.
func (c AgentConfigFile) MarshalJSON() ([]byte, error) {
	type alias AgentConfigFile
	return marshalOmitUnset(alias(c), map[string]bool{"guiAliasPort": !c.GuiAliasPort.Set, "proxy": !c.Proxy.Set, "edgeHttpPort": !c.EdgeHTTPPort.Set})
}

// UptimeDay is TS UptimeDay.
type UptimeDay struct {
	SiteSlug  Slug      `json:"siteSlug"`
	Date      string    `json:"date"`
	Beats     []float64 `json:"beats"`
	Hours     []float64 `json:"hours"`
	UpdatedAt Ts        `json:"updatedAt"`
}

// ───────────────────────── §21 #96 volume manager (TS VolumeOp & co.)

// VolumeOpStep is TS VolumeOpStep.
type VolumeOpStep struct {
	Name       string  `json:"name"`
	State      string  `json:"state"` // pending | running | done | skipped | failed
	Detail     *string `json:"detail"`
	StartedAt  *Ts     `json:"startedAt"`
	FinishedAt *Ts     `json:"finishedAt"`
}

// VolumeOpTarget is TS VolumeOpTarget.
type VolumeOpTarget struct {
	Kind       string  `json:"kind"`
	Path       string  `json:"path"`
	VolumeSlug *string `json:"volumeSlug"`
}

// VolumeOpSource is TS VolumeOpSource.
type VolumeOpSource struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

// VolumeOpProgress is TS VolumeOp.progress.
type VolumeOpProgress struct {
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal *int64 `json:"bytesTotal"`
	Files      int64  `json:"files"`
}

// VolumeOp is TS VolumeOp.
type VolumeOp struct {
	OpID        ULID             `json:"opId"`
	SiteSlug    Slug             `json:"siteSlug"`
	VolumeSlug  string           `json:"volumeSlug"`
	VolumeKind  string           `json:"volumeKind"`
	Kind        string           `json:"kind"`
	Actor       string           `json:"actor"`
	StartedAt   Ts               `json:"startedAt"`
	FinishedAt  *Ts              `json:"finishedAt"`
	Phase       string           `json:"phase"`
	Ok          *bool            `json:"ok"`
	Error       *string          `json:"error"`
	Steps       []VolumeOpStep   `json:"steps"`
	Progress    VolumeOpProgress `json:"progress"`
	Stopped     []string         `json:"stopped"`
	Target      *VolumeOpTarget  `json:"target"`
	Source      *VolumeOpSource  `json:"source"`
	SnapshotID  *ULID            `json:"snapshotId"`
	CopyID      *string          `json:"copyId"`
	ResultBytes *int64           `json:"resultBytes"`
	DurationMs  *int64           `json:"durationMs"`
	Warnings    []string         `json:"warnings"`
	FastForward *FastForwardUse  `json:"fastForward,omitempty"`
}

// VolumeEmptyRequest is TS VolumeEmptyRequest.
type VolumeEmptyRequest struct {
	IntentID ULID   `json:"intentId"`
	Typed    string `json:"typed"`
	Snapshot bool   `json:"snapshot"`
}

// VolumeCopyTargetInput is TS VolumeCopyTargetInput.
type VolumeCopyTargetInput struct {
	Kind       string  `json:"kind"`
	Path       *string `json:"path,omitempty"`
	VolumeSlug string  `json:"volumeSlug,omitempty"`
	Overwrite  *string `json:"overwrite,omitempty"`
}

// VolumeCopyRequest is TS VolumeCopyRequest.
type VolumeCopyRequest struct {
	Target         VolumeCopyTargetInput `json:"target"`
	StopContainers bool                  `json:"stopContainers"`
	Preserve       bool                  `json:"preserve"`
}

// VolumeRestoreSourceInput is TS VolumeRestoreRequest.source.
type VolumeRestoreSourceInput struct {
	Kind     string `json:"kind"`
	CopyID   string `json:"copyId,omitempty"`
	UploadID string `json:"uploadId,omitempty"`
	// SnapshotID (§21 #112 kind "snapshot"): a snapshot on this site that holds the volume
	SnapshotID string `json:"snapshotId,omitempty"`
}

// VolumeRestoreRequest is TS VolumeRestoreRequest.
type VolumeRestoreRequest struct {
	IntentID ULID                     `json:"intentId"`
	Typed    string                   `json:"typed"`
	Source   VolumeRestoreSourceInput `json:"source"`
}

// VolumeRestoreUpload is TS VolumeRestoreUpload.
type VolumeRestoreUpload struct {
	UploadID  string `json:"uploadId"`
	SizeBytes int64  `json:"sizeBytes"`
	Entries   int    `json:"entries"`
	ExpiresAt Ts     `json:"expiresAt"`
}

// VolumeSnapshotRequest is TS VolumeSnapshotRequest.
type VolumeSnapshotRequest struct {
	Note string `json:"note,omitempty"`
}

// VolumeSnapshotResponse is TS VolumeSnapshotResponse.
type VolumeSnapshotResponse struct {
	SnapshotID ULID  `json:"snapshotId"`
	SizeBytes  int64 `json:"sizeBytes"`
}

// VolumeCopy is TS VolumeCopy.
type VolumeCopy struct {
	CopyID      string `json:"copyId"`
	SiteSlug    Slug   `json:"siteSlug"`
	VolumeSlug  string `json:"volumeSlug"`
	VolumeKind  string `json:"volumeKind"`
	Path        string `json:"path"`
	InCopiesDir bool   `json:"inCopiesDir"`
	Exists      bool   `json:"exists"`
	SizeBytes   *int64 `json:"sizeBytes"`
	Files       *int64 `json:"files"`
	CreatedAt   Ts     `json:"createdAt"`
	CreatedBy   string `json:"createdBy"`
	OpID        *ULID  `json:"opId"`
}

// DeleteVolumeCopyRequest is TS DeleteVolumeCopyRequest.
type DeleteVolumeCopyRequest struct {
	Confirm string `json:"confirm"`
}

// VolumeOpRatePerMinute is TS VOLUME_OP_RATE_PER_MINUTE.
const VolumeOpRatePerMinute = 6

// VolumeRestoreUploadMax is TS VOLUME_RESTORE_UPLOAD_MAX.
const VolumeRestoreUploadMax = 2 << 30
