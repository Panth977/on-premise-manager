package model

import "encoding/json"

// §21 #101 volume content (a volume's own file tree) + persist-to-cloud volumes. Mirror of shared/src/types.ts
// (VolumeTreeEntry, VolumeTree, VolumePersist) and shared/src/volume-content.ts (cloud records, agent view types).

// VolumeTreeEntry is TS VolumeTreeEntry: one file of a volume's content (bytes = blob files/<hex>).
type VolumeTreeEntry struct {
	Path   string `json:"path"`
	Sha256 Sha256 `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`   // "0644"
	Render string `json:"render"` // none | expressions (starter content only)
}

// VolumeTree is TS VolumeTree (entries + EMPTY folders). Marshals nil slices as [].
type VolumeTree struct {
	Entries []VolumeTreeEntry `json:"entries"`
	Dirs    []string          `json:"dirs"`
}

// MarshalJSON emits [] for nil slices.
func (t VolumeTree) MarshalJSON() ([]byte, error) {
	type wire VolumeTree
	w := wire(t)
	if w.Entries == nil {
		w.Entries = []VolumeTreeEntry{}
	}
	if w.Dirs == nil {
		w.Dirs = []string{}
	}
	return json.Marshal(w)
}

// VolumePersist is TS VolumePersist (SiteVolume.persist; absent = off).
type VolumePersist struct {
	Exclude  []string `json:"exclude"`
	MaxBytes int64    `json:"maxBytes"`
	Auto     bool     `json:"auto"`
}

// MarshalJSON emits [] for a nil exclude list.
func (p VolumePersist) MarshalJSON() ([]byte, error) {
	type wire VolumePersist
	w := wire(p)
	if w.Exclude == nil {
		w.Exclude = []string{}
	}
	return json.Marshal(w)
}

// Limits + timings (VOLUME_CONTENT_LIMITS, VOLUME_SYNC_*).
const (
	VolumeContentFileMaxBytes         = 50 * 1024 * 1024
	VolumeContentSnapshotFileMaxBytes = 24 * 1024 * 1024
	VolumeContentSeedMaxEntries       = 1000
	VolumeContentSeedMaxDirs          = 1000
	VolumeContentSeedMaxTotalBytes    = 200 * 1024 * 1024
	VolumeContentVersionMaxEntries    = 10000
	VolumeContentVersionMaxDirs       = 5000
	VolumeContentPathMaxBytes         = 1024
	VolumeContentSegmentMaxBytes      = 255
	VolumePersistMinBytes             = 1024 * 1024
	VolumePersistMaxBytes             = 200 * 1024 * 1024
	VolumePersistDefaultBytes         = 50 * 1024 * 1024
	VolumeExcludeMax                  = 50
	VolumeExcludePatternMax           = 200
	VolumeContentNoteMax              = 500
	VolumeSyncScanSec                 = 60
	VolumeSyncQuietSec                = 120
	VolumeSyncMinIntervalSec          = 900
	VolumeContentApplyWaitSec         = 60
	VolumeSeedMarker                  = ".opm-seeded"
	VolumeContentMinAgent             = "0.12.0"
	DefaultFileMode                   = "0644"
)

// VolumePersistDefaultExclude is VOLUME_PERSIST_DEFAULT_EXCLUDE.
var VolumePersistDefaultExclude = []string{"node_modules/", ".npm/", ".cache/", "*.log"}

// ───────────────────────── 3-way plan

// VolumePlanSide is TS VolumePlanSide.
type VolumePlanSide struct {
	Sha256 Sha256 `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

// VolumePlanItem is TS VolumePlanItem (kind cloud | live | same | conflict; ops none | added | removed | changed).
type VolumePlanItem struct {
	Path    string          `json:"path"`
	Kind    string          `json:"kind"`
	LiveOp  string          `json:"liveOp"`
	CloudOp string          `json:"cloudOp"`
	Base    *VolumePlanSide `json:"base"`
	Live    *VolumePlanSide `json:"live"`
	Cloud   *VolumePlanSide `json:"cloud"`
}

// ───────────────────────── cloud records

// TreeChangeCounts is TS TreeChangeCounts.
type TreeChangeCounts struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
}

// VolumeSkipped is one item of VolumeContentVersion.skipped (reason symlink | special | bad-name | too-large).
type VolumeSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// VolumeContentVersionSummary is TS VolumeContentVersionSummary.
type VolumeContentVersionSummary struct {
	VersionID ULID   `json:"versionId"`
	Seq       int    `json:"seq"`
	Origin    string `json:"origin"` // snapshot | edit | restore
	TreeHash  Sha256 `json:"treeHash"`
	CreatedAt Ts     `json:"createdAt"`
	CreatedBy string `json:"createdBy"`
}

// VolumeContentVersion is TS VolumeContentVersion.
type VolumeContentVersion struct {
	VolumeContentVersionSummary
	DeploymentID  ID      `json:"deploymentId"`
	SiteSlug      Slug    `json:"siteSlug"`
	VolumeSlug    string  `json:"volumeSlug"`
	Trigger       *string `json:"trigger"`
	FileCount     int     `json:"fileCount"`
	DirCount      int     `json:"dirCount"`
	TotalBytes    int64   `json:"totalBytes"`
	BaseVersionID *ULID   `json:"baseVersionId"`
	// RestoredFromVersionID: restores only (null otherwise)
	RestoredFromVersionID *ULID            `json:"restoredFromVersionId"`
	Note                  string           `json:"note"`
	ExcludedCount         int              `json:"excludedCount"`
	SkippedCount          int              `json:"skippedCount"`
	Skipped               []VolumeSkipped  `json:"skipped"`
	AgentSnapshotID       *ULID            `json:"agentSnapshotId"`
	RequestID             *ULID            `json:"requestId"`
	PushID                *ULID            `json:"pushId"`
	Changes               TreeChangeCounts `json:"changes"`
}

// VolumeContentPushResult is TS VolumeContentPushResult.
type VolumeContentPushResult struct {
	Resolution     string   `json:"resolution"` // overwrite | keep-live | per-file | fast-forward | up-to-date
	Written        int      `json:"written"`
	Deleted        int      `json:"deleted"`
	KeptLive       int      `json:"keptLive"`
	SnapshotID     *ULID    `json:"snapshotId"`
	Restarted      []string `json:"restarted"`
	Error          *string  `json:"error"`
	AfterVersionID *ULID    `json:"afterVersionId"`
}

// VolumeContentPush is TS VolumeContentPush.
type VolumeContentPush struct {
	PushID        ULID                     `json:"pushId"`
	SiteSlug      Slug                     `json:"siteSlug"`
	VolumeSlug    string                   `json:"volumeSlug"`
	VersionID     ULID                     `json:"versionId"`
	VersionSeq    *int                     `json:"versionSeq,omitempty"` // the version's seq (absent on older clouds)
	TreeHash      Sha256                   `json:"treeHash"`
	BaseVersionID *ULID                    `json:"baseVersionId"`
	BaseTreeHash  *Sha256                  `json:"baseTreeHash"`
	Restart       bool                     `json:"restart"`
	Note          string                   `json:"note"`
	Status        string                   `json:"status"` // pending | applied | kept-live | cancelled | superseded | failed
	CreatedAt     Ts                       `json:"createdAt"`
	CreatedBy     string                   `json:"createdBy"`
	ResolvedAt    *Ts                      `json:"resolvedAt"`
	ResolvedBy    *string                  `json:"resolvedBy"`
	Result        *VolumeContentPushResult `json:"result"`
}

// VolumeSnapshotRequestRef is the snapshotRequest of VolumeContentState / VolumeContentCloudItem.
type VolumeSnapshotRequestRef struct {
	RequestID ULID   `json:"requestId"`
	CreatedAt Ts     `json:"createdAt"`
	CreatedBy string `json:"createdBy"`
}

// VolumeContentCloudItem is TS VolumeContentCloudItem (CloudView.volumeContent).
type VolumeContentCloudItem struct {
	SiteSlug        Slug                      `json:"siteSlug"`
	VolumeSlug      string                    `json:"volumeSlug"`
	Push            *VolumeContentPush        `json:"push"`
	SnapshotRequest *VolumeSnapshotRequestRef `json:"snapshotRequest"`
}

// VolumeSyncReport is TS VolumeSyncReport (HeartbeatRequest.volumeSync).
type VolumeSyncReport struct {
	VolumeSlug       string  `json:"volumeSlug"`
	State            string  `json:"state"`
	LiveTreeHash     *Sha256 `json:"liveTreeHash"`
	SnapshotTreeHash *Sha256 `json:"snapshotTreeHash"`
	LiveChanged      bool    `json:"liveChanged"`
	Queued           int     `json:"queued"`
	ScannedAt        *Ts     `json:"scannedAt"`
	LastError        *string `json:"lastError"`
}

// AgentVolumeSnapshotRequest is TS AgentVolumeSnapshotRequest (POST /v1/agent/volume-content/{volumeSlug}/versions).
type AgentVolumeSnapshotRequest struct {
	SnapshotID    ULID            `json:"snapshotId"`
	Trigger       string          `json:"trigger"` // manual-cloud | manual-agent | auto | before-apply | after-apply
	TakenAt       Ts              `json:"takenAt"`
	Actor         *string         `json:"actor"`
	Tree          VolumeTree      `json:"tree"`
	TreeHash      Sha256          `json:"treeHash"`
	ExcludedCount int             `json:"excludedCount"`
	SkippedCount  int             `json:"skippedCount"`
	Skipped       []VolumeSkipped `json:"skipped"`
	RequestID     *ULID           `json:"requestId"`
	PushID        *ULID           `json:"pushId"`
	Note          string          `json:"note"`
}

// AgentVolumeSnapshotResponse is TS AgentVolumeSnapshotResponse.
type AgentVolumeSnapshotResponse struct {
	Version  VolumeContentVersion `json:"version"`
	Existing bool                 `json:"existing"`
}

// AgentBlobsCheckRequest is TS AgentBlobsCheckRequest.
type AgentBlobsCheckRequest struct {
	Sha256s []Sha256 `json:"sha256s"`
}

// AgentBlobsCheckResponse is TS AgentBlobsCheckResponse.
type AgentBlobsCheckResponse struct {
	Missing []Sha256 `json:"missing"`
}

// AgentPutBlobResponse is the answer of PUT /v1/agent/volume-content/blobs/{sha256}.
type AgentPutBlobResponse struct {
	Sha256    Sha256 `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// AgentVolumePushReport is TS AgentVolumePushReport.
type AgentVolumePushReport struct {
	Status     string                  `json:"status"` // applied | kept-live | failed
	ResolvedBy string                  `json:"resolvedBy"`
	ResolvedAt Ts                      `json:"resolvedAt"`
	Result     VolumeContentPushResult `json:"result"`
}

// ───────────────────────── agent local view (VOLUME_SYNC_API)

// VolumeLocalSnapshot is TS VolumeLocalSnapshot.
type VolumeLocalSnapshot struct {
	SnapshotID ULID    `json:"snapshotId"`
	VersionID  *ULID   `json:"versionId"`
	Seq        *int    `json:"seq"`
	TreeHash   Sha256  `json:"treeHash"`
	Trigger    string  `json:"trigger"`
	TakenAt    Ts      `json:"takenAt"`
	UploadedAt *Ts     `json:"uploadedAt"`
	FileCount  int     `json:"fileCount"`
	TotalBytes int64   `json:"totalBytes"`
	Actor      *string `json:"actor"`
	Error      *string `json:"error"`
}

// VolumeSyncLive is VolumeSyncStatus.live.
type VolumeSyncLive struct {
	TreeHash      *Sha256 `json:"treeHash"`
	FileCount     int     `json:"fileCount"`
	TotalBytes    int64   `json:"totalBytes"`
	ExcludedCount int     `json:"excludedCount"`
	ScannedAt     *Ts     `json:"scannedAt"`
	Changed       bool    `json:"changed"`
}

// VolumeSyncStatus is TS VolumeSyncStatus.
type VolumeSyncStatus struct {
	SiteSlug     Slug                  `json:"siteSlug"`
	VolumeSlug   string                `json:"volumeSlug"`
	Label        string                `json:"label"`
	Persist      VolumePersist         `json:"persist"`
	State        string                `json:"state"`
	LastSnapshot *VolumeLocalSnapshot  `json:"lastSnapshot"`
	Live         *VolumeSyncLive       `json:"live"`
	Push         *VolumeContentPush    `json:"push"`
	Queued       int                   `json:"queued"`
	LastError    *string               `json:"lastError"`
	History      []VolumeLocalSnapshot `json:"history"`
	Users        []string              `json:"users"`
}

// VolumeSyncSnapshotRequest is TS VolumeSyncSnapshotRequest.
type VolumeSyncSnapshotRequest struct {
	Note *string `json:"note,omitempty"`
}

// VolumeSyncSnapshotResponse is TS VolumeSyncSnapshotResponse.
type VolumeSyncSnapshotResponse struct {
	Snapshot VolumeLocalSnapshot `json:"snapshot"`
	Created  bool                `json:"created"`
}

// VolumePushPlan is TS VolumePushPlan.
type VolumePushPlan struct {
	PushID        ULID             `json:"pushId"`
	VersionID     ULID             `json:"versionId"`
	BaseKnown     bool             `json:"baseKnown"`
	BaseTreeHash  *Sha256          `json:"baseTreeHash"`
	LiveTreeHash  Sha256           `json:"liveTreeHash"`
	CloudTreeHash Sha256           `json:"cloudTreeHash"`
	FastForward   bool             `json:"fastForward"`
	UpToDate      bool             `json:"upToDate"`
	Items         []VolumePlanItem `json:"items"`
	Counts        map[string]int   `json:"counts"`
	Exclude       []string         `json:"exclude"`
	Users         []string         `json:"users"`
	Restart       bool             `json:"restart"`
}

// VolumeSyncApplyRequest is TS VolumeSyncApplyRequest.
type VolumeSyncApplyRequest struct {
	IntentID     ULID              `json:"intentId,omitempty"` // empty = no intent wait (§21 #104 (e))
	PushID       ULID              `json:"pushId"`
	Resolution   string            `json:"resolution"` // overwrite | keep-live | per-file
	Choices      map[string]string `json:"choices,omitempty"`
	Restart      bool              `json:"restart"`
	LiveTreeHash Sha256            `json:"liveTreeHash"`
}

// VolumeSyncApplyResponse is TS VolumeSyncApplyResponse.
type VolumeSyncApplyResponse struct {
	Push   VolumeContentPush       `json:"push"`
	Result VolumeContentPushResult `json:"result"`
}

// VolumeSyncKeepLiveRequest is the body of POST …/volume-sync/{volumeSlug}/keep-live.
type VolumeSyncKeepLiveRequest struct {
	PushID ULID `json:"pushId"`
}

// VolumeSyncStateLabel is VOLUME_SYNC_STATE_LABEL.
var VolumeSyncStateLabel = map[string]string{
	"in-sync": "In sync", "live-changed": "Live changed", "cloud-ahead": "Cloud ahead", "conflict": "Conflict",
	"never-snapshotted": "No snapshot yet", "pending-upload": "Waiting to upload", "error": "Error",
}
