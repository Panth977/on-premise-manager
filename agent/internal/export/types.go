// Package export is the §21 #106 compose export: one OPM commit → a deterministic git tree that runs the same sites with
// `docker compose up` WITHOUT the agent (Go mirror of shared/src/github-mirror.ts ExportRequest / ExportResult; the JSON
// is identical). It is pure (no Docker, no filesystem): the cloud runs it as opm-export.wasm, the VM runs the same code
// as `opm-render init` (runtime mode) for the values that exist only there (secrets, auto host, required variables).
package export

import "opm/agent/internal/model"

// Format is MIRROR_EXPORT_FORMAT.
const Format = 1

// Helper images (digest-pinned; caddy = the agent's embedded Caddy version).
const (
	InitImage = "busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	EdgeImage = "caddy:2.11.7@sha256:f2a1290d0463aad60660d4ec134943f183ee2a5f6c3eb7bf32dd984f2f020772"
	// MaxFileBytes is MIRROR_MAX_FILE_BYTES: bigger files are never pushed (listed in .opm-skipped.txt).
	MaxFileBytes = 100 * 1024 * 1024
)

// Request is TS ExportRequest.
type Request struct {
	Format       int                                   `json:"format"`
	Branch       BranchInfo                            `json:"branch"`
	Commit       CommitInfo                            `json:"commit"`
	DeploymentID string                                `json:"deploymentId"`
	Config       model.Config                          `json:"config"`
	SecretsSet   []string                              `json:"secretsSet"`
	Catalog      model.RevisionCatalog                 `json:"catalog"`
	Images       map[string]ImageRef                   `json:"images"`
	Texts        map[string]string                     `json:"texts"`
	Sites        []string                              `json:"sites"`
	Persist      map[string]map[string]PersistSnapshot `json:"persist"`
	Tool         Tool                                  `json:"tool"`
}

// BranchInfo is TS ExportRequest.branch.
type BranchInfo struct {
	Kind         string  `json:"kind"` // template | deployment | agent | uncommitted
	GitBranch    string  `json:"gitBranch"`
	EntitySlug   string  `json:"entitySlug"`
	Label        string  `json:"label"`
	TemplateID   *string `json:"templateId"`
	DeploymentID *string `json:"deploymentId"`
	SiteSlug     *string `json:"siteSlug"`
}

// Person is a commit author.
type Person struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// CommitInfo is TS ExportRequest.commit.
type CommitInfo struct {
	ID        string  `json:"id"`
	Seq       *int    `json:"seq"`
	Kind      string  `json:"kind"`
	Message   string  `json:"message"`
	Author    Person  `json:"author"`
	CreatedAt string  `json:"createdAt"`
	Hash      *string `json:"hash"`
	ParentID  *string `json:"parentId"`
}

// ImageRef is TS ExportImageRef.
type ImageRef struct {
	Pull         string  `json:"pull"`
	RegistryHost *string `json:"registryHost"`
	Private      bool    `json:"private"`
	Platform     string  `json:"platform"`
}

// PersistSnapshot is TS ExportPersistSnapshot.
type PersistSnapshot struct {
	VersionID string           `json:"versionId"`
	Seq       int              `json:"seq"`
	CreatedAt string           `json:"createdAt"`
	TreeHash  string           `json:"treeHash"`
	Tree      model.VolumeTree `json:"tree"`
}

// Tool is TS ExportRequest.tool.
type Tool struct {
	Version string   `json:"version"`
	Amd64   *ToolBin `json:"amd64"`
	Arm64   *ToolBin `json:"arm64"`
}

// ToolBin is one opm-render binary.
type ToolBin struct {
	Sha256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// File is TS ExportFile (exactly one of Text / Base64 / Blob / Tool). Perm is the file mode written by opm-render init
// for runtime outputs (not part of the wire format).
type File struct {
	Path   string  `json:"path"`
	Mode   string  `json:"mode"` // 100644 | 100755
	Text   *string `json:"text,omitempty"`
	Base64 *string `json:"base64,omitempty"`
	Blob   *string `json:"blob,omitempty"`
	Tool   *string `json:"tool,omitempty"`
	Size   int64   `json:"size"`
	Perm   uint32  `json:"-"`
	// From (runtime mode): copy this file of the site directory (git checkout) instead of Text.
	From string `json:"-"`
}

// Note is TS ExportNote.
type Note struct {
	Code    string  `json:"code"`
	Site    *string `json:"site"`
	Message string  `json:"message"`
}

// EnvVar is TS ExportEnvVar.
type EnvVar struct {
	Name        string `json:"name"`
	Site        string `json:"site"`
	Kind        string `json:"kind"` // secret | var | site-host | edge-port | other
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// Result is TS ExportResult.
type Result struct {
	Format int      `json:"format"`
	Files  []File   `json:"files"`
	Notes  []Note   `json:"notes"`
	Env    []EnvVar `json:"env"`
}

// Mode selects the export: the git tree (default) or, on the VM, the deferred outputs of ONE site rendered with the
// real values (Values: env name → value, e.g. OPM_SECRET_MQTT_PASSWORD).
type Mode struct {
	Runtime bool
	Site    string
	Values  map[string]string
}
