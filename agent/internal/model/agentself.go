package model

// Agent self-management (SPEC-final §7.5, §21 #90): Go mirror of shared/src/agent-self.ts and the §21 #90 types of
// shared/src/types.ts (AgentReleaseInfo, AgentUpgradeProgress, ReleaseVerification, AgentUpgradeRequest,
// AgentUpgradeRun, compareAgentVersions, supportsGuiUpgrade, agentUpgradeAvailable, validateAgentSettings,
// uninstallCommands, SUPPORT_BUNDLE_SECTIONS).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GuiUpgradeMinVersion is TS GUI_UPGRADE_MIN_VERSION.
const GuiUpgradeMinVersion = "0.10.0"

// AgentUpgradeHealthSec is TS AGENT_UPGRADE_HEALTH_SEC.
const AgentUpgradeHealthSec = 60

// AgentUpgradeRequestTTLSec is TS AGENT_UPGRADE_REQUEST_TTL_SEC.
const AgentUpgradeRequestTTLSec = 24 * 3600

// AgentUpgradePhase values (TS AgentUpgradePhase).
const (
	UpgradeQueued     = "queued"
	UpgradeDownload   = "download"
	UpgradeVerify     = "verify"
	UpgradeSwap       = "swap"
	UpgradeRestart    = "restart"
	UpgradeHealth     = "health"
	UpgradeDone       = "done"
	UpgradeFailed     = "failed"
	UpgradeRolledBack = "rolled-back"
)

// UpgradeTerminal reports whether a phase is final.
func UpgradeTerminal(phase string) bool {
	return phase == UpgradeDone || phase == UpgradeFailed || phase == UpgradeRolledBack
}

// AgentReleaseInfo is TS AgentReleaseInfo.
type AgentReleaseInfo struct {
	Version string  `json:"version"`
	Notes   *string `json:"notes"`
}

// ReleaseVerification is TS ReleaseVerification.
type ReleaseVerification struct {
	Version              string  `json:"version"`
	Sha256               string  `json:"sha256"`
	SignatureOk          bool    `json:"signatureOk"`
	PublicKeySource      string  `json:"publicKeySource"` // agent.json | cloud | flag
	PublicKeyFingerprint string  `json:"publicKeyFingerprint"`
	Sha256Ok             *bool   `json:"sha256Ok"`
	SelfTestOk           *bool   `json:"selfTestOk"`
	Error                *string `json:"error"`
	CheckedAt            Ts      `json:"checkedAt"`
}

// AgentUpgradeProgress is TS AgentUpgradeProgress.
type AgentUpgradeProgress struct {
	UpgradeID ULID    `json:"upgradeId"`
	RequestID *string `json:"requestId"`
	RunID     *string `json:"runId"`
	Via       string  `json:"via"` // gui | cloud | cli | mesh
	SiteSlug  Slug    `json:"siteSlug"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Phase     string  `json:"phase"`
	// FailedPhase: the phase that failed when phase = failed | rolled-back (rolled-back: health, or restart when the
	// new process never came up)
	FailedPhase  *string              `json:"failedPhase,omitempty"`
	Progress     *float64             `json:"progress"`
	BytesDone    int64                `json:"bytesDone"`
	BytesTotal   *int64               `json:"bytesTotal"`
	Verification *ReleaseVerification `json:"verification"`
	Error        *string              `json:"error"`
	Actor        *string              `json:"actor"`
	StartedAt    Ts                   `json:"startedAt"`
	UpdatedAt    Ts                   `json:"updatedAt"`
	FinishedAt   *Ts                  `json:"finishedAt"`
}

// AgentUpgradeRequest is TS AgentUpgradeRequest.
type AgentUpgradeRequest struct {
	RequestID      string  `json:"requestId"`
	SiteSlug       Slug    `json:"siteSlug"`
	Version        string  `json:"version"`
	RequestedBy    string  `json:"requestedBy"`
	RequestedByUID string  `json:"requestedByUid"`
	RequestedAt    Ts      `json:"requestedAt"`
	ExpiresAt      Ts      `json:"expiresAt"`
	RunID          *string `json:"runId"`
}

// AgentUpgradeRunSite is one row of AgentUpgradeRun.sites.
type AgentUpgradeRunSite struct {
	SiteSlug  Slug    `json:"siteSlug"`
	Status    string  `json:"status"` // pending | requested | running | done | failed | skipped | cancelled
	From      *string `json:"from"`
	RequestID *string `json:"requestId"`
	Error     *string `json:"error"`
	At        *Ts     `json:"at"`
}

// AgentUpgradeRun is TS AgentUpgradeRun.
type AgentUpgradeRun struct {
	RunID       string                `json:"runId"`
	Origin      string                `json:"origin"` // cloud | agent
	Coordinator *Slug                 `json:"coordinator"`
	Version     string                `json:"version"`
	RequestedBy string                `json:"requestedBy"`
	RequestedAt Ts                    `json:"requestedAt"`
	Status      string                `json:"status"` // running | done | failed | cancelled
	Sites       []AgentUpgradeRunSite `json:"sites"`
	FinishedAt  *Ts                   `json:"finishedAt"`
}

// ───────────────────────── agent-self.ts: info

// AgentBuildInfo is TS AgentBuildInfo.
type AgentBuildInfo struct {
	Version   string  `json:"version"`
	GoVersion string  `json:"goVersion"`
	OS        string  `json:"os"`
	Arch      string  `json:"arch"`
	Commit    *string `json:"commit"`
	BuiltAt   *string `json:"builtAt"`
}

// AgentRestartGuard is AgentSupervisorInfo.restartGuard.
type AgentRestartGuard struct {
	Installed  bool    `json:"installed"`
	DropInPath *string `json:"dropInPath"`
	ScriptPath *string `json:"scriptPath"`
}

// AgentSupervisorInfo is TS AgentSupervisorInfo.
type AgentSupervisorInfo struct {
	Kind         string            `json:"kind"` // systemd | none
	Unit         *string           `json:"unit"`
	ActiveState  *string           `json:"activeState"`
	SubState     *string           `json:"subState"`
	RestartGuard AgentRestartGuard `json:"restartGuard"`
}

// AgentEnrollmentInfo is TS AgentEnrollmentInfo.
type AgentEnrollmentInfo struct {
	State              string   `json:"state"` // enrolled | not-enrolled | revoked
	DeploymentID       string   `json:"deploymentId"`
	SiteSlug           Slug     `json:"siteSlug"`
	APIBase            string   `json:"apiBase"`
	EnrolledAt         *Ts      `json:"enrolledAt"`
	SiteKeyFingerprint *string  `json:"siteKeyFingerprint"`
	PublicKey          *string  `json:"publicKey"`
	KeyVersion         int      `json:"keyVersion"`
	ReenrollHelp       []string `json:"reenrollHelp"`
}

// AgentCloudInfo is TS AgentCloudInfo.
type AgentCloudInfo struct {
	Route           string  `json:"route"`
	Internet        bool    `json:"internet"`
	LastFetchAt     *Ts     `json:"lastFetchAt"`
	LastHeartbeatAt *Ts     `json:"lastHeartbeatAt"`
	LastError       *string `json:"lastError"`
	ConsoleURL      *string `json:"consoleUrl"`
	DeploymentLabel *string `json:"deploymentLabel"`
}

// AgentDockerInfo is TS AgentDockerInfo.
type AgentDockerInfo struct {
	Ok         bool    `json:"ok"`
	Host       string  `json:"host"`
	Version    *string `json:"version"`
	APIVersion *string `json:"apiVersion"`
	Error      *string `json:"error"`
}

// AgentPortsEdge is AgentPortsInfo.edge.
type AgentPortsEdge struct {
	Running     bool    `json:"running"`
	DefaultEdge bool    `json:"defaultEdge"`
	HTTPPort    *int    `json:"httpPort"`
	HTTPSPort   *int    `json:"httpsPort"`
	LastError   *string `json:"lastError"`
}

// AgentPortsInfo is TS AgentPortsInfo.
type AgentPortsInfo struct {
	AgentPort           int            `json:"agentPort"`
	GuiAliasPort        *int           `json:"guiAliasPort"`
	DefaultEdgeHTTPPort *int           `json:"defaultEdgeHttpPort"`
	Edge                AgentPortsEdge `json:"edge"`
	GuiPorts            []GuiPort      `json:"guiPorts"`
}

// AgentSelfRelease is TS AgentSelfRelease (AgentReleaseInfo + check data).
type AgentSelfRelease struct {
	Version          string               `json:"version"`
	Notes            *string              `json:"notes"`
	Sha256           *string              `json:"sha256"`
	Source           string               `json:"source"` // cloud-view | release-check
	CheckedAt        *Ts                  `json:"checkedAt"`
	UpgradeAvailable bool                 `json:"upgradeAvailable"`
	Newer            bool                 `json:"newer"`
	Verification     *ReleaseVerification `json:"verification"`
}

// AgentPreviousBinary is AgentSelfInfo.previousBinary.
type AgentPreviousBinary struct {
	Path    string  `json:"path"`
	Exists  bool    `json:"exists"`
	Version *string `json:"version"`
	Sha256  *string `json:"sha256"`
}

// AgentBusy is AgentSelfInfo.busy.
type AgentBusy struct {
	Kind  string `json:"kind"` // update | rollback | snapshot | restore | upgrade
	ID    string `json:"id"`
	Since Ts     `json:"since"`
}

// AgentSelfInfo is TS AgentSelfInfo.
type AgentSelfInfo struct {
	SiteSlug       Slug                  `json:"siteSlug"`
	Self           bool                  `json:"self"`
	Version        string                `json:"version"`
	Build          AgentBuildInfo        `json:"build"`
	PID            int                   `json:"pid"`
	StartedAt      Ts                    `json:"startedAt"`
	UptimeSec      int64                 `json:"uptimeSec"`
	ConfigPath     *string               `json:"configPath"`
	DataDir        string                `json:"dataDir"`
	BinaryPath     string                `json:"binaryPath"`
	PreviousBinary AgentPreviousBinary   `json:"previousBinary"`
	Ports          AgentPortsInfo        `json:"ports"`
	Docker         AgentDockerInfo       `json:"docker"`
	Supervisor     AgentSupervisorInfo   `json:"supervisor"`
	Enrollment     AgentEnrollmentInfo   `json:"enrollment"`
	Cloud          AgentCloudInfo        `json:"cloud"`
	Release        *AgentSelfRelease     `json:"release"`
	Upgrade        *AgentUpgradeProgress `json:"upgrade"`
	Busy           *AgentBusy            `json:"busy"`
	Uninstall      []string              `json:"uninstall"`
}

// ───────────────────────── agent-self.ts: requests / responses

// StartAgentUpgradeRequest is TS StartAgentUpgradeRequest.
type StartAgentUpgradeRequest struct {
	Version string `json:"version"`
	Confirm Slug   `json:"confirm"`
	Force   bool   `json:"force,omitempty"`
}

// AgentUpgradeStateResponse is TS AgentUpgradeStateResponse.
type AgentUpgradeStateResponse struct {
	Current *AgentUpgradeProgress  `json:"current"`
	History []AgentUpgradeProgress `json:"history"`
}

// StartUpgradeAllRequest is TS StartUpgradeAllRequest.
type StartUpgradeAllRequest struct {
	Version string `json:"version"`
	Confirm string `json:"confirm"` // "UPGRADE"
}

// AgentUpgradeRunState is TS AgentUpgradeRunState.
type AgentUpgradeRunState struct {
	Run *AgentUpgradeRun `json:"run"`
}

// RestartAgentRequest is TS RestartAgentRequest.
type RestartAgentRequest struct {
	Confirm Slug   `json:"confirm"`
	Reason  string `json:"reason,omitempty"`
}

// RestartAgentResponse is TS RestartAgentResponse.
type RestartAgentResponse struct {
	Restarting bool   `json:"restarting"`
	Via        string `json:"via"` // systemd | exec
}

// AgentToolsSettings is AgentEditableSettings.tools.
type AgentToolsSettings struct {
	HostShell     bool `json:"hostShell"`
	ContainerExec bool `json:"containerExec"`
	DevTools      bool `json:"devTools"` // §21 #97 (default true)
}

// AgentEditableSettings is TS AgentEditableSettings.
type AgentEditableSettings struct {
	Tools          AgentToolsSettings `json:"tools"`
	ContainerNames string             `json:"containerNames"`
	EdgeHTTPPort   *int               `json:"edgeHttpPort"`
	GuiAliasPort   *int               `json:"guiAliasPort"`
}

// AgentConfigView is TS AgentConfigView. Masked is agent.json with secrets masked (key order kept).
type AgentConfigView struct {
	Path           string                `json:"path"`
	Masked         OrderedObject         `json:"masked"`
	Settings       AgentEditableSettings `json:"settings"`
	Writable       bool                  `json:"writable"`
	WritableReason *string               `json:"writableReason"`
	RestartPending bool                  `json:"restartPending"`
}

// PatchAgentToolsSettings is the partial tools object of PatchAgentConfigRequest.
type PatchAgentToolsSettings struct {
	HostShell     *bool `json:"hostShell,omitempty"`
	ContainerExec *bool `json:"containerExec,omitempty"`
	DevTools      *bool `json:"devTools,omitempty"` // §21 #97
}

// PatchAgentSettings is PatchAgentConfigRequest.settings (absent = unchanged; null ports = none).
type PatchAgentSettings struct {
	Tools          *PatchAgentToolsSettings `json:"tools,omitempty"`
	ContainerNames *string                  `json:"containerNames,omitempty"`
	EdgeHTTPPort   Optional[int]            `json:"edgeHttpPort"`
	GuiAliasPort   Optional[int]            `json:"guiAliasPort"`
}

// PatchAgentConfigRequest is TS PatchAgentConfigRequest.
type PatchAgentConfigRequest struct {
	Settings PatchAgentSettings `json:"settings"`
	Confirm  Slug               `json:"confirm"`
	Restart  *bool              `json:"restart,omitempty"`
}

// PatchAgentConfigResponse is TS PatchAgentConfigResponse.
type PatchAgentConfigResponse struct {
	Changed    []string        `json:"changed"`
	Restarting bool            `json:"restarting"`
	Config     AgentConfigView `json:"config"`
}

// AgentSettingsIssue is TS AgentSettingsIssue.
type AgentSettingsIssue struct {
	Path     string `json:"path"`
	Code     string `json:"code"` // V_PORT | V_PORT_CLASH | V_ENUM | V_TYPE | V_CONTAINER_NAMES
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// SupportBundleSections is TS SUPPORT_BUNDLE_SECTIONS.
var SupportBundleSections = []string{"version.json", "agent.json", "debug/", "docker-info.json", "containers.json", "activities.jsonl", "status.json", "upgrade/"}

// ───────────────────────── functions (mirrors of the TS helpers)

var agentVersionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+](.+))?$`)
var digitsRE = regexp.MustCompile(`^\d+$`)

type parsedVersion struct {
	core  [3]float64
	build string
}

func parseAgentVersion(v string) *parsedVersion {
	m := agentVersionRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil
	}
	p := &parsedVersion{build: m[4]}
	for i := 0; i < 3; i++ {
		p.core[i], _ = strconv.ParseFloat(m[i+1], 64)
	}
	return p
}

// CompareAgentVersions is TS compareAgentVersions: MAJOR.MINOR.PATCH numerically, then the BUILD stamp (absent = lowest;
// numeric when both are digits, else string order). Unparsable versions sort first.
func CompareAgentVersions(a, b string) int {
	x, y := parseAgentVersion(a), parseAgentVersion(b)
	if x == nil || y == nil {
		switch {
		case x != nil:
			return 1
		case y != nil:
			return -1
		}
		return 0
	}
	for i := 0; i < 3; i++ {
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
	if digitsRE.MatchString(s) && digitsRE.MatchString(t) {
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
	if s < t {
		return -1
	}
	return 1
}

var coreRE = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)

// SupportsGuiUpgrade is TS supportsGuiUpgrade (core version ≥ GUI_UPGRADE_MIN_VERSION).
func SupportsGuiUpgrade(agentVersion string) bool {
	if agentVersion == "" {
		return false
	}
	m := coreRE.FindStringSubmatch(strings.TrimSpace(agentVersion))
	return m != nil && CompareAgentVersions(m[1], GuiUpgradeMinVersion) >= 0
}

// AgentUpgradeAvailable is TS agentUpgradeAvailable (latest newer than installed; an unknown side → false).
func AgentUpgradeAvailable(installed, latest string) bool {
	if installed == "" || latest == "" {
		return false
	}
	return CompareAgentVersions(installed, latest) < 0
}

// ValidateAgentSettings is TS validateAgentSettings (same rules, same order of issues).
func ValidateAgentSettings(s AgentEditableSettings, agentPort int, previous *AgentEditableSettings) []AgentSettingsIssue {
	out := []AgentSettingsIssue{}
	port := func(path string, v *int) {
		if v == nil {
			return
		}
		if *v < 1 || *v > 65535 {
			out = append(out, AgentSettingsIssue{Path: path, Code: "V_PORT", Message: "a TCP port 1–65535, or empty for none", Severity: "error"})
		} else if *v == agentPort {
			out = append(out, AgentSettingsIssue{Path: path, Code: "V_PORT_CLASH", Message: fmt.Sprintf("port %d is the agent port (mesh + GUI)", *v), Severity: "error"})
		}
	}
	port("edgeHttpPort", s.EdgeHTTPPort)
	port("guiAliasPort", s.GuiAliasPort)
	if s.EdgeHTTPPort != nil && s.GuiAliasPort != nil && *s.EdgeHTTPPort == *s.GuiAliasPort {
		out = append(out, AgentSettingsIssue{Path: "guiAliasPort", Code: "V_PORT_CLASH", Message: fmt.Sprintf("port %d is also the default edge port", *s.GuiAliasPort), Severity: "error"})
	}
	validNames := s.ContainerNames == "plain" || s.ContainerNames == "prefixed"
	if !validNames {
		out = append(out, AgentSettingsIssue{Path: "containerNames", Code: "V_ENUM", Message: "plain or prefixed", Severity: "error"})
	}
	// tools.hostShell / tools.containerExec are typed bool in Go (a non-boolean fails JSON decoding with V_TYPE)
	if previous != nil && previous.ContainerNames != s.ContainerNames && validNames {
		out = append(out, AgentSettingsIssue{Path: "containerNames", Code: "V_CONTAINER_NAMES", Message: "the running containers are renamed in place at the restart (no recreate); a clash with another container stops the next update with a staging error", Severity: "info"})
	}
	if s.EdgeHTTPPort == nil {
		out = append(out, AgentSettingsIssue{Path: "edgeHttpPort", Code: "V_PORT", Message: "no default edge: before the first update the GUI answers only on the agent / alias port", Severity: "warning"})
	}
	return out
}

// UninstallCommands is TS uninstallCommands.
func UninstallCommands(configPath, dataDir, binaryPath string, systemd bool) []string {
	lines := []string{}
	if systemd {
		// §21 #91: the socket unit goes first (a held socket would start the service again on the next connection)
		lines = append(lines, "sudo systemctl disable --now opm-agent.socket opm-agent 2>/dev/null; sudo systemctl disable --now opm-agent",
			"sudo rm -f /etc/systemd/system/opm-agent.service /etc/systemd/system/opm-agent.socket", "sudo rm -rf /etc/systemd/system/opm-agent.service.d", "sudo systemctl daemon-reload")
	} else {
		lines = append(lines, `sudo pkill -f "opm-agent run"`)
	}
	lines = append(lines, fmt.Sprintf("sudo rm -f %s %s.prev %s.new", binaryPath, binaryPath, binaryPath), "sudo rm -f /usr/local/lib/opm/upgrade-guard.sh")
	lines = append(lines, "# optional — removes the site identity, snapshots and volumes (the OPM containers keep running until removed):")
	lines = append(lines, fmt.Sprintf("sudo rm -rf %s %s /etc/opm/install-token /etc/opm/installed-deps.json", dataDir, configPath))
	return lines
}

// ───────────────────────── §21 #91 build metadata, schema, health

// AgentLegacySchema is TS AGENT_LEGACY_SCHEMA (agents before 0.10.0 count as schema 1).
const AgentLegacySchema = 1

// AgentBuildMeta is TS AgentBuildMeta (`opm-agent version --json`, dist/release.json).
type AgentBuildMeta struct {
	Version   string  `json:"version"`
	Schema    int     `json:"schema"`
	MinSchema int     `json:"minSchema"`
	Commit    *string `json:"commit"`
	BuiltAt   *string `json:"builtAt"`
	OS        string  `json:"os,omitempty"`
	Arch      string  `json:"arch,omitempty"`
}

// AgentInstallAction is TS agentInstallAction: fresh | upgrade | downgrade | same.
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

// AgentSchemaCompatible is TS agentSchemaCompatible (nil = unknown → AGENT_LEGACY_SCHEMA; minSchema nil → 0).
func AgentSchemaCompatible(dbSchema, targetSchema, targetMinSchema *int) bool {
	db, max, min := AgentLegacySchema, AgentLegacySchema, 0
	if dbSchema != nil {
		db = *dbSchema
	}
	if targetSchema != nil {
		max = *targetSchema
	}
	if targetMinSchema != nil {
		min = *targetMinSchema
	}
	return db <= max && db >= min
}

// AgentHealthCloud is AgentHealthResponse.cloud.
type AgentHealthCloud struct {
	Connected     bool   `json:"connected"`
	Route         string `json:"route"`
	LastContactAt *Ts    `json:"lastContactAt"`
}

// AgentHealthCheck is one AgentHealthResponse.checks row.
type AgentHealthCheck struct {
	Name    string  `json:"name"`
	Ok      bool    `json:"ok"`
	Message *string `json:"message"`
}

// AgentHealthResponse is TS AgentHealthResponse (GET /_opm/api/health; 503 with the same body while not ok).
type AgentHealthResponse struct {
	Ok        bool             `json:"ok"`
	Version   string           `json:"version"`
	Schema    int              `json:"schema"`
	PID       int              `json:"pid"`
	StartedAt Ts               `json:"startedAt"`
	Cloud     AgentHealthCloud `json:"cloud"`
	Docker    struct {
		Ok bool `json:"ok"`
	} `json:"docker"`
	SocketActivated bool               `json:"socketActivated"`
	Checks          []AgentHealthCheck `json:"checks"`
}

// TerminateUpdateRequest is TS TerminateUpdateRequest (§21 #110): stop a running update on every site of its plan.
// Nothing is rolled back unless Rollback; RestartAgent restarts every agent (with Rollback: the roll back runs after
// the restart).
type TerminateUpdateRequest struct {
	Reason       string `json:"reason,omitempty"`
	Rollback     bool   `json:"rollback,omitempty"`
	RestartAgent bool   `json:"restartAgent,omitempty"`
}

// TerminateSiteResult is one site's answer to a terminate.
type TerminateSiteResult struct {
	SiteSlug Slug   `json:"siteSlug"`
	Ok       bool   `json:"ok"`
	Message  string `json:"message"`
}

// TerminateUpdateResponse is TS TerminateUpdateResponse.
type TerminateUpdateResponse struct {
	UpdateID ULID                  `json:"updateId"`
	Sites    []TerminateSiteResult `json:"sites"`
}
