package shared

import (
	"math"
	"regexp"
	"strconv"

	"opm/agent/internal/canonical"
	"opm/agent/internal/cloud/doc"
)

// Value exports of shared/src/types.ts.
const (
	// MASK is MASK.
	MASK = "•••"
	// REVEAL_SECONDS is REVEAL_SECONDS.
	REVEAL_SECONDS = 60
	// REVEAL_RATE_PER_MINUTE is REVEAL_RATE_PER_MINUTE.
	REVEAL_RATE_PER_MINUTE = 30
	// SEED_ARCHIVE_MAX is SEED_ARCHIVE_MAX.
	SEED_ARCHIVE_MAX = 200 * 1024 * 1024
	// GUI_UPGRADE_MIN_VERSION is GUI_UPGRADE_MIN_VERSION.
	GUI_UPGRADE_MIN_VERSION = "0.10.0"
	// AGENT_UPGRADE_HEALTH_SEC is AGENT_UPGRADE_HEALTH_SEC.
	AGENT_UPGRADE_HEALTH_SEC = 60
	// AGENT_UPGRADE_REQUEST_TTL_SEC is AGENT_UPGRADE_REQUEST_TTL_SEC.
	AGENT_UPGRADE_REQUEST_TTL_SEC = 24 * 3600
	// UPLOAD_PART_SIZE is UPLOAD_PART_SIZE.
	UPLOAD_PART_SIZE = 8 * 1024 * 1024
	// SMALL_BLOB_MAX is SMALL_BLOB_MAX.
	SMALL_BLOB_MAX = 24 * 1024 * 1024
	// BLOB_STREAM_MAX is BLOB_STREAM_MAX.
	BLOB_STREAM_MAX = 8 * 1024 * 1024
	// TRANSFER_MAX is TRANSFER_MAX.
	TRANSFER_MAX = 2 * 1024 * 1024 * 1024
	// INTENT_MAX_SPEED is INTENT_MAX_SPEED.
	INTENT_MAX_SPEED = 4
	// INTENT_SPEED_HEARTBEAT_MS is INTENT_SPEED_HEARTBEAT_MS.
	INTENT_SPEED_HEARTBEAT_MS = 500
	// INTENT_SPEED_DECAY_MS is INTENT_SPEED_DECAY_MS.
	INTENT_SPEED_DECAY_MS = 1500
	// INTENT_SPEED_X4_AFTER_MS is INTENT_SPEED_X4_AFTER_MS.
	INTENT_SPEED_X4_AFTER_MS = 1500
	// INTENT_SPEED_EASE_MS is INTENT_SPEED_EASE_MS.
	INTENT_SPEED_EASE_MS = 400
	// ICON_MAX_BYTES is ICON_MAX_BYTES.
	ICON_MAX_BYTES = 65536
	// CLOCK_SKEW_WARN_SEC is CLOCK_SKEW_WARN_SEC.
	CLOCK_SKEW_WARN_SEC = 120
	// AGENT_LEGACY_SCHEMA is AGENT_LEGACY_SCHEMA.
	AGENT_LEGACY_SCHEMA = 1
	// VOLUME_COPIES_DIR is VOLUME_COPIES_DIR.
	VOLUME_COPIES_DIR = "volume-copies"
	// VOLUME_OP_RATE_PER_MINUTE is VOLUME_OP_RATE_PER_MINUTE.
	VOLUME_OP_RATE_PER_MINUTE = 6
	// VOLUME_RESTORE_UPLOAD_MAX is VOLUME_RESTORE_UPLOAD_MAX.
	VOLUME_RESTORE_UPLOAD_MAX = 2 * 1024 * 1024 * 1024
	// HOST_FS_MAX_ENTRIES is HOST_FS_MAX_ENTRIES.
	HOST_FS_MAX_ENTRIES = 5000
)

// VOLUME_BACKUP_SCHEDULES is VOLUME_BACKUP_SCHEDULES.
var VOLUME_BACKUP_SCHEDULES = []string{"off", "hourly", "6h", "daily", "weekly"}

// DEFAULT_VOLUME_BACKUP_POLICY is DEFAULT_VOLUME_BACKUP_POLICY (a fresh object).
func DEFAULT_VOLUME_BACKUP_POLICY() *doc.Map { return doc.Obj("schedule", "daily", "keep", 7.0) }

// PROPOSAL_TERMINAL is PROPOSAL_TERMINAL.
var PROPOSAL_TERMINAL = []string{"applied", "failed-rolled-back", "stale", "superseded"}

// INSTALL_TOKEN_KINDS is INSTALL_TOKEN_KINDS.
var INSTALL_TOKEN_KINDS = []string{"install", "maintenance"}

// INTENT_SPEEDS is INTENT_SPEEDS.
var INTENT_SPEEDS = []int{1, 2, 4}

// ICON_CONTENT_TYPES is ICON_CONTENT_TYPES.
var ICON_CONTENT_TYPES = []string{"image/png", "image/svg+xml", "image/webp"}

// IsSiteImage is isSiteImage.
func IsSiteImage(s *doc.Map) bool { return !s.Truthy("digest") }

var (
	jReAgentVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+](` + jLine + `+))?$`)
	jReCoreVersion  = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)
	jReDigits       = regexp.MustCompile(`^\d+$`)
)

type jVersion struct {
	core  [3]float64
	build string
}

func jParseVersion(v string) *jVersion {
	m := jReAgentVersion.FindStringSubmatch(doc.JSTrim(v))
	if m == nil {
		return nil
	}
	out := &jVersion{build: m[4]}
	for i := range out.core {
		out.core[i], _ = strconv.ParseFloat(m[i+1], 64)
	}
	return out
}

// CompareAgentVersions is compareAgentVersions.
func CompareAgentVersions(a, b string) int {
	x, y := jParseVersion(a), jParseVersion(b)
	if x == nil || y == nil {
		switch {
		case x != nil:
			return 1
		case y != nil:
			return -1
		}
		return 0
	}
	for i := range x.core {
		if x.core[i] != y.core[i] {
			if x.core[i] < y.core[i] {
				return -1
			}
			return 1
		}
	}
	s, t := x.build, y.build
	switch {
	case s == t:
		return 0
	case s == "":
		return -1
	case t == "":
		return 1
	}
	if jReDigits.MatchString(s) && jReDigits.MatchString(t) {
		n, _ := strconv.ParseFloat(s, 64)
		m, _ := strconv.ParseFloat(t, 64)
		switch {
		case n < m:
			return -1
		case n > m:
			return 1
		}
		return 0
	}
	if jCmp16(s, t) < 0 {
		return -1
	}
	return 1
}

// SupportsGuiUpgrade is supportsGuiUpgrade ("" = null).
func SupportsGuiUpgrade(agentVersion string) bool {
	if agentVersion == "" {
		return false
	}
	m := jReCoreVersion.FindStringSubmatch(doc.JSTrim(agentVersion))
	return m != nil && CompareAgentVersions(m[1], GUI_UPGRADE_MIN_VERSION) >= 0
}

// AgentUpgradeAvailable is agentUpgradeAvailable ("" = null).
func AgentUpgradeAvailable(installed, latest string) bool {
	return installed != "" && latest != "" && CompareAgentVersions(installed, latest) < 0
}

// AgentInstallAction is agentInstallAction ("" = null).
func AgentInstallAction(installed, target string) string {
	if installed == "" {
		return "fresh"
	}
	switch c := CompareAgentVersions(installed, target); {
	case c < 0:
		return "upgrade"
	case c > 0:
		return "downgrade"
	}
	return "same"
}

// AgentSchemaCompatible is agentSchemaCompatible: dbSchema number | nil, target { schema?, minSchema? }.
func AgentSchemaCompatible(dbSchema any, target *doc.Map) bool {
	num := func(v, d any) float64 { f, _ := doc.AsNumber(jCoal(v, d)); return f }
	db := num(dbSchema, float64(AGENT_LEGACY_SCHEMA))
	return db <= num(target.Val("schema"), float64(AGENT_LEGACY_SCHEMA)) && db >= num(target.Val("minSchema"), 0.0)
}

// jRound is Math.round.
func jRound(x float64) float64 { return math.Floor(x + 0.5) }

// FormatSkewDuration is formatSkewDuration.
func FormatSkewDuration(sec float64) string {
	s := jRound(math.Abs(sec))
	switch {
	case s < 60:
		return jNum(s) + " s"
	case s < 3600:
		return jNum(jRound(s/60)) + " min"
	case s < 86400:
		h, m := math.Floor(s/3600), jRound(math.Mod(s, 3600)/60)
		if m == 0 {
			return jNum(h) + " h"
		}
		if m == 60 {
			return jNum(h+1) + " h"
		}
		return jNum(h) + " h " + jNum(m) + " min"
	}
	d, h := math.Floor(s/86400), jRound(math.Mod(s, 86400)/3600)
	if h == 0 {
		return jNum(d) + " d"
	}
	return jNum(d) + " d " + jNum(h) + " h"
}

// ClockSkewWarning is clockSkewWarning (nil = null).
func ClockSkewWarning(skewSec any) *string {
	f, ok := skewSec.(float64)
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) <= CLOCK_SKEW_WARN_SEC {
		return nil
	}
	dir := "behind"
	if f > 0 {
		dir = "ahead"
	}
	s := "agent clock is " + FormatSkewDuration(f) + " " + dir + " — fix NTP on the server (e.g. sudo timedatectl set-ntp true)"
	return &s
}

// InstallTokenState is installTokenState.
func InstallTokenState(t *doc.Map, nowMs int64) string {
	switch {
	case t.Truthy("revokedAt"):
		return "revoked"
	case jIs(t.Val("kind"), "maintenance"):
		return "active"
	case t.Truthy("usedAt"):
		return "used"
	}
	if s, ok := t.Val("expiresAt").(string); ok && s != "" {
		if ms, ok := doc.ParseISO(s); ok && ms <= nowMs {
			return "expired"
		}
	}
	return "active"
}

// AuditActionMatches is auditActionMatches (audit.ts).
func AuditActionMatches(action, filter string) bool {
	if n := len(filter); n > 0 && filter[n-1] == '.' {
		return len(action) >= n && action[:n] == filter
	}
	return action == filter
}

// ActivityHashInput is activityHashInput (canonical.ts); a.data keeps the TS semantics of undefined and number formatting.
func ActivityHashInput(a *doc.Map) (string, error) {
	body := doc.New()
	for _, k := range []string{"activityId", "deploymentId", "siteSlug", "seq", "ts", "actor", "origin", "type", "summary", "data"} {
		if v, ok := a.Get(k); ok {
			body.Set(k, v)
		}
	}
	raw, err := doc.Marshal(body)
	if err != nil {
		return "", err
	}
	s, err := canonical.CanonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	prev := ""
	if !jNull(a.Val("prevHash")) {
		prev = jStr(a.Val("prevHash"))
	}
	return prev + s, nil
}

// AUDIT_TARGET_TYPES is AUDIT_TARGET_TYPES.
var AUDIT_TARGET_TYPES = []string{"registry", "service", "image", "notFoundPage", "template", "tag", "account", "deployment", "upload", "blob", "icon", "system", "github"}

// AUDIT_ACTIONS is AUDIT_ACTIONS.
var AUDIT_ACTIONS = []string{"registry.created", "registry.updated", "registry.deleted", "registry.tested", "registry.password-revealed", "service.created", "service.updated", "service.deleted", "image.inspected", "image.registered", "image.updated", "image.deleted", "image.import-retried", "image.import-ready", "image.import-failed", "not-found-page.created", "not-found-page.deleted", "template.created", "template.updated", "template.deleted", "template.committed", "template.merged", "template.reverted", "template.reset", "template.working-discarded", "tag.created", "tag.updated", "tag.deleted", "account.created", "account.updated", "account.access-changed", "account.disabled", "account.enabled", "account.password-reset", "account.password-set", "account.password-revealed", "account.reset-link-created", "account.reset-link-revealed", "deployment.created", "deployment.updated", "deployment.deleted", "deployment.access-key-revealed", "deployment.access-key-rotated", "deployment.install-command-created", "deployment.install-command-revealed", "deployment.upgrade-command-revealed", "deployment.upgrade-command-regenerated", "deployment.secret-revealed", "deployment.secret-regenerated", "deployment.agent-upgrade-requested", "deployment.agent-upgrade-cancelled", "deployment.template-synced", "deployment.template-unlinked", "deployment.committed", "deployment.merged", "deployment.reverted", "deployment.reset", "deployment.working-discarded", "blob.uploaded", "upload.created", "upload.completed", "icon.uploaded", "icon.deleted", "github.updated", "github.tested", "github.token-revealed", "github.synced", "github.resynced", "github.push-failed", "system.daily-cleanup", "system.migration"}

// AUDIT_DEPLOYMENT_OPS is AUDIT_DEPLOYMENT_OPS.
var AUDIT_DEPLOYMENT_OPS = []string{"revealAccessKey", "rotateAccessKey", "createInstallToken", "revealInstallCommand", "revealUpgradeCommand", "regenerateUpgradeCommand", "revealSecret", "regenerateSecret", "requestAgentUpgrade", "upgradeAllAgents", "cancelAgentUpgrade", "cancelAgentUpgradeRun", "applyTemplateSync", "unlinkTemplate", "commitDeployment", "discardDeploymentWorking"}

// CREDENTIAL_OPS is CREDENTIAL_OPS.
var CREDENTIAL_OPS = []string{"createDeployment", "revealAccessKey", "rotateAccessKey", "createInstallToken", "revealInstallCommand", "revealUpgradeCommand", "regenerateUpgradeCommand", "revealSecret", "regenerateSecret", "revealRegistryPassword", "createAccount", "setAccountPassword", "revealAccountPassword", "resetAccountPassword", "revealAccountResetLink", "revealGithubToken"}

// AUDIT_EXEMPT_OPS is AUDIT_EXEMPT_OPS.
var AUDIT_EXEMPT_OPS = []string{"putUploadPart", "previewTemplateSync", "compareBranches", "previewMerge", "createStage", "putStageBlob", "deleteStage"}

// ACTIVITY_TYPES is ACTIVITY_TYPES.
var ACTIVITY_TYPES = []string{"install.enrolled", "agent.started", "agent.port-bind-failed", "login.succeeded", "login.failed", "logout", "actor.declared", "access-key.rotated", "access-key.received", "access-key.revealed", "proposal.published", "proposal.created", "proposal.staged", "proposal.stale", "proposal.superseded", "proposal.approved", "update.started", "update.snapshot", "update.applied", "update.failed", "update.rolled-back", "update.abandoned", "update.terminated", "update.recovered", "config.local-change", "snapshot.created", "snapshot.downloaded", "snapshot.imported", "snapshot.deleted", "snapshot.rolled-back", "volume.seeded", "volume.reset-to-seed", "transfer.sent", "transfer.received", "transfer.saved", "file.published", "file.fetched", "file.downloaded", "file.saved", "file.copy-dropped", "file.unpublished", "secret.revealed", "secret.regenerated", "secret.fetched", "internet.tunnel.started", "internet.tunnel.stopped", "internet.tunnel.revoked", "internet.tunnel.expired", "debug.downloaded", "edge.reloaded", "edge.reload-failed", "cert.issued", "cert.renewed", "cert.failed", "cert.local-ca-downloaded", "site.unreachable", "site.reachable", "deployment.created", "deployment.updated", "install-token.created", "install-token.revoked", "install-token.revealed", "install-command.revealed", "install-command.regenerated", "agent.revoked", "draft.reset-to-head", "draft.updated", "transfer.deleted", "activities.uploaded", "deployment.deleted", "terminal.opened", "terminal.closed", "container.restarted", "container.stopped", "container.started", "tool.netcheck", "volume.downloaded", "docker.images-pruned", "agent.upgrade-started", "agent.upgraded", "agent.upgrade-failed", "agent.rolled-back", "agent.restarted", "agent.config-changed", "agent.support-bundle-downloaded", "agent.upgrade-requested", "agent.upgrade-cancelled", "remote-gui.opened", "remote-gui.session", "agent.remote-gui-changed", "tunnel.created", "tunnel.deleted", "tunnel.enabled", "tunnel.disabled", "agent.home-move-started", "agent.home-moved", "agent.home-move-failed", "agent.home-old-deleted", "draft.template-synced", "deployment.template-unlinked", "volume.emptied", "volume.copied", "volume.restored", "volume.op-failed", "volume.copy-deleted", "volume.action-forwarded", "volume.file-written", "volume.file-uploaded", "volume.file-folder-created", "volume.file-moved", "volume.file-deleted", "volume.placeholder-filled", "volume-content.snapshot", "volume-content.snapshot-uploaded", "volume-content.snapshot-failed", "volume-content.conflict", "volume-content.applied", "volume-content.kept-live", "volume-content.apply-failed", "volume-content.version-created", "volume-content.snapshot-requested", "volume-content.sent", "volume-content.push-cancelled", "devtools.query", "devtools.query-cancelled", "devtools.connection-created", "devtools.connection-updated", "devtools.connection-deleted", "devtools.connection-tested", "devtools.credential-revealed", "devtools.mqtt-published", "devtools.mqtt-subscribed", "devtools.mqtt-unsubscribed", "devtools.action-forwarded", "branch.created", "branch.committed", "branch.merged", "branch.cherry-picked", "branch.reverted", "branch.reset", "branch.merge-failed", "working.discarded", "drift.detected", "drift.committed", "drift.discarded", "drift.accepted", "agent-branch.pushed"}

// AuditOp is one row of AUDIT_OPS.
type AuditOp struct{ Action, TargetType string }

// AUDIT_OPS is AUDIT_OPS.
var AUDIT_OPS = map[string]AuditOp{
	"createRegistry":           {"registry.created", "registry"},
	"patchRegistry":            {"registry.updated", "registry"},
	"deleteRegistry":           {"registry.deleted", "registry"},
	"testRegistry":             {"registry.tested", "registry"},
	"revealRegistryPassword":   {"registry.password-revealed", "registry"},
	"createService":            {"service.created", "service"},
	"patchService":             {"service.updated", "service"},
	"deleteService":            {"service.deleted", "service"},
	"inspectImage":             {"image.inspected", "image"},
	"registerImage":            {"image.registered", "image"},
	"patchImage":               {"image.updated", "image"},
	"deleteImage":              {"image.deleted", "image"},
	"retryImport":              {"image.import-retried", "image"},
	"createNotFoundPage":       {"not-found-page.created", "notFoundPage"},
	"deleteNotFoundPage":       {"not-found-page.deleted", "notFoundPage"},
	"createTemplate":           {"template.created", "template"},
	"patchTemplate":            {"template.updated", "template"},
	"deleteTemplate":           {"template.deleted", "template"},
	"commitTemplate":           {"template.committed", "template"},
	"discardTemplateWorking":   {"template.working-discarded", "template"},
	"merge":                    {"deployment.merged", "deployment"},
	"revertCommit":             {"deployment.reverted", "deployment"},
	"resetToCommit":            {"deployment.reset", "deployment"},
	"commitDeployment":         {"deployment.committed", "deployment"},
	"discardDeploymentWorking": {"deployment.working-discarded", "deployment"},
	"createTag":                {"tag.created", "tag"},
	"patchTag":                 {"tag.updated", "tag"},
	"deleteTag":                {"tag.deleted", "tag"},
	"createAccount":            {"account.created", "account"},
	"patchAccount":             {"account.updated", "account"},
	"disableAccount":           {"account.disabled", "account"},
	"resetAccountPassword":     {"account.reset-link-created", "account"},
	"setAccountPassword":       {"account.password-set", "account"},
	"revealAccountPassword":    {"account.password-revealed", "account"},
	"revealAccountResetLink":   {"account.reset-link-revealed", "account"},
	"createDeployment":         {"deployment.created", "deployment"},
	"patchDeployment":          {"deployment.updated", "deployment"},
	"deleteDeployment":         {"deployment.deleted", "deployment"},
	"revealAccessKey":          {"deployment.access-key-revealed", "deployment"},
	"rotateAccessKey":          {"deployment.access-key-rotated", "deployment"},
	"createInstallToken":       {"deployment.install-command-created", "deployment"},
	"revealInstallCommand":     {"deployment.install-command-revealed", "deployment"},
	"revealUpgradeCommand":     {"deployment.upgrade-command-revealed", "deployment"},
	"regenerateUpgradeCommand": {"deployment.upgrade-command-regenerated", "deployment"},
	"revealSecret":             {"deployment.secret-revealed", "deployment"},
	"regenerateSecret":         {"deployment.secret-regenerated", "deployment"},
	"requestAgentUpgrade":      {"deployment.agent-upgrade-requested", "deployment"},
	"upgradeAllAgents":         {"deployment.agent-upgrade-requested", "deployment"},
	"cancelAgentUpgrade":       {"deployment.agent-upgrade-cancelled", "deployment"},
	"cancelAgentUpgradeRun":    {"deployment.agent-upgrade-cancelled", "deployment"},
	"applyTemplateSync":        {"deployment.template-synced", "deployment"},
	"unlinkTemplate":           {"deployment.template-unlinked", "deployment"},
	"uploadBlob":               {"blob.uploaded", "blob"},
	"createUpload":             {"upload.created", "upload"},
	"completeUpload":           {"upload.completed", "upload"},
	"uploadIcon":               {"icon.uploaded", "icon"},
	"deleteIcon":               {"icon.deleted", "icon"},
	"patchGithubMirror":        {"github.updated", "github"},
	"testGithubMirror":         {"github.tested", "github"},
	"revealGithubToken":        {"github.token-revealed", "github"},
	"syncGithubMirror":         {"github.synced", "github"},
	"resyncGithubMirror":       {"github.resynced", "github"},
}

// ERROR_HTTP_STATUS is ERROR_HTTP_STATUS.
var ERROR_HTTP_STATUS = map[string]int{
	"unauthenticated":    401,
	"session-expired":    401,
	"invalid-key":        401,
	"actor-required":     401,
	"forbidden":          403,
	"not-found":          404,
	"method-not-allowed": 405,
	"conflict":           409,
	"stale":              409,
	"gone":               410,
	"too-large":          413,
	"invalid":            422,
	"gate-closed":        409,
	"locked":             409,
	"no-internet":        503,
	"unavailable":        503,
	"rate-limited":       429,
	"internal":           500,
}
