package model

// §21 #104 branches + commits: Go mirror of shared/src/branch.ts (json tags identical).

// BranchesMinAgent is TS BRANCHES_MIN_AGENT.
const BranchesMinAgent = "0.14.0"

// Commit message limits (TS COMMIT_MESSAGE_MAX / COMMIT_SUMMARY_MAX).
const (
	CommitMessageMax    = 2000
	CommitSummaryMax    = 200
	CommitPicksMax      = 500
	CommitStatsKeysMax  = 200
	CloudRecentCommits  = 10
	ErrDirtyWorkingCopy = "dirty-working-copy" // §21 #104 (0.14.0); 0.14.1 answers ReasonUncommittedDrift
	// ReasonUncommittedDrift is details.reason of the 409 conflict while changes made outside the agent are uncommitted (§21 #105 J12).
	ReasonUncommittedDrift = "uncommitted-drift"
	// ReasonHeadMoved is details.reason of the 409 stale when baseCommitId / targetHead is not the head (§21 #105 J11).
	ReasonHeadMoved = "head-moved"
)

// BranchRef is TS BranchRef: {kind:'template',templateId} | {kind:'deployment',deploymentId} | {kind:'agent',deploymentId,siteSlug}.
type BranchRef struct {
	Kind         string `json:"kind"`
	TemplateID   string `json:"templateId,omitempty"`
	DeploymentID string `json:"deploymentId,omitempty"`
	SiteSlug     string `json:"siteSlug,omitempty"`
}

// Key is TS branchKey.
func (b BranchRef) Key() string {
	switch b.Kind {
	case "template":
		return "template:" + b.TemplateID
	case "deployment":
		return "deployment:" + b.DeploymentID
	}
	return "agent:" + b.DeploymentID + "/" + b.SiteSlug
}

// CommitAuthor is TS CommitAuthor.
type CommitAuthor struct {
	Kind     string  `json:"kind"` // person | system | unknown (§21 #113 auto-commit of Docker changes)
	Email    string  `json:"email"`
	UID      *string `json:"uid"`
	Name     *string `json:"name"`
	SiteSlug *string `json:"siteSlug"`
}

// CommitSource is TS CommitSource. Picks is []string or "all".
type CommitSource struct {
	Branch    BranchRef `json:"branch"`
	CommitID  *string   `json:"commitId"`
	Seq       *int      `json:"seq"`
	Label     string    `json:"label"`
	Picks     any       `json:"picks"`
	Skipped   int       `json:"skipped"`
	Truncated bool      `json:"truncated,omitempty"`
}

// CommitStats is TS CommitStats.
type CommitStats struct {
	Added     int      `json:"added"`
	Changed   int      `json:"changed"`
	Removed   int      `json:"removed"`
	Keys      []string `json:"keys"`
	Truncated bool     `json:"truncated"`
}

// CommitSeqRef is TS { commitId; seq }.
type CommitSeqRef struct {
	CommitID string `json:"commitId"`
	Seq      int    `json:"seq"`
}

// SnapshotRefOnly is TS { snapshotId }.
type SnapshotRefOnly struct {
	SnapshotID string `json:"snapshotId"`
}

// AcceptedDrift is an item of CommitData.acceptedDrift.
type AcceptedDrift struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	Detail      string `json:"detail"`
}

// CommitVolumeContent is an item of CommitData.volumeContent.
type CommitVolumeContent struct {
	SiteSlug   string  `json:"siteSlug"`
	VolumeSlug string  `json:"volumeSlug"`
	TreeHash   string  `json:"treeHash"`
	VersionID  *string `json:"versionId"`
}

// MigratedFrom is CommitData.migratedFrom.
type MigratedFrom struct {
	RevID          string `json:"revId,omitempty"`
	Seq            int    `json:"seq,omitempty"`
	ProposalStatus string `json:"proposalStatus,omitempty"`
	LocalProposal  bool   `json:"localProposal,omitempty"`
}

// CommitData is TS CommitData.
type CommitData struct {
	AcceptedDrift []AcceptedDrift       `json:"acceptedDrift,omitempty"`
	VolumeContent []CommitVolumeContent `json:"volumeContent,omitempty"`
	MigratedFrom  *MigratedFrom         `json:"migratedFrom,omitempty"`
	UpdateID      *string               `json:"updateId,omitempty"`
	FastForward   *FastForwardUse       `json:"fastForward,omitempty"`
	// §21 #105 secret VALUES changed by the commit (slugs only, never a value)
	SecretValues *SecretValueChanges `json:"secretValues,omitempty"`
	// §21 #105 a pre-0.14.1 working copy auto-committed (migration)
	AutoCommitted *AutoCommitted `json:"autoCommitted,omitempty"`
}

// SecretValueChanges is TS SecretValueChanges (§21 #105).
type SecretValueChanges struct {
	Set       []string `json:"set"`
	Generated []string `json:"generated"`
	Cleared   []string `json:"cleared"`
}

// Any reports whether something changed.
func (s *SecretValueChanges) Any() bool {
	return s != nil && len(s.Set)+len(s.Generated)+len(s.Cleared) > 0
}

// AutoCommitted is TS CommitData.autoCommitted (§21 #105 J13 / J14).
type AutoCommitted struct {
	From  string `json:"from"` // cloud-working-copy | agent-working-copy
	Items int    `json:"items"`
}

// Commit is TS Commit.
type Commit struct {
	CommitID    string           `json:"commitId"`
	Branch      BranchRef        `json:"branch"`
	Seq         int              `json:"seq"`
	ParentID    *string          `json:"parentId"`
	Kind        string           `json:"kind"`
	Message     string           `json:"message"`
	Author      CommitAuthor     `json:"author"`
	CreatedAt   Ts               `json:"createdAt"`
	Source      *CommitSource    `json:"source"`
	RevertOf    *CommitSeqRef    `json:"revertOf"`
	ResetTo     *CommitSeqRef    `json:"resetTo"`
	RollbackOf  *SnapshotRefOnly `json:"rollbackOf"`
	Config      Config           `json:"config"`
	Hash        Sha256           `json:"hash"`
	Catalog     *RevisionCatalog `json:"catalog"`
	CatalogHash *Sha256          `json:"catalogHash"`
	Stats       CommitStats      `json:"stats"`
	Data        CommitData       `json:"data"`
}

// CommitMeta is a Commit without config + catalog (UpdatePlan.commits).
type CommitMeta struct {
	CommitID    string           `json:"commitId"`
	Branch      BranchRef        `json:"branch"`
	Seq         int              `json:"seq"`
	ParentID    *string          `json:"parentId"`
	Kind        string           `json:"kind"`
	Message     string           `json:"message"`
	Author      CommitAuthor     `json:"author"`
	CreatedAt   Ts               `json:"createdAt"`
	Source      *CommitSource    `json:"source"`
	RevertOf    *CommitSeqRef    `json:"revertOf"`
	ResetTo     *CommitSeqRef    `json:"resetTo"`
	RollbackOf  *SnapshotRefOnly `json:"rollbackOf"`
	Hash        Sha256           `json:"hash"`
	CatalogHash *Sha256          `json:"catalogHash"`
	Stats       CommitStats      `json:"stats"`
	Data        CommitData       `json:"data"`
}

// CommitView is TS CommitView (masked config).
type CommitView struct {
	CommitID    string           `json:"commitId"`
	Branch      BranchRef        `json:"branch"`
	Seq         int              `json:"seq"`
	ParentID    *string          `json:"parentId"`
	Kind        string           `json:"kind"`
	Message     string           `json:"message"`
	Author      CommitAuthor     `json:"author"`
	CreatedAt   Ts               `json:"createdAt"`
	Source      *CommitSource    `json:"source"`
	RevertOf    *CommitSeqRef    `json:"revertOf"`
	ResetTo     *CommitSeqRef    `json:"resetTo"`
	RollbackOf  *SnapshotRefOnly `json:"rollbackOf"`
	Config      ConfigView       `json:"config"`
	Hash        Sha256           `json:"hash"`
	Catalog     *RevisionCatalog `json:"catalog"`
	CatalogHash *Sha256          `json:"catalogHash"`
	Stats       CommitStats      `json:"stats"`
	Data        CommitData       `json:"data"`
}

// CommitSummary is TS CommitSummary.
type CommitSummary struct {
	CommitID   string           `json:"commitId"`
	Branch     BranchRef        `json:"branch"`
	Seq        int              `json:"seq"`
	ParentID   *string          `json:"parentId"`
	Kind       string           `json:"kind"`
	Message    string           `json:"message"`
	Author     CommitAuthor     `json:"author"`
	CreatedAt  Ts               `json:"createdAt"`
	Source     *CommitSource    `json:"source"`
	RevertOf   *CommitSeqRef    `json:"revertOf"`
	ResetTo    *CommitSeqRef    `json:"resetTo"`
	RollbackOf *SnapshotRefOnly `json:"rollbackOf"`
	Hash       Sha256           `json:"hash"`
	Stats      CommitStats      `json:"stats"`
}

// CommitRef is TS CommitRef.
type CommitRef struct {
	CommitID  string       `json:"commitId"`
	Seq       int          `json:"seq"`
	Hash      Sha256       `json:"hash"`
	Message   string       `json:"message"`
	Author    CommitAuthor `json:"author"`
	CreatedAt Ts           `json:"createdAt"`
	Kind      string       `json:"kind"`
}

// CloudHeadRef is TS CommitRef & { catalogHash }.
type CloudHeadRef struct {
	CommitRef
	CatalogHash *Sha256 `json:"catalogHash"`
}

// Ref returns the CommitRef of a commit.
func (c *Commit) Ref() CommitRef {
	return CommitRef{CommitID: c.CommitID, Seq: c.Seq, Hash: c.Hash, Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt, Kind: c.Kind}
}

// Summary returns the CommitSummary of a commit.
func (c *Commit) Summary() CommitSummary {
	return CommitSummary{CommitID: c.CommitID, Branch: c.Branch, Seq: c.Seq, ParentID: c.ParentID, Kind: c.Kind, Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt, Source: c.Source, RevertOf: c.RevertOf, ResetTo: c.ResetTo, RollbackOf: c.RollbackOf, Hash: c.Hash, Stats: c.Stats}
}

// Meta returns the commit without config + catalog.
func (c *Commit) Meta() CommitMeta {
	return CommitMeta{CommitID: c.CommitID, Branch: c.Branch, Seq: c.Seq, ParentID: c.ParentID, Kind: c.Kind, Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt, Source: c.Source, RevertOf: c.RevertOf, ResetTo: c.ResetTo, RollbackOf: c.RollbackOf, Hash: c.Hash, CatalogHash: c.CatalogHash, Stats: c.Stats, Data: c.Data}
}

// MergeSkip is TS MergeSkip.
type MergeSkip struct {
	Key        string `json:"key"`
	TheirsHash string `json:"theirsHash"`
	SourceSeq  *int   `json:"sourceSeq"`
	SkippedAt  Ts     `json:"skippedAt"`
	SkippedBy  string `json:"skippedBy"`
}

// MaskedSlot is TS MaskedSlot.
type MaskedSlot struct {
	Has   bool `json:"has"`
	Value any  `json:"value"`
}

// MergeBlob is MergeItem.blob.
type MergeBlob struct {
	Base       *string `json:"base"`
	Target     *string `json:"target"`
	Source     *string `json:"source"`
	VolumeSlug string  `json:"volumeSlug,omitempty"`
	Path       *string `json:"path,omitempty"`
	FileSlug   string  `json:"fileSlug,omitempty"`
}

// MergeItem is TS MergeItem.
type MergeItem struct {
	Key        string      `json:"key"`
	Section    string      `json:"section"`
	SiteSlug   *string     `json:"siteSlug"`
	Label      string      `json:"label"`
	Kind       string      `json:"kind"`
	Effect     string      `json:"effect"`
	Base       *MaskedSlot `json:"base"`
	Target     MaskedSlot  `json:"target"`
	Source     MaskedSlot  `json:"source"`
	Preselect  bool        `json:"preselect"`
	Scalar     bool        `json:"scalar"`
	Requires   []string    `json:"requires"`
	Locked     *string     `json:"locked"`
	Blob       *MergeBlob  `json:"blob,omitempty"`
	Note       string      `json:"note,omitempty"`
	SourceHash string      `json:"sourceHash"`
	SkippedAt  string      `json:"skippedAt,omitempty"`
}

// MergePlanBase is TS MergePlanBase.
type MergePlanBase struct {
	Kind           string  `json:"kind"` // record | origin | none
	SourceCommitID *string `json:"sourceCommitId"`
	Label          string  `json:"label"`
}

// BranchPoint is TS BranchPoint (at: "head" | "working" | {commitId}).
type BranchPoint struct {
	Branch BranchRef `json:"branch"`
	At     any       `json:"at"`
}

// MergePlanSource is MergePlan.source.
type MergePlanSource struct {
	Point  BranchPoint `json:"point"`
	Label  string      `json:"label"`
	Commit *CommitRef  `json:"commit"`
}

// MergePlanTarget is MergePlan.target.
type MergePlanTarget struct {
	Branch       BranchRef  `json:"branch"`
	Label        string     `json:"label"`
	Head         *CommitRef `json:"head"`
	WorkingClean bool       `json:"workingClean"`
	Uncommitted  int        `json:"uncommitted"`
}

// MergeReferences is MergePlan.references.
type MergeReferences struct {
	UpdatedSites []string `json:"updatedSites"`
}

// MergePlan is TS MergePlan.
type MergePlan struct {
	Source      MergePlanSource  `json:"source"`
	Target      MergePlanTarget  `json:"target"`
	Base        MergePlanBase    `json:"base"`
	Items       []MergeItem      `json:"items"`
	Counts      map[string]int   `json:"counts"`
	FastForward bool             `json:"fastForward"`
	References  *MergeReferences `json:"references,omitempty"`
	DiffOnly    bool             `json:"diffOnly"`
}

// MergeChoice is TS MergeChoice.
type MergeChoice struct {
	Key   string `json:"key"`
	Take  string `json:"take"` // source | target | custom
	Value any    `json:"value,omitempty"`
}

// MergeMissingVariable is an item of MergeMissing.variables.
type MergeMissingVariable struct {
	VariableSlug string   `json:"variableSlug"`
	Label        string   `json:"label"`
	Type         string   `json:"type"`
	Options      []string `json:"options"`
	Required     bool     `json:"required"`
}

// MergeMissingSecret is an item of MergeMissing.secrets.
type MergeMissingSecret struct {
	SecretSlug string `json:"secretSlug"`
	Label      string `json:"label"`
}

// MergeMissing is TS MergeMissing.
type MergeMissing struct {
	Variables []MergeMissingVariable `json:"variables"`
	Secrets   []MergeMissingSecret   `json:"secrets"`
}

// ChoiceError is an item of MergePreview.choiceErrors.
type ChoiceError struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// RuntimeAction is an item of MergePreview.runtime.
type RuntimeAction struct {
	ContainerSlug string `json:"containerSlug"`
	Action        string `json:"action"`
}

// PreviewValidation is MergePreview.validation.
type PreviewValidation struct {
	Errors   []ValidationIssue `json:"errors"`
	Warnings []ValidationIssue `json:"warnings"`
}

// MergePreview is TS MergePreview.
type MergePreview struct {
	Result       ConfigView        `json:"result"`
	Changes      []DiffChange      `json:"changes"`
	Validation   PreviewValidation `json:"validation"`
	Unresolved   []string          `json:"unresolved"`
	Missing      MergeMissing      `json:"missing"`
	ChoiceErrors []ChoiceError     `json:"choiceErrors"`
	Kind         string            `json:"kind"`
	Taken        int               `json:"taken"`
	Skipped      int               `json:"skipped"`
	Runtime      []RuntimeAction   `json:"runtime,omitempty"`
	Warnings     []string          `json:"warnings"`
}

// CommitDetail is TS CommitDetail.
type CommitDetail struct {
	Commit  CommitView  `json:"commit"`
	Changes []MergeItem `json:"changes"`
}

// AgentBranchUncommitted is TS AgentBranchUncommitted.
type AgentBranchUncommitted struct {
	Working   int     `json:"working"`
	Drift     int     `json:"drift"`
	Notes     int     `json:"notes"`
	ScannedAt *string `json:"scannedAt"`
}

// AgentBranchReport is TS AgentBranchReport (heartbeat + ping).
type AgentBranchReport struct {
	Head        *CommitRef             `json:"head"`
	MergedFrom  *CommitSeqRef          `json:"mergedFrom"`
	Uncommitted AgentBranchUncommitted `json:"uncommitted"`
}

// CloudBranchView is TS CloudBranchView (CloudView.branch).
type CloudBranchView struct {
	Head   *CloudHeadRef `json:"head"`
	Recent []CommitRef   `json:"recent"`
}

// PushAgentBranchRequest is TS PushAgentBranchRequest.
type PushAgentBranchRequest struct {
	Commits []Commit `json:"commits"`
}

// PushAgentBranchResponse is TS PushAgentBranchResponse.
type PushAgentBranchResponse struct {
	Stored []string   `json:"stored"`
	Head   *CommitRef `json:"head"`
}

// AgentCommitResponse is the body of GET /v1/agent/commits/{id} and the peer cloud-commit route.
type AgentCommitResponse struct {
	Commit Commit `json:"commit"`
}

// DriftItem is TS DriftItem (MergeItem + origin, detail, observedAt).
type DriftItem struct {
	MergeItem
	Origin     string `json:"origin"` // docker | file | secret-file | volume-content | edit
	Detail     string `json:"detail"`
	ObservedAt Ts     `json:"observedAt"`
}

// DriftNote is TS DriftNote.
type DriftNote struct {
	Fingerprint string   `json:"fingerprint"`
	Key         *string  `json:"key"`
	Label       string   `json:"label"`
	Detail      string   `json:"detail"`
	Actions     []string `json:"actions"` // discard | accept | take-bytes
}

// DriftReport is TS DriftReport.
type DriftReport struct {
	ScannedAt    Ts          `json:"scannedAt"`
	HeadCommitID *string     `json:"headCommitId"`
	Items        []DriftItem `json:"items"`
	Notes        []DriftNote `json:"notes"`
	LocalImages  []string    `json:"localImages"`
}

// UncommittedResponse is TS UncommittedResponse.
type UncommittedResponse struct {
	Items        []DriftItem `json:"items"`
	Notes        []DriftNote `json:"notes"`
	ScannedAt    *string     `json:"scannedAt"`
	HeadCommitID *string     `json:"headCommitId"`
}

// AgentBranchCloud is AgentBranchState.cloud.
type AgentBranchCloud struct {
	Head   *CloudHeadRef          `json:"head"`
	Recent []CommitRef            `json:"recent"`
	Staged map[string]SiteStaging `json:"staged"`
}

// AgentBranchLocal is AgentBranchState.local.
type AgentBranchLocal struct {
	Head       *CommitRef      `json:"head"`
	Commits    []CommitSummary `json:"commits"`
	MergedFrom *CommitSeqRef   `json:"mergedFrom"`
	Ahead      int             `json:"ahead"`
	Behind     int             `json:"behind"`
	Differs    int             `json:"differs"`
}

// AgentBranchSite is an item of AgentBranchState.sites (+ the peer branch route).
type AgentBranchSite struct {
	SiteSlug       string                  `json:"siteSlug"`
	Reachable      bool                    `json:"reachable"`
	AgentVersion   *string                 `json:"agentVersion"`
	Head           *CommitRef              `json:"head"`
	MergedFrom     *CommitSeqRef           `json:"mergedFrom"`
	Uncommitted    *AgentBranchUncommitted `json:"uncommitted"`
	StrictlyBehind bool                    `json:"strictlyBehind"`
	CanFastForward bool                    `json:"canFastForward"`
	Reason         *string                 `json:"reason"`
}

// AgentBranchState is TS AgentBranchState.
type AgentBranchState struct {
	SiteSlug    string                 `json:"siteSlug"`
	Cloud       AgentBranchCloud       `json:"cloud"`
	Local       AgentBranchLocal       `json:"local"`
	Uncommitted AgentBranchUncommitted `json:"uncommitted"`
	Run         *UpdateRun             `json:"run"`
	Sites       []AgentBranchSite      `json:"sites"`
}

// PeerBranchResponse is the body of GET /_opm/peer/branch.
type PeerBranchResponse struct {
	AgentBranchReport
	SiteSlug       string  `json:"siteSlug"`
	AgentVersion   string  `json:"agentVersion"`
	StrictlyBehind bool    `json:"strictlyBehind"`
	CanFastForward bool    `json:"canFastForward"`
	Reason         *string `json:"reason"`
}

// LocalCommitRequest is TS LocalCommitRequest. §21 #105: GUI edits mode = Config (+ BaseCommitID, required with
// Config); drift mode = Keys / Accept / TakeBytes. Never both.
type LocalCommitRequest struct {
	Message string `json:"message"`
	// Config: the full edited config from the browser (secrets: plainValue absent = keep the head value)
	Config *ConfigInput `json:"config,omitempty"`
	// BaseCommitID: the head the browser edited; Opt so that an explicit null (no head) is told apart from absent
	BaseCommitID Optional[string] `json:"baseCommitId"`
	// StageID (§21 #107): the local stage holding the new blobs of Config; promoted into the blob store before the apply
	StageID   string   `json:"stageId,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	Accept    []string `json:"accept,omitempty"`
	TakeBytes []string `json:"takeBytes,omitempty"`
}

// LocalCommitResponse is TS LocalCommitResponse.
type LocalCommitResponse struct {
	Commit *Commit    `json:"commit"`
	Run    *UpdateRun `json:"run"`
}

// LocalDiscardRequest is TS LocalDiscardRequest.
type LocalDiscardRequest struct {
	Keys  []string `json:"keys"`
	Notes []string `json:"notes,omitempty"`
}

// RunResponse is { run }.
type RunResponse struct {
	Run *UpdateRun `json:"run"`
}

// LocalCompareRequest is TS LocalCompareRequest (source: "cloud" | {cloudCommitId} | {localCommitId}).
type LocalCompareRequest struct {
	Source   any  `json:"source"`
	DiffOnly bool `json:"diffOnly,omitempty"`
}

// LocalMergeRequest is TS LocalMergeRequest.
type LocalMergeRequest struct {
	SourceCommitID string                   `json:"sourceCommitId"`
	TargetHead     *string                  `json:"targetHead"`
	Choices        []MergeChoice            `json:"choices"`
	Values         map[string]VariableValue `json:"values,omitempty"`
	Message        string                   `json:"message,omitempty"`
}

// FastForwardAllRequest is TS FastForwardAllRequest.
type FastForwardAllRequest struct {
	SourceCommitID string   `json:"sourceCommitId"`
	Sites          []string `json:"sites"`
	Message        string   `json:"message"`
}

// LocalRevertRequest is TS LocalRevertRequest.
type LocalRevertRequest struct {
	CommitID   string        `json:"commitId"`
	Message    string        `json:"message"`
	TargetHead *string       `json:"targetHead"`
	Choices    []MergeChoice `json:"choices,omitempty"`
}

// UpdatePlanTarget is an item of UpdatePlan.targets.
type UpdatePlanTarget struct {
	Revision  Revision `json:"revision"`
	FromRevID *RevID   `json:"fromRevId"`
}

// ───────────────────────── §21 #106 drift push (POST /v1/agent/branch/drift; TS github-mirror.ts)

// DriftItemSummary is TS DriftItemSummary.
type DriftItemSummary struct {
	Key    string `json:"key"`
	Origin string `json:"origin"`
	Detail string `json:"detail"`
}

// DriftNoteSummary is TS DriftNoteSummary.
type DriftNoteSummary struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	Detail      string `json:"detail"`
}

// AgentDriftPushRequest is TS AgentDriftPushRequest: the site's uncommitted drift (no items + no notes = cleared,
// liveConfig null). Never holds a secret value (liveConfig is a ConfigView).
type AgentDriftPushRequest struct {
	HeadCommitID *string            `json:"headCommitId"`
	ScannedAt    Ts                 `json:"scannedAt"`
	Fingerprint  string             `json:"fingerprint"`
	Items        []DriftItemSummary `json:"items"`
	Notes        []DriftNoteSummary `json:"notes"`
	LiveConfig   *ConfigView        `json:"liveConfig"`
}

// AgentDriftPushResponse is TS AgentDriftPushResponse.
type AgentDriftPushResponse struct {
	Stored   bool `json:"stored"`
	Mirrored bool `json:"mirrored"`
}
