package model

// ═════════════════════ generic responses

// ListResponse is TS ListResponse<T>.
type ListResponse[T any] struct {
	Items []T `json:"items"`
}

// PageResponse is TS PageResponse<T>.
type PageResponse[T any] struct {
	Items  []T     `json:"items"`
	Cursor *string `json:"cursor"`
}

// Deleted is TS Deleted.
type Deleted struct {
	Deleted bool `json:"deleted"`
}

// OkResponse is { ok: true }.
type OkResponse struct {
	Ok bool `json:"ok"`
}

// StoredResponse is { stored }.
type StoredResponse struct {
	Stored bool `json:"stored"`
}

// AcceptedResponse is { accepted }.
type AcceptedResponse struct {
	Accepted bool `json:"accepted"`
}

// DiffChange is TS DiffChange (section: variables | secrets | files | sites | containers | edge).
type DiffChange struct {
	Section  string `json:"section"`
	SiteSlug *Slug  `json:"siteSlug"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // added | removed | changed
	Before   any    `json:"before"`
	After    any    `json:"after"`
	// Note (round E): a review hint (e.g. "seed changed — applies only to new / empty volumes").
	Note string `json:"note,omitempty"`
	// Blob (§21 #101): one file of a volume's content (path <site>/volumes/<v>/content/<path>): the blobs of a text diff.
	Blob *DiffBlob `json:"blob,omitempty"`
}

// DiffBlob is TS DiffChange.blob (§21 #101).
type DiffBlob struct {
	VolumeSlug string  `json:"volumeSlug"`
	Path       string  `json:"path"`
	Before     *Sha256 `json:"before"`
	After      *Sha256 `json:"after"`
}

// RevealSeconds is TS REVEAL_SECONDS: a revealed value stays visible this long (SPEC §21 #84).
const RevealSeconds = 60

// RevealRatePerMinute is TS REVEAL_RATE_PER_MINUTE: value-returning requests per session per fixed minute.
const RevealRatePerMinute = 30

// RevealSecretResponse is TS RevealSecretResponse (= RevealResponse).
type RevealSecretResponse struct {
	Value        string `json:"value"`
	HideAfterSec int    `json:"hideAfterSec"`
}

// UploadPartResponse is TS UploadPartResponse.
type UploadPartResponse struct {
	Received []int `json:"received"`
}

// CompleteUploadResponse is TS CompleteUploadResponse.
type CompleteUploadResponse struct {
	Upload   Upload    `json:"upload"`
	Blob     *BlobRef  `json:"blob"`
	Transfer *Transfer `json:"transfer"`
}

// CreateTransferResponse is TS CreateTransferResponse.
type CreateTransferResponse struct {
	Transfer Transfer `json:"transfer"`
	Upload   Upload   `json:"upload"`
}

// ═════════════════════ install + agent ↔ cloud

// EnrollRequest is TS EnrollRequest.
type EnrollRequest struct {
	PublicKey     string `json:"publicKey"`
	AgentVersion  string `json:"agentVersion"`
	Hostname      string `json:"hostname"`
	IP            string `json:"ip"`
	AdvertiseHost string `json:"advertiseHost"`
	AgentPort     int    `json:"agentPort"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	// DetectedAddresses: non-loopback IPs (IPv4 first) + hostname (optional for old agents)
	DetectedAddresses []string `json:"detectedAddresses,omitempty"`
}

// EnrollResponse is TS EnrollResponse.
type EnrollResponse struct {
	DeploymentID ID           `json:"deploymentId"`
	SiteSlug     Slug         `json:"siteSlug"`
	Peers        []PeerInfo   `json:"peers"`
	AccessKey    AccessKeyRef `json:"accessKey"`
	ServerTime   Ts           `json:"serverTime"`
	// optional (§21 #86 add-on)
	ConsoleURL      *string `json:"consoleUrl,omitempty"`
	DeploymentLabel *string `json:"deploymentLabel,omitempty"`
}

// PollQuery is TS PollQuery (query string: cursor, wait).
type PollQuery struct {
	Cursor string
	Wait   *int
}

// PollResponse is TS PollResponse.
type PollResponse = CloudView

// HeartbeatRequest is TS HeartbeatRequest.
type HeartbeatRequest struct {
	SiteSlug      Slug               `json:"siteSlug"`
	AgentVersion  string             `json:"agentVersion"`
	HeadRevID     *RevID             `json:"headRevId"`
	Staging       *HeartbeatStaging  `json:"staging"`
	PeersSeen     []PeerSeen         `json:"peersSeen"`
	Metrics       HostMetricsSummary `json:"metrics"`
	Edge          *EdgeReport        `json:"edge"`
	KeyVersion    int                `json:"keyVersion"`
	AdvertiseHost string             `json:"advertiseHost"`
	AgentPort     int                `json:"agentPort"`
	IP            string             `json:"ip"`
	Hostname      string             `json:"hostname"`
	// DetectedAddresses: non-loopback IPs (IPv4 first) + hostname (optional for old agents)
	DetectedAddresses []string `json:"detectedAddresses,omitempty"`
	// §21 #86 (optional for old agents)
	GuiPorts []GuiPort `json:"guiPorts,omitempty"`
	// §21 #90 current / last upgrade attempt (sent at once on every phase change; until 24 h after it finished)
	Upgrade *AgentUpgradeProgress `json:"upgrade,omitempty"`
	// §21 #92 the agent's raw clock when it sent this heartbeat (NOT corrected by the X-OPM-Server-Time offset);
	// the cloud stores clockSkew = agentTime − receive time and warns when |skew| > 2 min.
	AgentTime string `json:"agentTime,omitempty"`
	// §21 #101 sync state of every persist-to-cloud volume of this site (agents ≥ 0.12)
	VolumeSync []VolumeSyncReport `json:"volumeSync,omitempty"`
	// §21 #104 the agent branch of this site (agents ≥ 0.14)
	Branch *AgentBranchReport `json:"branch,omitempty"`
}

// AgentReleaseResponse is TS AgentReleaseResponse (§21 #86, opm-agent upgrade).
type AgentReleaseResponse struct {
	Version          string `json:"version"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	Sha256           string `json:"sha256"`
	Signature        string `json:"signature"`
	ReleasePublicKey string `json:"releasePublicKey"`
	BinaryPath       string `json:"binaryPath"`
	// §21 #90 release notes + binary size (absent on old clouds)
	Notes     *string `json:"notes,omitempty"`
	SizeBytes *int64  `json:"sizeBytes,omitempty"`
	// §21 #91 SQLite schema of the build + the oldest DB schema it opens (absent = unknown)
	Schema    *int `json:"schema,omitempty"`
	MinSchema *int `json:"minSchema,omitempty"`
}

// HeartbeatResponse is TS HeartbeatResponse.
type HeartbeatResponse struct {
	Ok         bool `json:"ok"`
	ServerTime Ts   `json:"serverTime"`
}

// ReportHeadRequest is TS ReportHeadRequest.
type ReportHeadRequest struct {
	Revision Revision `json:"revision"`
}

// ReportHeadResponse is TS ReportHeadResponse.
type ReportHeadResponse struct {
	Stored      bool `json:"stored"`
	HeadChanged bool `json:"headChanged"`
}

// AgentProposalResponse is TS AgentProposalResponse.
type AgentProposalResponse struct {
	Proposal Proposal `json:"proposal"`
	Revision Revision `json:"revision"`
}

// RegisterLocalProposalRequest is TS RegisterLocalProposalRequest.
type RegisterLocalProposalRequest struct {
	Proposal Proposal `json:"proposal"`
	Revision Revision `json:"revision"`
}

// ProposalStatusReport is TS ProposalStatusReport (all fields but siteSlug optional).
type ProposalStatusReport struct {
	SiteSlug   Slug             `json:"siteSlug"`
	Staging    string           `json:"staging,omitempty"`
	Progress   *float64         `json:"progress,omitempty"`
	Error      Optional[string] `json:"error"`
	Status     string           `json:"status,omitempty"` // approved | applying | applied | failed-rolled-back | stale
	ApprovedBy string           `json:"approvedBy,omitempty"`
	ApprovedAt string           `json:"approvedAt,omitempty"`
	UpdateID   string           `json:"updateId,omitempty"`
	Result     *ProposalResult  `json:"result,omitempty"`
}

// MarshalJSON omits error when unset.
func (r ProposalStatusReport) MarshalJSON() ([]byte, error) {
	type alias ProposalStatusReport
	return marshalOmitUnset(alias(r), map[string]bool{"error": !r.Error.Set})
}

// ProposalStatusResponse is TS ProposalStatusResponse.
type ProposalStatusResponse struct {
	Ok       bool     `json:"ok"`
	Proposal Proposal `json:"proposal"`
}

// RevisionResponse is { revision } (GET /v1/agent/revisions/{revId}, GET /_opm/peer/revisions/{revId}).
type RevisionResponse struct {
	Revision Revision `json:"revision"`
}

// PeerRevisionResponse is TS PeerRevisionResponse.
type PeerRevisionResponse = RevisionResponse

// ImageConfigRef is ImageManifestResponse.config.
type ImageConfigRef struct {
	Digest    Sha256 `json:"digest"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ImageManifestResponse is TS ImageManifestResponse.
type ImageManifestResponse struct {
	ServiceSlug Slug           `json:"serviceSlug"`
	Version     string         `json:"version"`
	Digest      Sha256         `json:"digest"`
	Platform    string         `json:"platform"`
	MediaType   string         `json:"mediaType"`
	Config      ImageConfigRef `json:"config"`
	Layers      []ImageLayer   `json:"layers"`
}

// AgentActivitiesRequest is TS AgentActivitiesRequest.
type AgentActivitiesRequest struct {
	SiteSlug Slug       `json:"siteSlug"`
	Entries  []Activity `json:"entries"`
}

// ActivityError is an item of the activity upload errors.
type ActivityError struct {
	ActivityID string `json:"activityId"`
	Reason     string `json:"reason"`
}

// AgentActivitiesResponse is TS AgentActivitiesResponse.
type AgentActivitiesResponse struct {
	AckSeq   int             `json:"ackSeq"`
	Inserted int             `json:"inserted"`
	Skipped  int             `json:"skipped"`
	Rejected int             `json:"rejected"`
	Errors   []ActivityError `json:"errors"`
}

// AgentCreateTransferRequest is TS AgentCreateTransferRequest.
type AgentCreateTransferRequest struct {
	FileName  string `json:"fileName"`
	SizeBytes int64  `json:"sizeBytes"`
	Sha256    Sha256 `json:"sha256"`
	Note      string `json:"note,omitempty"`
	Actor     string `json:"actor"`
}

// AgentTransferStatusRequest is TS AgentTransferStatusRequest.
type AgentTransferStatusRequest struct {
	Status    string `json:"status"` // ready | saved-on-server | downloaded | fetched (§21 #100)
	SavedPath string `json:"savedPath,omitempty"`
	Actor     string `json:"actor"`
}

// RelayInner is the inner signed request of a relay.
type RelayInner struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	BodyB64 *string           `json:"bodyB64"`
}

// RelayRequest is TS RelayRequest.
type RelayRequest struct {
	ForSite Slug       `json:"forSite"`
	Request RelayInner `json:"request"`
}

// RelayResponse is TS RelayResponse.
type RelayResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 *string           `json:"bodyB64"`
}

// ═════════════════════ agent local API (/_opm/api/*)

// AgentInfoResponse is TS AgentInfoResponse (surface: edge | gui-alias | agent-port).
type AgentInfoResponse struct {
	DeploymentID ID     `json:"deploymentId"`
	SiteSlug     Slug   `json:"siteSlug"`
	AgentVersion string `json:"agentVersion"`
	Enrolled     bool   `json:"enrolled"`
	HasAccessKey bool   `json:"hasAccessKey"`
	KeyVersion   int    `json:"keyVersion"`
	ServerTime   Ts     `json:"serverTime"`
	Surface      string `json:"surface"`
	// optional console link data for the GUI (§21 #86 add-on)
	ConsoleURL      *string `json:"consoleUrl"`
	APIBase         *string `json:"apiBase"`
	DeploymentLabel *string `json:"deploymentLabel"`
	// §21 #91 SQLite schema of this build
	Schema int `json:"schema"`
}

// LoginRequest is TS LoginRequest.
type LoginRequest struct {
	AccessKey string `json:"accessKey"`
}

// LoginResponse is TS LoginResponse.
type LoginResponse struct {
	SessionID  ULID `json:"sessionId"`
	ExpiresAt  Ts   `json:"expiresAt"`
	KeyVersion int  `json:"keyVersion"`
}

// SessionResponse is TS SessionResponse.
type SessionResponse = LoginResponse

// LoggedOutResponse is { loggedOut: true }.
type LoggedOutResponse struct {
	LoggedOut bool `json:"loggedOut"`
}

// ActorRequest is TS ActorRequest.
type ActorRequest struct {
	Email string `json:"email"`
}

// ActorResponse is TS ActorResponse.
type ActorResponse struct {
	ActorToken string `json:"actorToken"`
	ExpiresAt  Ts     `json:"expiresAt"`
	Email      string `json:"email"`
}

// RecentActorsResponse is TS RecentActorsResponse.
type RecentActorsResponse struct {
	Actors []ActorRow `json:"actors"`
}

// PeerActorsResponse is TS PeerActorsResponse.
type PeerActorsResponse = RecentActorsResponse

// SiteTileStaging is SiteTile.staging.
type SiteTileStaging struct {
	RevID   RevID       `json:"revId"`
	Staging SiteStaging `json:"staging"`
}

// ContainerCounts is SiteTile.containers.
type ContainerCounts struct {
	Total int `json:"total"`
	Ready int `json:"ready"`
}

// SiteTile is TS SiteTile.
type SiteTile struct {
	SiteSlug      Slug             `json:"siteSlug"`
	Label         *string          `json:"label"`
	Self          bool             `json:"self"`
	Enrolled      bool             `json:"enrolled"`
	Reachable     bool             `json:"reachable"`
	LastSeenAt    *Ts              `json:"lastSeenAt"`
	Host          *string          `json:"host"`
	AdvertiseHost *string          `json:"advertiseHost"`
	AgentPort     *int             `json:"agentPort"`
	AgentVersion  *string          `json:"agentVersion"`
	Head          *RevRef          `json:"head"`
	Staging       *SiteTileStaging `json:"staging"`
	CloudRoute    *string          `json:"cloudRoute"`
	Containers    ContainerCounts  `json:"containers"`
	InConfig      bool             `json:"inConfig"`
	Lock          *UpdateLock      `json:"lock"`
}

// OverviewHead is OverviewResponse.head.
type OverviewHead struct {
	RevID     RevID  `json:"revId"`
	Seq       int    `json:"seq"`
	Hash      Sha256 `json:"hash"`
	Summary   string `json:"summary"`
	CreatedAt Ts     `json:"createdAt"`
}

// OverviewCloud is OverviewResponse.cloud.
type OverviewCloud struct {
	Route     string  `json:"route"`
	LastOkAt  *Ts     `json:"lastOkAt"`
	FetchedAt *Ts     `json:"fetchedAt"`
	Source    *string `json:"source"`
}

// OverviewResponse is TS OverviewResponse. Links values: float64 rtt ms, nil, or "self".
type OverviewResponse struct {
	DeploymentID ID                    `json:"deploymentId"`
	Self         Slug                  `json:"self"`
	ServerTime   Ts                    `json:"serverTime"`
	Head         *OverviewHead         `json:"head"`
	Sites        []SiteTile            `json:"sites"`
	Links        map[Slug]map[Slug]any `json:"links"`
	Cloud        OverviewCloud         `json:"cloud"`
	Lock         *UpdateLock           `json:"lock"`
	Deleted      bool                  `json:"deleted"`
}

// AgentConfigResponse is TS AgentConfigResponse.
type AgentConfigResponse struct {
	Head    *HeadHash        `json:"head"`
	Config  ConfigView       `json:"config"`
	Catalog *RevisionCatalog `json:"catalog"`
	// Working (§21 #104, agents ≥ 0.14): the GUI edits not committed yet (the edited config, masked); absent = none
	Working *ConfigView `json:"working,omitempty"`
	// KnownCatalog (§21 #113): every image a local edit can use — the head's, the recent cloud commits', the
	// drift-adopted and the site images (⊇ catalog); absent from older agents
	KnownCatalog *RevisionCatalog `json:"knownCatalog,omitempty"`
}

// LocalProposalRequest is TS LocalProposalRequest.
type LocalProposalRequest struct {
	BaseRevID RevID       `json:"baseRevId"`
	Summary   string      `json:"summary"`
	Config    ConfigInput `json:"config"`
}

// LocalProposalPreviewResponse is TS LocalProposalPreviewResponse.
type LocalProposalPreviewResponse struct {
	Changes    []DiffChange     `json:"changes"`
	Validation ValidationReport `json:"validation"`
}

// LocalProposalResponse is TS LocalProposalResponse.
type LocalProposalResponse struct {
	Proposal Proposal     `json:"proposal"`
	Changes  []DiffChange `json:"changes"`
}

// Count is { total; have }.
type Count struct {
	Total int `json:"total"`
	Have  int `json:"have"`
}

// StagingBlob is an item of StagingDetail.blobs (source: cloud | peer:<slug> | local | null).
type StagingBlob struct {
	Sha256    Sha256  `json:"sha256"`
	Kind      string  `json:"kind"`
	SizeBytes int64   `json:"sizeBytes"`
	Source    *string `json:"source"`
	State     string  `json:"state"` // have | downloading | waiting | failed
}

// StagingDetail is TS StagingDetail.
type StagingDetail struct {
	SiteSlug Slug              `json:"siteSlug"`
	Staging  SiteStaging       `json:"staging"`
	Layers   Count             `json:"layers"`
	Files    Count             `json:"files"`
	Blobs    []StagingBlob     `json:"blobs"`
	Issues   []ValidationIssue `json:"issues"`
}

// ProposalListItem is TS ProposalListItem.
type ProposalListItem struct {
	Proposal     Proposal `json:"proposal"`
	IsHeadParent bool     `json:"isHeadParent"`
}

// ProposalDetailResponse is TS ProposalDetailResponse.
type ProposalDetailResponse struct {
	Proposal Proposal        `json:"proposal"`
	Revision RevisionView    `json:"revision"`
	Changes  []DiffChange    `json:"changes"`
	Staging  []StagingDetail `json:"staging"`
	Gate     GateCheck       `json:"gate"`
}

// ApproveStartResponse is TS ApproveStartResponse.
type ApproveStartResponse struct {
	Intent ApproveIntent `json:"intent"`
	Gate   GateCheck     `json:"gate"`
}

// ApproveRequest is TS ApproveRequest.
type ApproveRequest struct {
	IntentID ULID   `json:"intentId"`
	Typed    string `json:"typed"`
	// Message (§21 #104, branch mode): the commit message of a snapshot rollback (required there)
	Message string `json:"message,omitempty"`
}

// ApproveResponse is TS ApproveResponse.
type ApproveResponse struct {
	UpdateID ULID      `json:"updateId"`
	Run      UpdateRun `json:"run"`
}

// CurrentUpdateResponse is { run }.
type CurrentUpdateResponse struct {
	Run *UpdateRun `json:"run"`
}

// CreateSnapshotRequest is TS CreateSnapshotRequest.
type CreateSnapshotRequest struct {
	Note string `json:"note,omitempty"`
}

// CreateSnapshotResponse is TS CreateSnapshotResponse.
type CreateSnapshotResponse struct {
	SnapshotID ULID   `json:"snapshotId"`
	Sites      []Slug `json:"sites"`
}

// SnapshotDetailResponse is TS SnapshotDetailResponse.
type SnapshotDetailResponse struct {
	Summary  SnapshotSummary `json:"summary"`
	Snapshot Snapshot        `json:"snapshot"`
}

// RollbackImageChange is an item of RollbackPlanResponse.images.
type RollbackImageChange struct {
	SiteSlug      Slug    `json:"siteSlug"`
	ContainerSlug Slug    `json:"containerSlug"`
	ServiceSlug   Slug    `json:"serviceSlug"`
	From          *string `json:"from"`
	To            *string `json:"to"`
}

// RollbackVolume is an item of RollbackPlanResponse.volumesRestored.
type RollbackVolume struct {
	SiteSlug   Slug   `json:"siteSlug"`
	VolumeSlug string `json:"volumeSlug"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// KeptVolume is an item of RollbackPlanResponse.volumesKept.
type KeptVolume struct {
	SiteSlug   Slug   `json:"siteSlug"`
	VolumeSlug string `json:"volumeSlug"`
	Backup     bool   `json:"backup"`
}

// SiteVolumeUse is an item of SiteVolumeStatus.usedBy.
type SiteVolumeUse struct {
	ContainerSlug Slug   `json:"containerSlug"`
	ImageVolume   *Slug  `json:"imageVolume"`
	MountPath     string `json:"mountPath"` // resolved
	ReadOnly      bool   `json:"readOnly"`
}

// VolumeSnapshotRef is SiteVolumeStatus.lastSnapshot.
type VolumeSnapshotRef struct {
	SnapshotID ULID  `json:"snapshotId"`
	CreatedAt  Ts    `json:"createdAt"`
	SizeBytes  int64 `json:"sizeBytes"`
}

// SiteVolumeStatus is TS SiteVolumeStatus (agent Backups page).
type SiteVolumeStatus struct {
	VolumeSlug   string             `json:"volumeSlug"`
	Label        string             `json:"label"`
	Description  string             `json:"description"`
	Backup       bool               `json:"backup"`
	Declared     bool               `json:"declared"`
	Path         string             `json:"path"`     // absolute directory on this server
	Location     StorageLocation    `json:"location"` // as configured (orphans: auto)
	UsedBy       []SiteVolumeUse    `json:"usedBy"`
	SizeBytes    *int64             `json:"sizeBytes"`
	LastSnapshot *VolumeSnapshotRef `json:"lastSnapshot"`
	Kind         string             `json:"kind"`   // round E: dir | file (orphans: dir)
	Seed         *VolumeSeed        `json:"seed"`   // round E: starter content (null = none)
	Seeded       *VolumeSeeded      `json:"seeded"` // round E: when the starter content was applied here (null = never)
}

// VolumeSeeded is SiteVolumeStatus.seeded (round E).
type VolumeSeeded struct {
	At      Ts     `json:"at"`
	Source  string `json:"source"` // files | file | archive
	Entries int    `json:"entries"`
}

// ResetVolumeResponse is TS ResetVolumeResponse (POST …/volumes/{volumeSlug}/reset, round E).
type ResetVolumeResponse struct {
	SiteSlug   Slug   `json:"siteSlug"`
	VolumeSlug string `json:"volumeSlug"`
	SnapshotID ULID   `json:"snapshotId"`
	Entries    int    `json:"entries"`
}

// HostPathMount is an item of SiteVolumesResponse.hostPaths.
type HostPathMount struct {
	ContainerSlug Slug   `json:"containerSlug"`
	MountPath     string `json:"mountPath"`
	Path          string `json:"path"`
	ReadOnly      bool   `json:"readOnly"`
}

// SiteVolumesResponse is TS SiteVolumesResponse (GET /_opm/api/sites/{siteSlug}/volumes).
type SiteVolumesResponse struct {
	SiteSlug   Slug               `json:"siteSlug"`
	AgentHome  string             `json:"agentHome"`
	MeasuredAt Ts                 `json:"measuredAt"`
	Volumes    []SiteVolumeStatus `json:"volumes"`
	HostPaths  []HostPathMount    `json:"hostPaths"`
}

// RollbackPlanResponse is TS RollbackPlanResponse.
type RollbackPlanResponse struct {
	Snapshot        SnapshotSummary       `json:"snapshot"`
	Images          []RollbackImageChange `json:"images"`
	Changes         []DiffChange          `json:"changes"`
	VolumesRestored []RollbackVolume      `json:"volumesRestored"`
	VolumesKept     []KeptVolume          `json:"volumesKept"`
	DataLossSince   Ts                    `json:"dataLossSince"`
	NewSeq          int                   `json:"newSeq"`
	Gate            GateCheck             `json:"gate"`
}

// PatchSnapshotRequest is TS PatchSnapshotRequest.
type PatchSnapshotRequest struct {
	Note string `json:"note"`
}

// LogLine is an item of ContainerLogsResponse.lines.
type LogLine struct {
	Ts     Ts     `json:"ts"`
	Stream string `json:"stream"` // stdout | stderr
	Text   string `json:"text"`
}

// ContainerLogsResponse is TS ContainerLogsResponse.
type ContainerLogsResponse struct {
	SiteSlug      Slug      `json:"siteSlug"`
	ContainerSlug Slug      `json:"containerSlug"`
	ServiceSlug   Slug      `json:"serviceSlug"`
	Lines         []LogLine `json:"lines"`
}

// SaveTransferRequest is TS SaveTransferRequest.
type SaveTransferRequest struct {
	Path string `json:"path"`
}

// SendFromPathRequest is TS SendFromPathRequest.
type SendFromPathRequest struct {
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

// TunnelStartRequest is TS TunnelStartRequest.
type TunnelStartRequest struct {
	UserAgent string `json:"userAgent"`
}

// TunnelLeaseIDRequest is { leaseId } (stop / revoke).
type TunnelLeaseIDRequest struct {
	LeaseID ULID `json:"leaseId"`
}

// TunnelLeaseResponse is TS TunnelLeaseResponse.
type TunnelLeaseResponse struct {
	Lease TunnelLease `json:"lease"`
}

// TunnelStateResponse is TS TunnelStateResponse.
type TunnelStateResponse struct {
	Lease      *TunnelLease `json:"lease"`
	HolderSite *Slug        `json:"holderSite"`
	Pending    int          `json:"pending"`
	Encrypted  bool         `json:"encrypted"`
}

// TunnelNextResponse is TS TunnelNextResponse.
type TunnelNextResponse struct {
	Requests []TunnelRequest `json:"requests"`
}

// TunnelChunkResponse is { received }.
type TunnelChunkResponse struct {
	Received int `json:"received"`
}

// TunnelCompleteRequest is TS TunnelCompleteRequest.
type TunnelCompleteRequest struct {
	LeaseID ULID   `json:"leaseId"`
	Parts   int    `json:"parts"`
	Sha256  Sha256 `json:"sha256"`
}

// CertRenewResponse is TS CertRenewResponse.
type CertRenewResponse struct {
	Status string     `json:"status"` // renewed | failed
	Cert   CertStatus `json:"cert"`
}

// ═════════════════════ peer API (/_opm/peer/*)

// PeerProposalPush is TS PeerProposalPush.
type PeerProposalPush struct {
	Proposal Proposal `json:"proposal"`
	Revision Revision `json:"revision"`
}

// PeerRelayRequest is TS PeerRelayRequest.
type PeerRelayRequest struct {
	Request RelayInner `json:"request"`
}

// PeerSnapshotStepRequest is the body of POST /_opm/peer/snapshots.
type PeerSnapshotStepRequest struct {
	SnapshotID ULID   `json:"snapshotId"`
	Reason     string `json:"reason"` // manual | scheduled
	Note       string `json:"note"`
	CreatedBy  string `json:"createdBy"`
}

// ───────────────────────── §21 #118 remote agent GUI (TS shared/src/remote-gui.ts)

// RemoteGuiCloudItem is TS RemoteGuiCloudItem.
type RemoteGuiCloudItem struct {
	SiteSlug string `json:"siteSlug"`
	Until    Ts     `json:"until"`
}

// RemoteGuiActor is TS RemoteGuiActor.
type RemoteGuiActor struct {
	UID   string  `json:"uid"`
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

// RemoteGuiRequest is TS RemoteGuiRequest.
type RemoteGuiRequest struct {
	ReqID     string            `json:"reqId"`
	SessionID string            `json:"sessionId"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	BodyB64   *string           `json:"bodyB64"`
	Actor     RemoteGuiActor    `json:"actor"`
	// WS (§21 #118 g): a WebSocket upgrade relayed frame by frame
	WS *struct {
		StreamID string `json:"streamId"`
	} `json:"ws,omitempty"`
	// Stream (§21 #118 h): an event stream; the body goes chunk by chunk as frames of this stream
	Stream *struct {
		StreamID string `json:"streamId"`
	} `json:"stream,omitempty"`
}

// RemoteGuiWsFrame is TS RemoteGuiWsFrame.
type RemoteGuiWsFrame struct {
	Op      string `json:"op"` // text | binary | close
	DataB64 string `json:"dataB64"`
	Code    int    `json:"code,omitempty"`
}

// RemoteGuiWsRecvResponse is TS RemoteGuiWsRecvResponse.
type RemoteGuiWsRecvResponse struct {
	Frames []RemoteGuiWsFrame `json:"frames"`
	Closed bool               `json:"closed"`
}

// RemoteGuiWsSendRequest is TS RemoteGuiWsSendRequest.
type RemoteGuiWsSendRequest struct {
	Frames []RemoteGuiWsFrame `json:"frames"`
}

// RemoteGuiNextResponse is TS RemoteGuiNextResponse.
type RemoteGuiNextResponse struct {
	Items []RemoteGuiRequest `json:"items"`
}

// RemoteGuiSession is an item of RemoteGuiSetting.sessions.
type RemoteGuiSession struct {
	Email string `json:"email"`
	Since Ts     `json:"since"`
}

// RemoteGuiSetting is TS RemoteGuiSetting.
type RemoteGuiSetting struct {
	Enabled  bool               `json:"enabled"`
	Sessions []RemoteGuiSession `json:"sessions,omitempty"`
}

// SessionRemote is TS SessionResponse.remote.
type SessionRemote struct {
	Email      string  `json:"email"`
	Name       *string `json:"name"`
	ActorToken string  `json:"actorToken"`
	ExpiresAt  Ts      `json:"expiresAt"`
}

// SessionView is GET /_opm/api/session (LoginResponse + §21 #118 remote).
type SessionView struct {
	LoginResponse
	Remote *SessionRemote `json:"remote"`
}

// ───────────────────────── §21 #119 storage + moving the agent home (TS shared/src/agent-self.ts)

// StorageFs is one filesystem OPM uses (TS StorageFilesystem).
type StorageFs struct {
	Path       string   `json:"path"` // a representative path on it (the first role's)
	TotalBytes int64    `json:"totalBytes"`
	FreeBytes  int64    `json:"freeBytes"`
	UsedBytes  int64    `json:"usedBytes"`
	FloorBytes int64    `json:"floorBytes"` // the free space the agent never uses
	Roles      []string `json:"roles"`      // "agent home", "Docker data", "volume <slug>", …
}

// StorageView is GET /_opm/api/agent/storage (TS StorageView).
type StorageView struct {
	Home          string      `json:"home"`
	HomeBytes     *int64      `json:"homeBytes"` // null while it is being measured
	HomeFiles     int         `json:"homeFiles"`
	DockerRootDir *string     `json:"dockerRootDir"`
	Filesystems   []StorageFs `json:"filesystems"`
	Move          any         `json:"move"` // the current / last move (movehome.State) or null
	CanMove       bool        `json:"canMove"`
	CannotMove    *string     `json:"cannotMove"`
	MeasuredAt    *Ts         `json:"measuredAt"`
}

// MoveHomeRequest is POST /_opm/api/agent/move-home/check and /move-home.
type MoveHomeRequest struct {
	To    string `json:"to"`
	Typed string `json:"typed,omitempty"`
}

// MoveHomeCheck is TS MoveHomeCheck.
type MoveHomeCheck struct {
	Ok               bool     `json:"ok"`
	To               string   `json:"to"`
	SameFilesystem   bool     `json:"sameFilesystem"`
	SizeBytes        int64    `json:"sizeBytes"`
	TargetFreeBytes  int64    `json:"targetFreeBytes"`
	TargetTotalBytes int64    `json:"targetTotalBytes"`
	Errors           []string `json:"errors"`
	Warnings         []string `json:"warnings"`
	Containers       []string `json:"containers"` // stopped during the move
}
