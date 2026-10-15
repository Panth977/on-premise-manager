// Package driver turns a site config into Docker containers (SPEC-final §15): names, the hashed container spec,
// image loading from content-addressed blobs, the recreate rule, readiness ("completed") and health checks.
package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"opm/agent/internal/canonical"
	"opm/agent/internal/docker"
	"opm/agent/internal/expr"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// Label keys.
const (
	LDeployment = "opm.deployment"
	LSite       = "opm.site"
	LContainer  = "opm.container" // containerSlug (the identity of a managed container)
	LService    = "opm.service"   // serviceSlug
	LRev        = "opm.rev"
	LSpecHash   = "opm.spec_hash"
	LRevSeq     = "opm.rev_seq"
)

// NetworkName is opm_<deploymentId>_<siteSlug>.
func NetworkName(dep, site string) string { return "opm_" + dep + "_" + site }

// Docker container naming modes (agent.json containerNames, SPEC-final §15.1, §21 #88).
const (
	NamesPlain    = "plain"    // default: the docker name = the containerSlug (what the site config declares)
	NamesPrefixed = "prefixed" // opm_<deploymentId>_<siteSlug>_<containerSlug>: several sites on one Docker host
)

// ValidNames reports whether mode is a valid agent.json containerNames value ("" = plain).
func ValidNames(mode string) bool { return mode == "" || mode == NamesPlain || mode == NamesPrefixed }

// PrefixedName is opm_<deploymentId>_<siteSlug>_<containerSlug> (the docker name in prefixed mode and before §21 #88;
// always kept as a network alias for backward compatibility).
func PrefixedName(dep, site, slug string) string { return "opm_" + dep + "_" + site + "_" + slug }

// ContainerName is the docker name of a managed container: the containerSlug (mode plain / ""), or PrefixedName (mode
// prefixed). The name is never part of the spec hash and never used to FIND a managed container (labels are).
func ContainerName(mode, dep, site, slug string) string {
	if mode == NamesPrefixed {
		return PrefixedName(dep, site, slug)
	}
	return slug
}

// ImageRef is the internal reference of an image by its config digest ("opm/cfg:<hex>"). It is the integrity anchor:
// it is verified against the config's rootfs diff_ids after every docker load.
func ImageRef(configDigest string) string {
	return "opm/cfg:" + strings.TrimPrefix(configDigest, "sha256:")
}

// SanitizeRepo maps a name to a docker repository: lower case, [a-z0-9._/-] (others → "-"), no leading / trailing
// separators; empty → "opm-image". A valid serviceSlug is returned unchanged.
func SanitizeRepo(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' || c == '/') {
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b), "._-/")
	if out == "" {
		return "opm-image"
	}
	return out
}

// SanitizeTag maps a name to a docker tag: [A-Za-z0-9_.-] (others → "-"), first character not "." or "-" (→ "_"),
// at most 128 characters; empty → "latest". A valid image version is returned unchanged.
func SanitizeTag(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			b[i] = '-'
		}
	}
	if len(b) > 128 {
		b = b[:128]
	}
	if len(b) == 0 {
		return "latest"
	}
	if b[0] == '.' || b[0] == '-' {
		b[0] = '_'
	}
	return string(b)
}

// ReadableRef is the readable docker reference of an image on a site: "<serviceSlug>:<version>" (e.g.
// northwind-backend:v1), sanitized to the docker reference rules. It is NOT part of the spec hash.
func ReadableRef(img model.ImageSpec) (ref, repo, tag string) {
	repo, tag = SanitizeRepo(img.ServiceSlug), SanitizeTag(img.Version)
	return repo + ":" + tag, repo, tag
}

// SpecMount is a hashed mount. Sha256: file mounts = the content sha256 (rendered files: of the RENDERED bytes).
type SpecMount struct {
	Type     string  `json:"type"`
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	ReadOnly bool    `json:"readOnly"`
	Sha256   *string `json:"sha256"`
	// Origin is the logical source of the mount (NOT hashed): what the compose export (§21 #106) maps it to.
	Origin MountOrigin `json:"-"`
}

// MountOrigin is the logical source of a SpecMount: Kind volume (Slug = volumeSlug, FileVolume = single-file kind) |
// host (Slug = the host path) | file (Slug = fileSlug, Rendered = render expressions) | secret (Slug = secretSlug).
type MountOrigin struct {
	Kind       string
	Slug       string
	FileVolume bool
	Rendered   bool
}

// SpecPort is a hashed port publication (HostPort 0 = loopback ephemeral).
type SpecPort struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
	HostIP        string `json:"hostIp"`
	HostPort      int    `json:"hostPort"`
}

// Spec is the hashed container spec (§15.2).
type Spec struct {
	Image          string             `json:"image"`
	Entrypoint     []string           `json:"entrypoint"`
	Cmd            []string           `json:"cmd"`
	Workdir        *string            `json:"workdir"`
	User           *string            `json:"user"`
	Env            []string           `json:"env"`
	Mounts         []SpecMount        `json:"mounts"`
	Ports          []SpecPort         `json:"ports"`
	Healthcheck    *model.HealthCheck `json:"healthcheck"`
	Restart        string             `json:"restart"`
	Resources      map[string]int64   `json:"resources"`
	StopTimeoutSec int                `json:"stopTimeoutSec"`
	ExtraHosts     []string           `json:"extraHosts"`
	Network        string             `json:"network"`
	Alias          string             `json:"alias"`
	Labels         map[string]string  `json:"labels"`
}

// PortRef maps an image port name to its container port + protocol.
type PortRef struct {
	Name          string
	ContainerPort int
	Protocol      string
	HostPort      *int
	HostIP        string // bind address of the published port
	Edge          bool   // the edge may proxy to this port
}

// Built is a container ready to be created.
type Built struct {
	ContainerSlug string // the identity on the site (docker name, alias, health / status key)
	Service       string // serviceSlug
	Slug          string // = ContainerSlug (display)
	Name          string // docker name (ContainerName of the naming mode; not hashed)
	Kind          string // service | job
	Enabled       bool
	Spec          Spec
	Hash          string
	Image         model.ImageSpec
	Health        *model.HealthCheck
	Ports         []PortRef
	// SecretFiles: path (<agentHome>/secrets/<secretSlug>, ONE copy per site) → value (written 0400, atomically).
	SecretFiles map[string][]byte
	// SiteFiles: path (the file's location, ONE copy per site) → its content (rendered once per site, or the blob copied
	// byte for byte). The content may hold secrets: it is never logged.
	SiteFiles map[string]SiteFile
	DataDirs  []string // volume directories (their locations); a single-file volume's file is listed too
	// FileVolumes: locations in DataDirs that are single-file volumes (round E kind "file"): PrepareHost creates the
	// FILE (empty) before the bind mount, else Docker would create a directory.
	FileVolumes map[string]bool
	Secrets     []string // secret-tainted values (for log redaction)
	Depends     []string
	FileBlobs   []string // hex of mounted file contents
	Readable    string   // readable docker reference <serviceSlug>:<version> (containers are created from it; not hashed)
}

// BuildParams are the inputs of Build.
type BuildParams struct {
	DataDir      string
	DeploymentID string
	SiteSlug     string
	RevID        string
	RevSeq       int
	Config       *model.Config
	Catalog      *model.RevisionCatalog
	Container    *model.Container
	SiteMap      map[string]expr.SiteRef // optional cache (must be built with the site hosts, see Hosts)
	// Hosts: the site addresses (configured host, else advertised / detected; nil = unknown → E_NULL at use).
	// nil map = validate.ResolveSiteHosts without detected addresses.
	Hosts map[string]validate.SiteHost
	// SkipHostCheck: do not stat host-path binds (unit tests).
	SkipHostCheck bool
	// DeferRender: do not render files with render 'expressions' (staging before the file blobs are on the site;
	// only FileBlobs is used and the hash is meaningless).
	DeferRender bool
	// Names: the docker naming mode (agent.json containerNames: "" / plain = containerSlug, prefixed). Not hashed.
	Names string
	// ReadBlob (optional): the bytes of a content blob by hex sha256 (default: <DataDir>/blobs/files/<hex>). The compose
	// export (§21 #106) serves them from memory.
	ReadBlob func(hex string) ([]byte, error)
}

func services(cat *model.RevisionCatalog) map[string]string {
	m := map[string]string{}
	for slug := range cat.Services {
		m[slug] = slug
	}
	return m
}

// SiteMapFor builds the site.<slug> expression map of a config with the given site hosts (nil = no detected address).
func SiteMapFor(cfg *model.Config, cat *model.RevisionCatalog, dep string, hosts map[string]validate.SiteHost) map[string]expr.SiteRef {
	if hosts == nil {
		hosts = validate.ResolveSiteHosts(cfg, dep, validate.HostOptions{})
	}
	return validate.SiteExprMap(cfg, services(cat), CatalogImages(cat), dep, hosts)
}

// CatalogImages maps a revision catalog to the validation image view.
func CatalogImages(cat *model.RevisionCatalog) map[string]validate.CatalogImage {
	out := map[string]validate.CatalogImage{}
	for k, im := range cat.Images {
		out[k] = validate.CatalogImage{Kind: im.Kind, Ports: im.Ports, Env: im.Env, Volumes: im.Volumes, Healthcheck: im.Healthcheck}
	}
	return out
}

// SiteFile is the one copy of a file on a site.
type SiteFile struct {
	FileSlug string
	Bytes    []byte      // rendered content (render expressions); nil when Src is set
	Src      string      // render none: the content blob, copied byte for byte
	Sha      string      // hex sha256 of the content
	Mode     os.FileMode // 0400 when the rendered content holds a secret in clear, else 0444 (RenderedFileMode, §21 #103)
}

// VolumeDir is the AUTO directory of a site volume: <agentHome>/volumes/<volumeSlug>/ (round D part 2).
func VolumeDir(dataDir, volumeSlug string) string {
	return filepath.Join(dataDir, "volumes", volumeSlug)
}

// LegacyVolumeDir is the round-C / part-1 directory <agentHome>/vol/<volumeSlug>/ (moved to the location on apply).
func LegacyVolumeDir(dataDir, volumeSlug string) string {
	return filepath.Join(dataDir, "vol", volumeSlug)
}

// VolumePath is the directory of a volume of a site config at its location (undeclared: auto).
func VolumePath(dataDir string, sc *model.SiteConfig, volumeSlug string) string {
	var loc *model.StorageLocation
	if v := sc.FindVolume(volumeSlug); v != nil {
		loc = v.Location
	}
	return filepath.Clean(validate.LocationPath(loc, "volumes", volumeSlug, dataDir))
}

// FilePath is the location of the one copy of a file on a site (auto = <agentHome>/files/<fileSlug>).
func FilePath(dataDir string, f *model.FileDef) string {
	return filepath.Clean(validate.LocationPath(f.Location, "files", f.FileSlug, dataDir))
}

// SecretPath is the one copy of a secret mounted as a file: <agentHome>/secrets/<secretSlug>.
func SecretPath(dataDir, secretSlug string) string {
	return filepath.Join(dataDir, "secrets", secretSlug)
}

// FileBlobPath is the content-addressed blob of a file: <agentHome>/blobs/files/<hex>.
func FileBlobPath(dataDir, hex string) string { return filepath.Join(dataDir, "blobs", "files", hex) }

// RenderedFileMode is the host mode of a rendered File: 0400 when a secret is in the bytes in clear, else 0444 — also
// when every secret only went through mosquitto_passwd (§21 #103: a salted hash; Mosquitto reads password_file after
// dropping root, so a root-owned 0400 file would be unreadable to the broker).
func RenderedFileMode(out validate.RenderedFile) os.FileMode {
	if out.Secret && !out.SecretHashed {
		return 0o400
	}
	return 0o444
}

// RenderSiteFileFor renders a render-'expressions' file ONCE for a site: V_FILE_RENDER "<fileSlug>:<line>:<col>: …
// (site <s>)" and, for JSON mimes, V_FILE_JSON. Error messages never contain the rendered bytes.
func RenderSiteFileFor(f *model.FileDef, src string, ectx *expr.Context, siteSlug string) (validate.RenderedFile, error) {
	out := validate.RenderSiteFile(src, ectx)
	where := "site " + siteSlug
	if out.Err != nil {
		return out, fmt.Errorf("V_FILE_RENDER %s:%d:%d: %s %s (%s)", f.FileSlug, out.Err.Line, out.Err.Col, out.Err.Code, out.Err.Message, where)
	}
	if validate.IsJSONMime(f.Mime) {
		if je := validate.JSONError(out.Value); je != "" {
			return out, fmt.Errorf("V_FILE_JSON %s: the rendered file is not valid JSON for %s: %s", f.FileSlug, where, je)
		}
	}
	return out, nil
}

// Build resolves every expression and builds the spec + docker create config.
func Build(p BuildParams) (*Built, error) {
	c := p.Container
	// a config written before round D (no site volumes / legacy data sources) is built from its normalized form
	if norm := validate.NormalizeConfigVolumes(p.Config, p.Catalog.Images); norm != p.Config {
		p.Config = norm
		if site := siteOf(norm, p.SiteSlug); site != nil {
			for i := range site.Config.Containers {
				if site.Config.Containers[i].ContainerSlug == c.ContainerSlug {
					c = &site.Config.Containers[i]
				}
			}
		}
		p.Container = c
		p.SiteMap = nil
	}
	svc := services(p.Catalog)
	if svc[c.ServiceSlug] == "" {
		return nil, fmt.Errorf("service %s is not in the revision catalog", c.ServiceSlug)
	}
	slug := c.ContainerSlug
	if slug == "" {
		return nil, fmt.Errorf("a container of service %s has no containerSlug (publish again from the cloud)", c.ServiceSlug)
	}
	img, ok := p.Catalog.Images[validate.ImageKey(c.ServiceSlug, c.Version)]
	if !ok {
		return nil, fmt.Errorf("image %s:%s is not in the revision catalog", c.ServiceSlug, c.Version)
	}
	images := CatalogImages(p.Catalog)
	if p.SiteMap == nil {
		p.SiteMap = SiteMapFor(p.Config, p.Catalog, p.DeploymentID, p.Hosts)
	}
	ectx := validate.ContainerExprContext(p.Config, svc, images, p.DeploymentID, nil, p.SiteSlug, c, p.SiteMap)
	vars := validate.VarValues(p.Config)
	health := img.Healthcheck
	if c.Healthcheck != nil {
		health = c.Healthcheck
	}
	b := &Built{ContainerSlug: slug, Service: c.ServiceSlug, Slug: slug, Name: ContainerName(p.Names, p.DeploymentID, p.SiteSlug, slug), Kind: img.Kind,
		Enabled: c.Enabled, Image: img, Health: health, SecretFiles: map[string][]byte{}, SiteFiles: map[string]SiteFile{}}
	b.Readable, _, _ = ReadableRef(img)
	for _, d := range c.Depends {
		b.Depends = append(b.Depends, d.ContainerSlug)
	}
	secretVal := map[string]*string{}
	for i := range p.Config.Secrets {
		s := p.Config.Secrets[i]
		secretVal[s.SecretSlug] = s.Value
	}
	// env + mount paths: resolved together in dependency order (container.env.<NAME>, file.<slug>.path; E_CYCLE).
	// Image defaults (incl. the options default / legacy list first item) overlaid by the container env.
	cr := validate.ResolveContainerConfig(p.Config, c, img.Env, img.Volumes, ectx)
	env := map[string]string{}
	for _, e := range cr.Env {
		if e.SecretRef == nil && e.Src == nil {
			continue // image env without a default
		}
		r := cr.Resolution.Env[e.Name]
		what := "env"
		if e.Field == expr.FieldImageEnv {
			what = "image env"
		}
		if e.SecretRef != nil && e.SecretRef.Value == nil {
			ref := ""
			for _, x := range c.Env {
				if x.Name == e.Name && x.SecretRef != nil {
					ref = *x.SecretRef
				}
			}
			return nil, fmt.Errorf("%s: env %s: secret %s has no value", slug, e.Name, ref)
		}
		if r.Err != nil {
			return nil, fmt.Errorf("%s: %s %s: %v", slug, what, e.Name, r.Err)
		}
		env[e.Name] = r.Result.Value
		if r.Result.Secret {
			b.Secrets = append(b.Secrets, r.Result.Value)
		}
	}
	mountTarget := func(key string) (string, error) {
		r := cr.Resolution.Mounts[key]
		if r.Err != nil {
			return "", fmt.Errorf("%s: %s mountPath: %v", slug, key, r.Err)
		}
		if !validate.IsSafeMountPath(r.Result.Value) {
			return "", fmt.Errorf("%s: %s mountPath resolves to %q (V_PATH)", slug, key, r.Result.Value)
		}
		return r.Result.Value, nil
	}
	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	sort.Strings(envList)
	// mounts (containerMounts: the image-volume rows — an image volume without a row is not mounted — plus the extra mounts)
	var mounts []SpecMount
	var fctx *expr.Context // the file context of the site (files render once per site)
	for _, m := range validate.ContainerMounts(c, img.Volumes) {
		target, err := mountTarget(m.Key)
		if err != nil {
			return nil, err
		}
		switch m.Source.Kind {
		case "volume", "data":
			name := m.Source.VolumeName()
			if !validate.RE.VolumeSlug.MatchString(name) {
				return nil, fmt.Errorf("%s: volume name %q is not valid (V_FORMAT)", slug, name)
			}
			if site := siteOf(p.Config, p.SiteSlug); site != nil && site.Config.Volumes != nil && site.Config.FindVolume(name) == nil {
				return nil, fmt.Errorf("%s: volume %s is not declared on site %s (V_VOLUME_UNDECLARED)", slug, name, p.SiteSlug)
			}
			src := VolumeDir(p.DataDir, name)
			if site := siteOf(p.Config, p.SiteSlug); site != nil {
				src = VolumePath(p.DataDir, &site.Config, name)
			}
			fileVol := false
			if site := siteOf(p.Config, p.SiteSlug); site != nil {
				if sv := site.Config.FindVolume(name); sv != nil && sv.VolumeKind() == "file" {
					fileVol = true
				}
			}
			mounts = append(mounts, SpecMount{"bind", src, target, m.ReadOnly, nil, MountOrigin{Kind: "volume", Slug: name, FileVolume: fileVol}})
			b.DataDirs = append(b.DataDirs, src)
			if site := siteOf(p.Config, p.SiteSlug); site != nil {
				if sv := site.Config.FindVolume(name); sv != nil && sv.VolumeKind() == "file" {
					if b.FileVolumes == nil {
						b.FileVolumes = map[string]bool{}
					}
					b.FileVolumes[src] = true
				}
			}
		case "host":
			if !validate.IsSafeMountPath(m.Source.Path) || expr.IsExpr(m.Source.Path) {
				return nil, fmt.Errorf("%s: host path %q is not valid (V_VOLUME)", slug, m.Source.Path)
			}
			if !p.SkipHostCheck {
				if _, err := os.Stat(m.Source.Path); err != nil {
					return nil, fmt.Errorf("%s: host path %s does not exist on this machine", slug, m.Source.Path)
				}
			}
			mounts = append(mounts, SpecMount{"bind", m.Source.Path, target, m.ReadOnly, nil, MountOrigin{Kind: "host", Slug: m.Source.Path}})
		case "file":
			var f *model.FileDef
			for i := range p.Config.Files {
				if p.Config.Files[i].FileSlug == m.Source.FileSlug {
					f = &p.Config.Files[i]
				}
			}
			if f == nil || f.Content == nil {
				return nil, fmt.Errorf("%s: file %s has no content", slug, m.Source.FileSlug)
			}
			hex := strings.TrimPrefix(f.Content.Sha256, "sha256:")
			b.FileBlobs = append(b.FileBlobs, hex)
			src := FilePath(p.DataDir, f) // ONE copy per site, bind-mounted read-only by every container
			if f.RenderMode() != "expressions" {
				b.SiteFiles[src] = SiteFile{FileSlug: f.FileSlug, Src: FileBlobPath(p.DataDir, hex), Sha: hex, Mode: 0o444}
				sum := f.Content.Sha256
				mounts = append(mounts, SpecMount{"bind", src, target, true, &sum, MountOrigin{Kind: "file", Slug: f.FileSlug}})
				continue
			}
			if p.DeferRender {
				mounts = append(mounts, SpecMount{"bind", src, target, true, nil, MountOrigin{Kind: "file", Slug: f.FileSlug, Rendered: true}})
				continue
			}
			var raw []byte
			var err error
			if p.ReadBlob != nil {
				raw, err = p.ReadBlob(hex)
			} else {
				raw, err = os.ReadFile(FileBlobPath(p.DataDir, hex))
			}
			if err != nil {
				return nil, fmt.Errorf("%s: file %s: content is not on this site yet", slug, f.FileSlug)
			}
			if fctx == nil {
				fctx = validate.SiteFileExprContext(p.Config, svc, images, p.DeploymentID, nil, p.SiteSlug, p.SiteMap)
			}
			out, err := RenderSiteFileFor(f, string(raw), fctx, p.SiteSlug)
			if err != nil {
				return nil, err
			}
			mode := RenderedFileMode(out)
			if out.Secret {
				for _, v := range secretVal {
					if v != nil && *v != "" && strings.Contains(out.Value, *v) {
						b.Secrets = append(b.Secrets, *v)
					}
				}
			}
			bytes := []byte(out.Value)
			sum := canonical.Sha256Hex(bytes)
			b.SiteFiles[src] = SiteFile{FileSlug: f.FileSlug, Bytes: bytes, Sha: strings.TrimPrefix(sum, "sha256:"), Mode: mode}
			mounts = append(mounts, SpecMount{"bind", src, target, true, &sum, MountOrigin{Kind: "file", Slug: f.FileSlug, Rendered: true}})
		case "secret":
			val := secretVal[m.Source.SecretSlug]
			if val == nil {
				return nil, fmt.Errorf("%s: secret %s has no value", slug, m.Source.SecretSlug)
			}
			src := SecretPath(p.DataDir, m.Source.SecretSlug) // ONE copy per site (0400)
			sum := canonical.Sha256Hex([]byte(*val))          // a new value recreates the containers that mount it
			mounts = append(mounts, SpecMount{"bind", src, target, true, &sum, MountOrigin{Kind: "secret", Slug: m.Source.SecretSlug}})
			b.SecretFiles[src] = []byte(*val)
			b.Secrets = append(b.Secrets, *val)
		}
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Target < mounts[j].Target })
	// ports: loopback ephemeral publication for every image port (edge upstream + health checks) + the published rows
	var ports []SpecPort
	rows := validate.ContainerPorts(c, img.Ports)
	for _, ip := range img.Ports {
		n := validate.ImagePortNumber(ip, vars, p.DeploymentID)
		if n == nil {
			return nil, fmt.Errorf("%s: image port %s does not resolve", slug, ip.Name)
		}
		var row validate.EffectivePort
		for _, r := range rows {
			if r.PortName == ip.Name {
				row = r
			}
		}
		var hp *int
		if !row.HostPort.IsNull() {
			x, err := expr.ResolveNumberField(row.HostPort.Any(), &expr.Context{Deployment: ectx.Deployment, Var: vars}, expr.FieldHostPort)
			if err != nil {
				return nil, fmt.Errorf("%s: port %s hostPort: %v", slug, ip.Name, err)
			}
			hp = x
		}
		hostIP := row.HostIP
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		b.Ports = append(b.Ports, PortRef{Name: ip.Name, ContainerPort: *n, Protocol: ip.Protocol, HostPort: hp, HostIP: hostIP, Edge: row.Edge})
		ports = append(ports, SpecPort{*n, ip.Protocol, "127.0.0.1", 0})
		if hp != nil {
			ports = append(ports, SpecPort{*n, ip.Protocol, hostIP, *hp})
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, c := ports[i], ports[j]
		if a.ContainerPort != c.ContainerPort {
			return a.ContainerPort < c.ContainerPort
		}
		if a.Protocol != c.Protocol {
			return a.Protocol < c.Protocol
		}
		if a.HostIP != c.HostIP {
			return a.HostIP < c.HostIP
		}
		return a.HostPort < c.HostPort
	})
	restart := map[string]string{"always": "always", "on-failure": "on-failure", "never": "no"}[c.Restart]
	if restart == "" {
		restart = "no"
	}
	res := map[string]int64{"nanoCpus": 0, "memoryBytes": 0}
	if c.Resources.Cpus != nil {
		res["nanoCpus"] = int64(*c.Resources.Cpus * 1e9)
	}
	if c.Resources.MemoryMb != nil {
		res["memoryBytes"] = int64(*c.Resources.MemoryMb) * 1024 * 1024
	}
	cmd := c.Command
	if cmd == nil {
		cmd = img.Command
	}
	entry := c.Entrypoint
	if entry == nil {
		entry = img.Entrypoint
	}
	stop := int(img.StopTimeoutSec)
	if stop <= 0 {
		stop = 30
	}
	net := NetworkName(p.DeploymentID, p.SiteSlug)
	labels := map[string]string{LDeployment: p.DeploymentID, LSite: p.SiteSlug, LContainer: slug, LService: c.ServiceSlug}
	b.Spec = Spec{Image: img.ConfigDigest, Entrypoint: entry, Cmd: cmd, Workdir: img.Workdir, User: img.User, Env: envList,
		Mounts: mounts, Ports: ports, Healthcheck: health, Restart: restart, Resources: res, StopTimeoutSec: stop,
		ExtraHosts: []string{"host.docker.internal:host-gateway"}, Network: net, Alias: slug, Labels: labels}
	if b.Spec.Env == nil {
		b.Spec.Env = []string{}
	}
	if b.Spec.Mounts == nil {
		b.Spec.Mounts = []SpecMount{}
	}
	if b.Spec.Ports == nil {
		b.Spec.Ports = []SpecPort{}
	}
	h, err := canonical.HashCanonical(b.Spec)
	if err != nil {
		return nil, err
	}
	b.Hash = strings.TrimPrefix(h, "sha256:")
	return b, nil
}

// Aliases are the DNS names of the container on the site network: the containerSlug (the hashed alias) and, for
// backward compatibility, the prefixed name opm_<dep>_<site>_<containerSlug> (§21 #88). Not hashed: adding the second
// alias never recreated a container.
func (b *Built) Aliases() []string {
	out := []string{b.Spec.Alias}
	if dep, site := b.Spec.Labels[LDeployment], b.Spec.Labels[LSite]; dep != "" && site != "" {
		if p := PrefixedName(dep, site, b.ContainerSlug); p != b.Spec.Alias {
			out = append(out, p)
		}
	}
	return out
}

// CreateConfig is the docker create body for a built container.
func (b *Built) CreateConfig(revID string, revSeq int) *docker.ContainerConfig {
	s := b.Spec
	labels := map[string]string{}
	for k, v := range s.Labels {
		labels[k] = v
	}
	labels[LRev] = revID
	labels[LRevSeq] = strconv.Itoa(revSeq)
	labels[LSpecHash] = b.Hash
	exposed := map[string]struct{}{}
	bind := map[string][]docker.PortBinding{}
	for _, p := range s.Ports {
		k := strconv.Itoa(p.ContainerPort) + "/" + p.Protocol
		exposed[k] = struct{}{}
		hp := ""
		if p.HostPort != 0 {
			hp = strconv.Itoa(p.HostPort)
		}
		bind[k] = append(bind[k], docker.PortBinding{HostIP: p.HostIP, HostPort: hp})
	}
	var mounts []docker.Mount
	for _, m := range s.Mounts {
		mounts = append(mounts, docker.Mount{Type: "bind", Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	stop := s.StopTimeoutSec
	image := b.Readable
	if image == "" {
		image = ImageRef(s.Image)
	}
	cc := &docker.ContainerConfig{Image: image, Entrypoint: s.Entrypoint, Cmd: s.Cmd, Env: s.Env, Labels: labels,
		ExposedPorts: exposed, StopTimeout: &stop,
		HostConfig: docker.HostConfig{Mounts: mounts, PortBindings: bind, RestartPolicy: docker.RestartPolicy{Name: s.Restart},
			NanoCpus: s.Resources["nanoCpus"], Memory: s.Resources["memoryBytes"], ExtraHosts: s.ExtraHosts, NetworkMode: s.Network},
		NetworkingConfig: &docker.NetworkingConfig{EndpointsConfig: map[string]docker.EndpointConfig{s.Network: {Aliases: b.Aliases()}}}}
	if s.Workdir != nil {
		cc.WorkingDir = *s.Workdir
	}
	if s.User != nil {
		cc.User = *s.User
	}
	return cc
}

// PrepareHost creates the volume directories (0755) and places the one copy per site of every secret (0400) and file
// (0444, or 0400 when its rendered content uses secrets) this container mounts. A file is swapped in atomically: the
// copy staged for revID (StageSiteFile) is renamed onto its location, else a temp file next to it is written and
// renamed. A copy whose content is already right is left alone (containers that keep running keep their inode).
func (b *Built) PrepareHost(dataDir, revID string) error {
	for _, d := range b.DataDirs {
		if b.FileVolumes[d] {
			if err := EnsureVolumeFile(d); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	for p, content := range b.SecretFiles {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := placeFile(p, content, "", canonical.Sha256Hex(content), 0o400, ""); err != nil {
			return err
		}
	}
	for p, sf := range b.SiteFiles {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		staged := ""
		if revID != "" {
			staged = StagedPath(p, revID)
		}
		if err := placeFile(p, sf.Bytes, sf.Src, sf.Sha, sf.Mode, staged); err != nil {
			return err
		}
	}
	return nil
}

// StagedPath is the copy of a site file staged for a revision, next to its location.
func StagedPath(path, revID string) string { return path + ".opm-staged-" + revID }

// StageSiteFile writes the copy of a site file for a revision next to its location (staging; APPLY renames it).
func StageSiteFile(path string, sf SiteFile, revID string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(StagedPath(path, revID), sf.Bytes, sf.Src, sf.Mode)
}

func fileSha(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// placeFile puts content (bytes, or the file src) at p with mode; sha = hex of the content. staged (optional) is a
// prepared copy that is renamed onto p when its content matches.
func placeFile(p string, content []byte, src, sha string, mode os.FileMode, staged string) error {
	sha = strings.TrimPrefix(sha, "sha256:")
	if st, err := os.Lstat(p); err == nil && st.IsDir() {
		// e.g. a pre-part-2 per-container secrets/<containerSlug>/ directory with the same name: agent-owned, replaced
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	} else if err == nil && st.Mode().Perm() == mode && fileSha(p) == sha {
		if staged != "" {
			_ = os.Remove(staged)
		}
		return nil
	}
	if staged != "" {
		if st, err := os.Stat(staged); err == nil && st.Mode().IsRegular() && fileSha(staged) == sha {
			_ = os.Chmod(staged, mode)
			return os.Rename(staged, p)
		}
		_ = os.Remove(staged)
	}
	return writeFileAtomic(p, content, src, mode)
}

// writeFileAtomic writes a temp file next to p and renames it onto p.
func writeFileAtomic(p string, content []byte, src string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".opm-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if src != "" {
		in, err := os.Open(src)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("file content %s: %w", filepath.Base(src), err)
		}
		_, err = io.Copy(tmp, in)
		in.Close()
		if err != nil {
			tmp.Close()
			return err
		}
	} else if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, p); err != nil {
		return err
	}
	ok = true
	return nil
}

func siteOf(cfg *model.Config, slug string) *model.Site {
	for i := range cfg.Sites {
		if cfg.Sites[i].SiteSlug == slug {
			return &cfg.Sites[i]
		}
	}
	return nil
}

// EnsureVolumeFile creates the file of a single-file volume (empty, 0644) when it does not exist. A directory at its
// location is an error: the volume changed kind while it holds data.
func EnsureVolumeFile(p string) error {
	st, err := os.Stat(p)
	if err == nil {
		if st.IsDir() {
			return fmt.Errorf("volume location %s is a directory but the volume is a single file (use a new volume name)", p)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
