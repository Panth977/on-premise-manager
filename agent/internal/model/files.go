package model

// §21 #123 shared files (TS shared/src/files.ts).

const (
	FileMax           = 2 << 30
	FileNoteMax       = 500
	FileTombstoneDays = 30
)

// SharedFileOrigin is TS SharedFileOrigin (kind cloud | site).
type SharedFileOrigin struct {
	Kind       string  `json:"kind"` // cloud | site
	SiteSlug   string  `json:"siteSlug,omitempty"`
	Via        string  `json:"via,omitempty"` // browser | path
	SourcePath *string `json:"sourcePath,omitempty"`
}

// SharedFileCloudFetch is TS SharedFileCloudFetch.
type SharedFileCloudFetch struct {
	RequestedAt string  `json:"requestedAt"`
	RequestedBy string  `json:"requestedBy"`
	From        *string `json:"from"`
	StartedAt   *string `json:"startedAt"`
	BytesDone   int64   `json:"bytesDone"`
	Error       *string `json:"error"`
}

// SharedFileCloud is the cloud copy state.
type SharedFileCloud struct {
	Stored bool                  `json:"stored"`
	Fetch  *SharedFileCloudFetch `json:"fetch"`
}

// SharedFile is TS SharedFile.
type SharedFile struct {
	FileID        string           `json:"fileId"`
	FileName      string           `json:"fileName"`
	SizeBytes     int64            `json:"sizeBytes"`
	Sha256        string           `json:"sha256"`
	Note          *string          `json:"note"`
	PublishedAt   string           `json:"publishedAt"`
	PublishedBy   string           `json:"publishedBy"`
	Origin        SharedFileOrigin `json:"origin"`
	State         string           `json:"state"` // uploading | ready | unpublished
	UnpublishedAt *string          `json:"unpublishedAt"`
	UnpublishedBy *string          `json:"unpublishedBy"`
	Cloud         SharedFileCloud  `json:"cloud"`
	Sites         []string         `json:"sites"`
}

// SharedFileSiteJob is TS SharedFileSiteJob.
type SharedFileSiteJob struct {
	Phase     string  `json:"phase"` // queued | downloading | waiting-cloud | failed
	From      *string `json:"from"`
	BytesDone int64   `json:"bytesDone"`
	Error     *string `json:"error"`
	StartedAt string  `json:"startedAt"`
}

// SharedFileSources is TS SiteSharedFile.sources.
type SharedFileSources struct {
	Peers []string `json:"peers"`
	Cloud bool     `json:"cloud"`
}

// SiteSharedFile is TS SiteSharedFile.
type SiteSharedFile struct {
	SharedFile
	Here     bool               `json:"here"`
	Job      *SharedFileSiteJob `json:"job"`
	Sources  SharedFileSources  `json:"sources"`
	ListedBy string             `json:"listedBy"` // cloud | peer | local
}

// FileFetchItem is one entry of CloudView.fileFetches.
type FileFetchItem struct {
	FileID string `json:"fileId"`
	From   string `json:"from"`
}

// PublishFromPathRequest is TS PublishFromPathRequest.
type PublishFromPathRequest struct {
	Path string  `json:"path"`
	Note *string `json:"note"`
}

// SaveFileRequest is TS SaveFileRequest.
type SaveFileRequest struct {
	Path string `json:"path"`
}

// AgentRegisterFileRequest is TS AgentRegisterFileRequest.
type AgentRegisterFileRequest struct {
	FileID      string  `json:"fileId"`
	FileName    string  `json:"fileName"`
	SizeBytes   int64   `json:"sizeBytes"`
	Sha256      string  `json:"sha256"`
	Note        *string `json:"note"`
	PublishedAt string  `json:"publishedAt"`
	PublishedBy string  `json:"publishedBy"`
	Via         string  `json:"via"`
	SourcePath  *string `json:"sourcePath"`
}

// AgentFileHeldRequest is TS AgentFileHeldRequest.
type AgentFileHeldRequest struct {
	Held bool `json:"held"`
}

// AgentFileActorRequest is TS AgentFileActorRequest.
type AgentFileActorRequest struct {
	Actor string `json:"actor"`
}

// AgentFileUploadResponse is TS AgentFileUploadResponse.
type AgentFileUploadResponse struct {
	Upload Upload `json:"upload"`
}

// PeerFilesResponse is TS PeerFilesResponse.
type PeerFilesResponse struct {
	SiteSlug string       `json:"siteSlug"`
	Files    []SharedFile `json:"files"`
}
