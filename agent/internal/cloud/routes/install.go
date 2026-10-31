package routes

import (
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/core"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// Install path: script, token-gated agent binary, enroll (routes/install.ts, routes/install-script.ts). The template is
// core.RenderInstallScript; loadDepsManifest, resolveAgentVersion, detectedAddresses* and peerInfo are in core/route_install.go.

func init() {
	cloud.Handlers(map[string]cloud.Handler{
		"installScript":  installScriptOp,
		"installBinary":  installBinary,
		"installDeps":    installDeps,
		"installRelease": installRelease,
		"enroll":         enroll,
		"agentRelease":   agentRelease,
		"agentBinary":    agentBinary,
	})
}

// SYSTEMD_UNIT is SYSTEMD_UNIT.
const SYSTEMD_UNIT = `[Unit]
Description=On-Prem Manager agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
[Service]
Type=simple
ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json
Restart=always
RestartSec=5
LimitNOFILE=65536
KillMode=process
[Install]
WantedBy=multi-user.target
`

// Constants of routes/install.ts. The agent negotiates the Docker Engine API between 1.41 (floor, deps/manifest.json
// minDockerApi overrides) and 1.47 (cap).
const (
	DEFAULT_MIN_DOCKER_API = "1.41"
	AGENT_MAX_DOCKER_API   = "1.47"
)

var (
	// VERSION_RE is VERSION_RE.
	VERSION_RE = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,64}$`)

	reShqBad  = regexp.MustCompile(`[^A-Za-z0-9._~:/?#@!$&'()*+,;=%-]`)
	reIPv4Lit = regexp.MustCompile(`^[0-9.]{7,15}$`)
	reIPv6Lit = regexp.MustCompile(`^[0-9A-Fa-f:]{2,39}$`)
)

func shq(s string) string { return reShqBad.ReplaceAllString(s, "") }

// shIp: an IPv4 / IPv6 literal or "" (shell-safe).
func shIp(s string) string {
	v := strings.TrimPrefix(doc.JSTrim(s), "::ffff:")
	if reIPv4Lit.MatchString(v) || reIPv6Lit.MatchString(v) {
		return v
	}
	return ""
}

func posInt(n any) bool {
	f, ok := n.(float64)
	return ok && f == float64(int64(f)) && f > 0
}

func shNum(n any) string {
	if posInt(n) {
		return core.JSString(n)
	}
	return ""
}

// ArchFile is the sha256 + release signature of one linux arch.
type ArchFile struct{ Sha256, Sig string }

// PinnedAgentInfo is the agent of InstallScriptOpts. Schema, MinSchema: number or nil.
type PinnedAgentInfo struct {
	Version           string
	Files             map[string]ArchFile
	Schema, MinSchema any
}

// PinnedDocker is the docker of InstallScriptOpts: the pinned Docker static bundle.
type PinnedDocker struct {
	Version string
	// MinAPI: "" = undefined
	MinAPI string
	Sha256 map[string]string
}

// InstallScriptOpts is InstallScriptOpts. Agent: pinned release; a missing arch → the script fails clearly. Docker nil =
// none uploaded → manual Docker install only.
type InstallScriptOpts struct {
	ApiBase, DeploymentID, SiteSlug, Token, ReleasePublicKey string
	Agent                                                    *PinnedAgentInfo
	Docker                                                   *PinnedDocker
	// PortAgent, PortAlias, PortEdge: number, nil or doc.Undefined
	PortAgent, PortAlias, PortEdge any
	// UpgradeOnly: §21 #86: the token already enrolled this site → upgrade-only script (never writes the token)
	UpgradeOnly bool
	// SeenIP: §21 #86: the client IP the cloud saw when the script was requested
	SeenIP string
	// RequestedVersion: §21 #91: the script was requested with ?version=<v>
	RequestedVersion string
	// Kind: §21 #92 install | maintenance (the site's regenerated upgrade command; always upgrade-only)
	Kind string
}

// InstallScript is installScript: pinned versions + sha256 embedded at generation (never "latest" at run time).
func InstallScript(o InstallScriptOpts) string {
	a := o.Agent
	if a == nil {
		a = &PinnedAgentInfo{}
	}
	d := o.Docker
	if d == nil {
		d = &PinnedDocker{}
	}
	scriptQS := ""
	if o.RequestedVersion != "" && VERSION_RE.MatchString(o.RequestedVersion) {
		scriptQS = "?version=" + o.RequestedVersion
	}
	minAPI := d.MinAPI
	if minAPI == "" {
		minAPI = DEFAULT_MIN_DOCKER_API
	}
	minSchema := ""
	if f, ok := a.MinSchema.(float64); ok && f == float64(int64(f)) && f >= 0 {
		minSchema = core.JSString(f)
	}
	agentPort := shNum(o.PortAgent)
	if agentPort == "" {
		agentPort = "7080"
	}
	aliasPort := ""
	if o.PortAlias != nil {
		if aliasPort = shNum(o.PortAlias); aliasPort == "" {
			aliasPort = "8080"
		}
	}
	bool01 := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	desc := "This install command already enrolled site $SITE"
	if o.Kind == "maintenance" {
		desc = "This upgrade command belongs to the enrolled site $SITE"
	}
	return core.RenderInstallScript(map[string]string{
		"API":              shq(o.ApiBase),
		"DEP":              shq(o.DeploymentID),
		"SITE":             shq(o.SiteSlug),
		"TOKEN":            shq(o.Token),
		"RELEASE_PUBKEY":   shq(o.ReleasePublicKey),
		"AGENT_VERSION":    shq(a.Version),
		"AGENT_SCHEMA":     shNum(a.Schema),
		"AGENT_MIN_SCHEMA": minSchema,
		"SCRIPT_QS":        scriptQS,
		"AGENT_SHA_AMD64":  shq(a.Files["amd64"].Sha256),
		"AGENT_SIG_AMD64":  shq(a.Files["amd64"].Sig),
		"AGENT_SHA_ARM64":  shq(a.Files["arm64"].Sha256),
		"AGENT_SIG_ARM64":  shq(a.Files["arm64"].Sig),
		"DOCKER_VERSION":   shq(d.Version),
		"MIN_DOCKER_API":   shq(minAPI),
		"MAX_DOCKER_API":   AGENT_MAX_DOCKER_API,
		"DOCKER_SHA_AMD64": shq(d.Sha256["amd64"]),
		"DOCKER_SHA_ARM64": shq(d.Sha256["arm64"]),
		"AGENT_PORT":       agentPort,
		"ALIAS_PORT":       aliasPort,
		"EDGE_PORT":        shNum(o.PortEdge),
		"SYSTEMD_UNIT":     SYSTEMD_UNIT,
		"UPGRADE_ONLY":     bool01(o.UpgradeOnly || o.Kind == "maintenance"),
		"CMD_DESC":         desc,
		"SEEN_IP":          shIp(o.SeenIP),
		"HTTP_PORT":        "80",
		// §21 #90: the pinned agent upgrades itself from the GUIs (final message + boot guard expectation)
		"GUI_UPGRADE": bool01(shared.SupportsGuiUpgrade(a.Version)),
	})
}

// firstWord is s.trim().split(/\s+/)[0].
func firstWord(s string) string {
	if f := strings.FieldsFunc(s, doc.IsJSSpace); len(f) > 0 {
		return f[0]
	}
	return ""
}

// shaSig reads <bin>.sha256 + <bin>.sig; "" for a missing or unreadable one.
func shaSig(bin string) (string, string) {
	sha, _, err := core.ReadText(bin + ".sha256")
	if err != nil {
		sha = ""
	}
	sig, _, err := core.ReadText(bin + ".sig")
	if err != nil {
		sig = ""
	}
	return sha, sig
}

// pinnedAgent: agent release for the script: requested (§21 #91 ?version=), else the deps manifest agent version, else
// OPM_AGENT_VERSION / agent/latest; schema / minSchema from agent/<v>/release.json. manifestVersion "" = null.
func pinnedAgent(manifestVersion string) *PinnedAgentInfo {
	version, err := core.ResolveAgentVersion(manifestVersion)
	if err != nil {
		return nil
	}
	files := map[string]ArchFile{}
	for _, arch := range []string{"amd64", "arm64"} {
		sha, sig := shaSig(core.Paths.AgentBinary(version, "linux", arch))
		if sha != "" && sig != "" {
			files[arch] = ArchFile{firstWord(sha), doc.JSTrim(sig)}
		}
	}
	meta := BuildMeta(version)
	return &PinnedAgentInfo{version, files, fldOr(meta, "schema", nil), fldOr(meta, "minSchema", nil)}
}

// ReleaseMetaPath is releaseMetaPath: §21 #91 agent/<v>/release.json.
func ReleaseMetaPath(version string) string { return "agent/" + version + "/release.json" }

// BuildMeta is buildMeta: { schema, minSchema }, nil = none (builds before 0.10.0 → AGENT_LEGACY_SCHEMA).
func BuildMeta(version string) *doc.Map {
	t, _, err := core.ReadText(ReleaseMetaPath(version))
	if err != nil || t == "" {
		return nil
	}
	v, err := doc.ParseString(t)
	if err != nil {
		return nil
	}
	j, _ := v.(*doc.Map)
	ok := func(k string) bool {
		f, isNum := j.Val(k).(float64)
		return isNum && f == float64(int64(f)) && f >= 0
	}
	if !ok("schema") || !ok("minSchema") {
		return nil
	}
	return doc.Obj("schema", j.Val("schema"), "minSchema", j.Val("minSchema"))
}

func withSchemaHeaders(h map[string]string, m *doc.Map) map[string]string {
	if m != nil {
		h["X-OPM-Agent-Schema"] = core.JSString(m.Val("schema"))
		h["X-OPM-Agent-Min-Schema"] = core.JSString(m.Val("minSchema"))
	}
	return h
}

func shSingle(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ErrorScript is errorScript: §21 #91: curl -f hides error bodies, so an unusable token gets a tiny script that says why (exit 1).
func ErrorScript(lines []string) string {
	out := []string{"#!/bin/sh", "# On-Prem Manager agent installer: this install command cannot be used"}
	for _, l := range lines {
		out = append(out, `printf '%s\n' `+shSingle(l)+" >&2")
	}
	return strings.Join(append(out, "exit 1", ""), "\n")
}

const regen = "open the cloud Setup page → Install command → Regenerate (or View) and run the new command"

// TokenProblemText is tokenProblemText; siteSlug "" = null.
func TokenProblemText(reason, siteSlug string) []string {
	site := ""
	if siteSlug != "" {
		site = " (site " + siteSlug + ")"
	}
	const nothing = "Nothing was changed on this server."
	switch reason {
	case "expired":
		return []string{"OPM install: token expired" + site + " — " + regen + ".", nothing}
	case "revoked":
		return []string{"OPM install: token revoked" + site + " — " + regen + ".", nothing}
	case "used":
		return []string{"OPM install: this install command was used for an earlier enrollment" + site + "; that site was revoked or enrolled again with a newer command since — " + regen + ".", nothing}
	case "enrollment-ended":
		return []string{"OPM install: this upgrade command belongs to an enrollment" + site + " that was revoked — create a new install command for the site in the cloud Setup page.", nothing}
	case "deleted":
		return []string{"OPM install: the deployment of this install command was deleted.", nothing}
	}
	return []string{"OPM install: unknown install token — copy the install command again from the cloud Setup page (View / Regenerate).", nothing}
}

func scriptReply(body string) *cloud.Reply {
	return &cloud.Reply{Status: 200, Headers: map[string]string{"Content-Type": "text/x-shellscript; charset=utf-8", "Cache-Control": "no-store"}, Body: body}
}

// AgentReleaseMeta is what pinnedRelease returns. Notes, SizeBytes, Schema, MinSchema may be nil.
type AgentReleaseMeta struct {
	Version, Sha256, Signature          string
	Notes, SizeBytes, Schema, MinSchema any
}

// PinnedRelease is pinnedRelease: §21 #86: the pinned agent release of one os/arch (deps manifest agent version, else
// OPM_AGENT_VERSION / agent/latest). requested "" = null.
func PinnedRelease(os, arch, requested string) (*AgentReleaseMeta, error) {
	rel := core.AgentReleaseInfo(doc.Undefined)
	if requested == "" && rel == nil {
		return nil, cloud.E.NotFound("no agent release is uploaded (agent/latest missing)")
	}
	version := requested
	if version == "" {
		version = rel.Str("version")
	}
	// releaseOf: release metadata of one uploaded version; not-found when that os/arch is missing
	bin := core.Paths.AgentBinary(version, os, arch)
	sha, sig := shaSig(bin)
	var size any
	if n, err := core.SizeOf(bin); err == nil && n >= 0 {
		size = float64(n)
	}
	meta := BuildMeta(version)
	if sha == "" || sig == "" {
		return nil, cloud.E.NotFound("agent " + version + " for " + os + "/" + arch + " is not uploaded")
	}
	var notes any
	if rel != nil && rel.Val("version") == version {
		notes = rel.Val("notes")
	}
	return &AgentReleaseMeta{version, firstWord(sha), doc.JSTrim(sig), notes, size, fldOr(meta, "schema", nil), fldOr(meta, "minSchema", nil)}, nil
}

func releaseResponse(r *AgentReleaseMeta, os, arch, binaryBase string) *doc.Map {
	return doc.Obj("version", r.Version, "os", os, "arch", arch, "sha256", r.Sha256, "signature", r.Signature, "releasePublicKey", cloud.Env.ReleasePublicKey(),
		"binaryPath", binaryBase+"?os="+os+"&arch="+arch+"&version="+doc.EncodeURIComponent(r.Version), "notes", r.Notes, "sizeBytes", r.SizeBytes, "schema", r.Schema, "minSchema", r.MinSchema)
}

func osArch(c *cloud.Ctx) (string, string, error) {
	os, arch := c.Query.Get("os"), c.Query.Get("arch")
	if os != "linux" && os != "darwin" {
		return "", "", cloud.E.Bad("os", "V_ENUM", "os must be linux | darwin")
	}
	if arch != "amd64" && arch != "arm64" {
		return "", "", cloud.E.Bad("arch", "V_ENUM", "arch must be amd64 | arm64")
	}
	return os, arch, nil
}

// agentRelease: §21 #86 `opm-agent upgrade` (signed by the site key).
func agentRelease(c *cloud.Ctx) (any, error) {
	os, arch, err := osArch(c)
	if err != nil {
		return nil, err
	}
	r, err := PinnedRelease(os, arch, "")
	if err != nil {
		return nil, err
	}
	return releaseResponse(r, os, arch, "/v1/agent/binary"), nil
}

// installRelease: §21 #91 release metadata for the install script (install token; version = ?version= or the pin; uploaded only).
func installRelease(c *cloud.Ctx) (any, error) {
	if _, err := cloud.VerifyInstallToken(c.Headers["authorization"]); err != nil {
		return nil, err
	}
	os, arch, err := osArch(c)
	if err != nil {
		return nil, err
	}
	vq := c.Query.Get("version")
	if vq != "" && !VERSION_RE.MatchString(vq) {
		return nil, cloud.E.Bad("version", "V_FORMAT", "version must be 1–64 of [0-9A-Za-z._+-]")
	}
	r, err := PinnedRelease(os, arch, vq)
	if err != nil {
		return nil, err
	}
	return releaseResponse(r, os, arch, "/v1/install/binary"), nil
}

func agentBinary(c *cloud.Ctx) (any, error) {
	os, arch, err := osArch(c)
	if err != nil {
		return nil, err
	}
	version, ok := c.Query.Lookup("version")
	if !ok || !VERSION_RE.MatchString(version) {
		return nil, cloud.E.Bad("version", "V_FORMAT", "version is required (1–64 of [0-9A-Za-z._+-])")
	}
	bin := core.Paths.AgentBinary(version, os, arch)
	sha, sig := shaSig(bin)
	meta := BuildMeta(version)
	if sha == "" || sig == "" {
		return nil, cloud.E.NotFound("agent " + version + " for " + os + "/" + arch + " is not uploaded")
	}
	return core.ServeObject(bin, core.ServeOpts{
		Range:      c.Headers["range"],
		FileName:   "opm-agent",
		NoRedirect: core.NoRedirectFor(c.Headers, ""),
		Headers:    withSchemaHeaders(map[string]string{"X-OPM-Sha256": firstWord(sha), "X-OPM-Signature": doc.JSTrim(sig), "X-OPM-Agent-Version": version}, meta),
	})
}

// versionShown is s.replace(/[^keep]/g, '?') of a regex without the u flag: one '?' per UTF-16 unit.
func versionShown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && (r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || strings.ContainsRune("._+-", r)) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('?')
		if r1, _ := utf16.EncodeRune(r); r1 != '�' {
			b.WriteByte('?')
		}
	}
	return b.String()
}

func installScriptOp(c *cloud.Ctx) (any, error) {
	t, err := cloud.VerifyInstallToken(c.Headers["authorization"])
	if err != nil {
		// §21 #91: a token that cannot be used → 200 + a script that prints why (curl -f would hide a 410 body)
		var e *cloud.ApiError
		if errors.As(err, &e) && (e.Code == "gone" || (e.Code == "unauthenticated" && strings.Contains(e.Message, "unknown install token"))) {
			reason := "unknown"
			if e.Code == "gone" {
				reason = e.Details.Str("reason")
				if reason == "" {
					reason = "expired"
				}
			}
			return scriptReply(ErrorScript(TokenProblemText(reason, e.Details.Str("siteSlug")))), nil
		}
		return nil, err
	}
	vq := c.Query.Get("version")
	if vq != "" && !VERSION_RE.MatchString(vq) {
		return scriptReply(ErrorScript([]string{"OPM install: ?version=" + versionShown(vq) + " is not a version (use e.g. ?version=0.10.0-10090000)."})), nil
	}
	m := core.LoadDepsManifest()
	depSnap, err := cloud.Col.Deployments().Doc(t.DeploymentID).Get()
	if err != nil {
		return nil, err
	}
	dep := cloud.DecodeDeployment(depSnap.Data())
	want := vq
	if want == "" {
		want = m.Map("agent").Str("version")
	}
	requested := pinnedAgent(want)
	if vq != "" && requested != nil && len(requested.Files) == 0 {
		return scriptReply(ErrorScript([]string{"OPM install: agent " + vq + " is not uploaded to the OPM cloud (only uploaded versions can be installed).", "Rerun the command without ?version= to install the pinned version."})), nil
	}
	// ports the agent listens on now: the config the SITE runs (its heartbeat head: a revision or, in branch mode, its
	// agent-branch commit), else the deployment head, else the legacy head; before the first head the defaults 7080 / 8080
	depID := dep.Str("deploymentId")
	var headCfg *doc.Map
	if agentDoc, err := cloud.Col.Agents(depID).Doc(t.SiteSlug).Get(); err == nil && agentDoc.Exists() {
		if running, err := core.SiteRunningCommit(depID, t.SiteSlug, agentDoc.Data().Str("headRevId")); err == nil {
			headCfg = running.Map("config")
		}
	}
	if headCfg == nil {
		headCfg, _ = core.DeploymentHeadConfig(dep)
	}
	if headCfg == nil {
		if h := dep.Map("head"); h != nil {
			r, _ := core.LoadRevision(depID, h.Str("revId"), nil)
			headCfg = r.Map("config")
		}
	}
	headSite := findBy(headCfg.Arr("sites"), "siteSlug", t.SiteSlug).Map("config")
	draftSite := findBy(dep.Map("draft").Arr("sites"), "siteSlug", t.SiteSlug).Map("config")
	var docker *PinnedDocker
	if m != nil {
		d := m.Map("docker")
		sha := map[string]string{}
		for _, arch := range []string{"amd64", "arm64"} {
			sha[arch] = d.Map("files").Map(arch).Str("sha256")
		}
		docker = &PinnedDocker{d.Str("version"), m.Str("minDockerApi"), sha}
	}
	edgeSite := headSite
	if edgeSite == nil {
		edgeSite = draftSite
	}
	var alias any = doc.Undefined
	if headSite != nil {
		alias = fld(headSite, "agentGuiAliasPort")
	}
	return scriptReply(InstallScript(InstallScriptOpts{
		ApiBase: core.ApiBaseOf(c), DeploymentID: t.DeploymentID, SiteSlug: t.SiteSlug, Token: t.Token, ReleasePublicKey: cloud.Env.ReleasePublicKey(),
		Agent: requested, Docker: docker,
		PortAgent: fldOr(headSite, "agentPort", nil), PortAlias: alias, PortEdge: fldOr(edgeSite.Map("edge"), "httpPort", nil),
		UpgradeOnly: t.Mode == "upgrade", SeenIP: c.IP, RequestedVersion: vq, Kind: t.Kind,
	})), nil
}

func installBinary(c *cloud.Ctx) (any, error) {
	if _, err := cloud.VerifyInstallToken(c.Headers["authorization"]); err != nil {
		return nil, err
	}
	os, arch, err := osArch(c)
	if err != nil {
		return nil, err
	}
	vq, has := c.Query.Lookup("version")
	if has && !VERSION_RE.MatchString(vq) {
		return nil, cloud.E.Bad("version", "V_FORMAT", "bad version")
	}
	version, err := core.ResolveAgentVersion(vq)
	if err != nil {
		return nil, err
	}
	bin := core.Paths.AgentBinary(version, os, arch)
	sha, _, err := core.ReadText(bin + ".sha256")
	if err != nil {
		return nil, err
	}
	sig, _, err := core.ReadText(bin + ".sig")
	if err != nil {
		return nil, err
	}
	meta := BuildMeta(version)
	if sha == "" || sig == "" {
		return nil, cloud.E.NotFound("agent " + version + " for " + os + "/" + arch + " is not uploaded")
	}
	return core.ServeObject(bin, core.ServeOpts{
		Range:    c.Headers["range"],
		FileName: "opm-agent",
		Headers:  withSchemaHeaders(map[string]string{"X-OPM-Sha256": firstWord(sha), "X-OPM-Signature": doc.JSTrim(sig), "X-OPM-Agent-Version": version}, meta),
	})
}

// installDeps: a pinned dependency of the install script (deps/manifest.json): name "docker" = the official static bundle.
func installDeps(c *cloud.Ctx) (any, error) {
	if _, err := cloud.VerifyInstallToken(c.Headers["authorization"]); err != nil {
		return nil, err
	}
	arch := c.Query.Get("arch")
	if arch != "amd64" && arch != "arm64" {
		return nil, cloud.E.Bad("arch", "V_ENUM", "arch must be amd64 | arm64")
	}
	name := c.Params["name"]
	if name != "docker" {
		return nil, cloud.E.NotFound("unknown install dependency " + name + " (known: docker)")
	}
	m := core.LoadDepsManifest()
	f := m.Map("docker").Map("files").Map(arch)
	if m == nil || f == nil {
		return nil, cloud.E.NotFound("no install dependencies are uploaded (deps/manifest.json missing)")
	}
	path := f.Str("path")
	return core.ServeObject(path, core.ServeOpts{
		Range:    c.Headers["range"],
		FileName: path[strings.LastIndexByte(path, '/')+1:],
		Headers:  map[string]string{"X-OPM-Sha256": f.Str("sha256"), "X-OPM-Version": m.Map("docker").Str("version")},
	})
}

func validateEnroll(b *doc.Map) error {
	v := &cloud.V{}
	pk, isStr := b.StrOK("publicKey")
	if !isStr || !stdBase64Of32(pk) {
		v.Err("publicKey", "V_FORMAT", "publicKey must be the raw 32-byte Ed25519 key, base64 (std, padded)")
	}
	for _, k := range []string{"agentVersion", "hostname", "ip", "advertiseHost", "os", "arch"} {
		if s, ok := b.StrOK(k); !ok || doc.JSLen(s) > 255 {
			v.Err(k, "V_TYPE", k+" must be a string")
		}
	}
	if s, ok := b.StrOK("advertiseHost"); ok && s == "" {
		v.Err("advertiseHost", "V_REQUIRED", "advertiseHost is required")
	}
	if f, ok := b.Val("agentPort").(float64); !ok || f != float64(int64(f)) || f < 1 || f > 65535 {
		v.Err("agentPort", "V_RANGE", "agentPort must be 1–65535")
	}
	if da := core.DetectedAddressesError(fld(b, "detectedAddresses")); da != "" {
		v.Err("detectedAddresses", "V_TYPE", da)
	}
	return v.ThrowIf()
}

func stdBase64Of32(s string) bool {
	raw := doc.B64Decode(s)
	return len(raw) == 32 && stdB64(raw) == s
}

func enroll(c *cloud.Ctx) (any, error) {
	token, err := cloud.BearerInstallToken(c.Headers["authorization"])
	if err != nil {
		return nil, err
	}
	tokenHash := cloud.Sha256Hex([]byte(token))
	b, err := c.BodyMap()
	if err != nil {
		return nil, err
	}
	if err := validateEnroll(b); err != nil {
		return nil, err
	}
	q, err := cloud.DB().CollectionGroup("installTokens").Where("tokenHash", "==", tokenHash).Limit(1).Get()
	if err != nil {
		return nil, err
	}
	if q.Empty() {
		return nil, cloud.E.Unauth("unknown install token")
	}
	tRef := q.Docs[0].Ref()
	deploymentID := tRef.Parent().Parent().ID()
	depRef := cloud.Col.Deployments().Doc(deploymentID)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (any, error) {
		ts, err := tx.Get(tRef)
		if err != nil {
			return nil, err
		}
		ds, err := tx.Get(depRef)
		if err != nil {
			return nil, err
		}
		t := ts.Data()
		if err := cloud.CheckTokenState(t); err != nil {
			return nil, err
		}
		if !ds.Exists() {
			return nil, cloud.E.Gone("the deployment was deleted")
		}
		dep := cloud.DecodeDeployment(ds.Data())
		if dep.Truthy("deleted") {
			return nil, cloud.E.Gone("the deployment was deleted")
		}
		site := t.Str("siteSlug")
		aRef := cloud.Col.Agents(deploymentID).Doc(site)
		existing, err := tx.Get(aRef)
		if err != nil {
			return nil, err
		}
		if existing.Exists() {
			return nil, cloud.E.Conflict("site " + site + " is already enrolled (revoke the enrollment first)")
		}
		others, err := tx.Query(cloud.Col.Agents(deploymentID).Query)
		if err != nil {
			return nil, err
		}
		now := doc.NowISO()
		ip := b.Str("ip")
		if ip == "" {
			ip = c.IP
		}
		agent := doc.Obj(
			"siteSlug", site, "publicKey", fld(b, "publicKey"), "enrolledAt", now, "agentVersion", fld(b, "agentVersion"), "lastSeenAt", now, "online", true, "ip", ip,
			"hostname", fld(b, "hostname"), "advertiseHost", fld(b, "advertiseHost"), "agentPort", fld(b, "agentPort"), "os", fld(b, "os"), "arch", fld(b, "arch"), "relayVia", nil,
			"headRevId", nil, "staging", nil, "keyVersion", fld(dep, "accessKeyVersion"), "ackSeq", 0.0, "metrics", nil, "edge", nil, "peersSeen", []any{}, "detectedAddresses", core.DetectedAddressesOf(fld(b, "detectedAddresses")),
		)
		// §21 #92: the plaintext stays stored: the used token is now this enrollment's "Upgrade / reinstall command"
		// (revealUpgradeCommand); it is nulled when the enrollment is revoked or the command is regenerated
		tx.Update(tRef, doc.Obj("usedAt", now))
		tx.Create(aRef, agent.Without("online"))
		log := core.NewCloudLog(deploymentID, dep)
		fp := cloud.KeyFingerprint(b.Str("publicKey"))
		who := b.Str("hostname")
		if who == "" {
			who = b.Str("ip")
		}
		log.Add(tx, core.CloudEntry{Type: "install.enrolled", Summary: "Site " + site + " enrolled (" + who + ")", Data: doc.Obj("siteSlug", site, "agentVersion", fld(b, "agentVersion"), "publicKeyFingerprint", fp, "ip", ip, "hostname", fld(b, "hostname"))})
		tx.Update(depRef, log.DepUpdate().Set("agentEpoch", dep.Num("agentEpoch")+1))
		list := make([]*doc.Map, 0, len(others.Docs)+1)
		for _, d := range others.Docs {
			list = append(list, d.Data())
		}
		peers := sortedPeers(append(list, agent))
		var console any
		if u := cloud.Env.ConsoleURL(); u != "" {
			console = u
		}
		return doc.Obj("deploymentId", deploymentID, "siteSlug", site, "peers", peers, "accessKey", doc.Obj("hash", fld(dep, "accessKeyHash"), "version", fld(dep, "accessKeyVersion")), "serverTime", now, "consoleUrl", console, "deploymentLabel", fldOr(dep, "label", nil)), nil
	})
}

func stdB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func sortedPeers(l []*doc.Map) []any {
	sort.SliceStable(l, func(i, j int) bool { return l[i].Str("siteSlug") < l[j].Str("siteSlug") })
	out := make([]any, len(l))
	for i, a := range l {
		out[i] = core.PeerInfo(a)
	}
	return out
}
