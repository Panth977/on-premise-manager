// Package export is the compose export (§21 #106, #115 Docker-only): one OPM commit → a deterministic git tree that runs
// the same sites with Docker alone: `./deploy.sh` (busybox renders the values that exist only on the machine) and
// `docker compose up`, with Caddy as the edge and Portainer as the Docker GUI. No OPM code runs on the machine. Go mirror
// of shared/src/github-mirror.ts ExportRequest / ExportResult (the JSON is identical). It is pure (no Docker, no
// filesystem): the cloud runs it as opm-export.wasm.
package export

import "opm/agent/internal/model"

// Format is MIRROR_EXPORT_FORMAT.
const Format = 2

// Helper images, all official and digest-pinned. busybox musl = a static binary (it also runs the health checks inside
// any image); caddy = the agent's embedded Caddy version; Portainer CE LTS = the Docker GUI.
const (
	InitImage      = "busybox:1.37-musl@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092"
	EdgeImage      = "caddy:2.11.7@sha256:f2a1290d0463aad60660d4ec134943f183ee2a5f6c3eb7bf32dd984f2f020772"
	MosquittoImage = "eclipse-mosquitto:2.0.22@sha256:199ea8ef2e35ec2b1b37e59cfd1dbae538ed4dfa4a2251a121a52215a6248a21"
	PortainerImage = "portainer/portainer-ce:2.45.2@sha256:03f94a406496dc84d37b552795fc424ccad8d0d7200353b6a82915f6e8f73ab0"
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
	// Repository is the mirror repository "owner/name" ("" = unknown): the READMEs show the exact clone URL.
	Repository string `json:"repository"`
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

// File is TS ExportFile (exactly one of Text / Base64 / Blob).
type File struct {
	Path   string  `json:"path"`
	Mode   string  `json:"mode"` // 100644 | 100755
	Text   *string `json:"text,omitempty"`
	Base64 *string `json:"base64,omitempty"`
	Blob   *string `json:"blob,omitempty"`
	Size   int64   `json:"size"`
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

// Mode is reserved for export variants (the git tree is the only one).
type Mode struct{}
