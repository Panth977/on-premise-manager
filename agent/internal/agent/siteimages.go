package agent

// §21 #113 site images (Portainer-like): a container on a site may run an image that is not in the cloud catalog — a
// docker reference ("nginx:1.27", "registry.example.com/team/app:2") pulled by this host's Docker, or an image already
// on the host. It becomes a catalog entry of the agent branch exactly like a drift-adopted image (§21 #104): service =
// the repository name, version = local-<12 hex of the image id>, digest "" (no blobs in the cloud), configDigest = the
// docker image id, reference = the pullable reference (pinned @sha256 when the registry gave a repo digest) so another
// site / a host that lost it pulls it again. Resolved images are remembered in kv "site-images" so the GUI can use
// them in edits before they are committed; the commit carries them in its catalog.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

const siteImagesKVKey = "site-images"

// siteImageEntry is one remembered site image.
type siteImageEntry struct {
	Image   model.ImageSpec      `json:"image"`
	Service model.CatalogService `json:"service"`
	AddedAt string               `json:"addedAt"`
	AddedBy string               `json:"addedBy"`
}

// localImageSpec builds the catalog entry of a docker image (drift + site images): service = the repository name,
// version local-<12 hex>; label = the repository name. li may be nil (image not inspectable: an empty spec).
func localImageSpec(li *liveImage, id, hint string) (model.ImageSpec, string) {
	repo := hint
	if li != nil && len(li.RepoTags) > 0 {
		repo = li.RepoTags[0]
	}
	repo = strings.Split(repo, "@")[0]
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	svc := sanitizeSlug(repo, map[string]bool{})
	hexs := strings.TrimPrefix(id, "sha256:")
	if len(hexs) > 12 {
		hexs = hexs[:12]
	}
	sp := model.ImageSpec{ServiceSlug: svc, Version: "local-" + hexs, ConfigDigest: id, Kind: "service", Layers: []model.ImageLayer{}, Ports: []model.ImagePort{}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}}
	if li != nil {
		sp.Entrypoint, sp.Command, sp.SizeBytes = li.Config.Entrypoint, li.Config.Cmd, li.Size
		if li.Config.WorkingDir != "" {
			w := li.Config.WorkingDir
			sp.Workdir = &w
		}
		if li.Config.User != "" {
			u := li.Config.User
			sp.User = &u
		}
		var cps []string
		for p := range li.Config.ExposedPorts {
			cps = append(cps, p)
		}
		sort.Strings(cps)
		for _, p := range cps {
			num, proto, _ := strings.Cut(p, "/")
			n, _ := strconv.Atoi(num)
			sp.Ports = append(sp.Ports, model.ImagePort{Name: "p" + num, Port: numOf(n), Protocol: proto})
		}
		var vols []string
		for v := range li.Config.Volumes {
			vols = append(vols, v)
		}
		sort.Strings(vols)
		for i, v := range vols {
			k := "dir"
			sp.Volumes = append(sp.Volumes, model.ImageVolume{Name: fmt.Sprintf("v%d", i+1), MountPath: v, Kind: &k})
		}
	}
	return sp, repo
}

func (a *Agent) siteImages() map[string]siteImageEntry {
	m := map[string]siteImageEntry{}
	_, _ = a.db.GetKV(siteImagesKVKey, &m)
	return m
}

// siteImageCatalog is the remembered site images as a catalog.
func (a *Agent) siteImageCatalog() model.RevisionCatalog {
	out := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	for k, e := range a.siteImages() {
		out.Images[k] = e.Image
		if _, ok := out.Services[e.Service.ServiceSlug]; !ok {
			out.Services[e.Service.ServiceSlug] = e.Service
		}
	}
	return out
}

// knownCatalog is every image this site can use in a local edit: the head's, the recent cloud commits' (another
// catalog version of a service), the drift-adopted ones and the site images (§21 #113). The head wins on a key.
func (a *Agent) knownCatalog(hr *model.Revision) model.RevisionCatalog {
	var out model.RevisionCatalog
	if hr != nil {
		out = mergeCatalogs(&hr.Catalog, &model.RevisionCatalog{})
	} else {
		out = *emptyCatalog()
	}
	si := a.siteImageCatalog()
	out = mergeCatalogs(&out, &si)
	if ds := a.lastDrift(); ds != nil && len(ds.LocalCatalog.Images) > 0 {
		out = mergeCatalogs(&out, &ds.LocalCatalog)
	}
	for _, ref := range a.cloudCommitMetas(model.CloudRecentCommits) {
		if cc, ok := a.cloudCommit(ref.CommitID); ok && cc.Catalog != nil {
			out = mergeCatalogs(&out, cc.Catalog)
		}
	}
	return out
}

// SiteImageResolveRequest is POST /_opm/api/images/resolve.
type SiteImageResolveRequest struct {
	// Reference: a docker reference to use ("nginx:1.27"); pulled when this host does not have it (or Pull is true)
	Reference string `json:"reference"`
	// ImageID: an image already on the host (sha256:…); used as it is
	ImageID string `json:"imageId"`
	// Pull: pull the reference even when the host has it (a moved tag)
	Pull bool `json:"pull"`
	// registry credentials for this pull only (never stored)
	Username string `json:"username"`
	Password string `json:"password"`
}

// SiteImageResolveResponse is the resolved catalog entry.
type SiteImageResolveResponse struct {
	Key     string               `json:"key"` // <serviceSlug>:<version>
	Image   model.ImageSpec      `json:"image"`
	Service model.CatalogService `json:"service"`
	Pulled  bool                 `json:"pulled"`
}

// resolveSiteImage pulls / inspects an image, tags it for the engine and remembers it as a site image.
func (a *Agent) resolveSiteImage(ctx context.Context, req SiteImageResolveRequest, actor string) (*SiteImageResolveResponse, *httpError) {
	ref := strings.TrimSpace(req.Reference)
	id := strings.TrimSpace(req.ImageID)
	if ref == "" && id == "" {
		return nil, invalid("give a docker reference or a host image", []model.ValidationIssue{{Path: "reference", Code: "V_REQUIRED", Message: "a reference (e.g. nginx:1.27) or an image id is required", Severity: "error"}})
	}
	if ref != "" && !validReference(ref) {
		return nil, invalid("not a docker reference", []model.ValidationIssue{{Path: "reference", Code: "V_REFERENCE", Message: "a docker reference is [host/]repository[:tag][@sha256:digest]", Severity: "error"}})
	}
	pulled := false
	target := id
	if target == "" {
		target = ref
		_, err := a.dc.ImageInspect(ctx, ref)
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			return nil, herr(model.ErrUnavailable, "docker: "+err.Error(), nil)
		}
		if err != nil || req.Pull {
			var auth *docker.RegistryAuth
			if req.Username != "" {
				host := "https://index.docker.io/v1/"
				if first, _, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
					host = first
				}
				auth = &docker.RegistryAuth{Username: req.Username, Password: req.Password, ServerAddress: host}
			}
			pctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			perr := a.dc.ImagePull(pctx, ref, "", auth, nil)
			cancel()
			if perr != nil {
				return nil, herr(model.ErrUnavailable, "docker pull "+ref+": "+perr.Error()+" (does this site reach the registry? use the Internet tunnel or docker load the image on the host)", nil)
			}
			pulled = true
		}
	}
	li := a.inspectImage(ctx, target, map[string]*liveImage{})
	if li == nil {
		return nil, herr(model.ErrNotFound, "image "+target+" is not in this host's Docker", nil)
	}
	sp, label := localImageSpec(li, li.ID, ref)
	if ref != "" {
		pin := ref
		repo, _ := docker.SplitReference(ref)
		for _, rd := range li.RepoDigests {
			if r, dig, ok := strings.Cut(rd, "@"); ok && (r == repo || strings.HasSuffix(r, "/"+repo) || strings.HasSuffix(repo, "/"+r)) {
				if strings.Contains(ref, "@") {
					pin = ref
				} else {
					pin = ref + "@" + dig
				}
				break
			}
		}
		sp.Reference = &pin
	} else if len(li.RepoDigests) > 0 {
		r := li.RepoDigests[0]
		if len(li.RepoTags) > 0 {
			if tag := strings.TrimPrefix(li.RepoTags[0], strings.Split(li.RepoTags[0], ":")[0]); tag != "" {
				r = li.RepoTags[0] + "@" + strings.SplitN(li.RepoDigests[0], "@", 2)[1]
			}
		}
		sp.Reference = &r
	}
	// readable + integrity tags so the engine runs it like a loaded image (§15.1.1)
	if err := a.dc.ImageTag(ctx, li.ID, "opm/cfg", strings.TrimPrefix(li.ID, "sha256:")); err != nil {
		return nil, herr(model.ErrUnavailable, "docker tag: "+err.Error(), nil)
	}
	_, rrepo, rtag := driver.ReadableRef(sp)
	_ = a.dc.ImageTag(ctx, li.ID, rrepo, rtag)
	svc := model.CatalogService{ServiceSlug: sp.ServiceSlug, Label: label}
	key := sp.ServiceSlug + ":" + sp.Version
	m := a.siteImages()
	m[key] = siteImageEntry{Image: sp, Service: svc, AddedAt: string(nowTs()), AddedBy: actor}
	_ = a.db.SetKV(siteImagesKVKey, m)
	a.log.Info("images", "site image resolved", "key", key, "pulled", pulled, "actor", actor)
	return &SiteImageResolveResponse{Key: key, Image: sp, Service: svc, Pulled: pulled}, nil
}

// validReference: [host[:port]/]path[:tag][@sha256:<64 hex>] (lowercase repository, tag ≤ 128).
func validReference(ref string) bool {
	if ref == "" || len(ref) > 255 || strings.ContainsAny(ref, " \t\n") {
		return false
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && (!strings.HasPrefix(digest, "sha256:") || len(digest) != 71) {
		return false
	}
	tag := ""
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
		if tag == "" || len(tag) > 128 {
			return false
		}
	}
	return name != "" && !strings.HasPrefix(name, "/") && !strings.HasSuffix(name, "/")
}

// HostImage is an item of GET /_opm/api/images (images of this host's Docker, for the image picker).
type HostImage struct {
	ID       string   `json:"id"`
	RepoTags []string `json:"repoTags"`
	Size     int64    `json:"sizeBytes"`
	Created  int64    `json:"created"`
}

// SiteImagesResponse is GET /_opm/api/images.
type SiteImagesResponse struct {
	// Catalog: every image a local edit can use (knownCatalog: head, recent cloud commits, drift, site images)
	Catalog model.RevisionCatalog `json:"catalog"`
	// Host: the images of this host's Docker (not the agent's own opm/cfg integrity tags)
	Host []HostImage `json:"host"`
}

func (a *Agent) handleSiteImages(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	out := SiteImagesResponse{Catalog: a.knownCatalog(a.headRevision()), Host: []HostImage{}}
	raw, err := a.dc.GetRaw(r.Context(), "/images/json", nil)
	if err == nil {
		var rows []struct {
			ID       string   `json:"Id"`
			RepoTags []string `json:"RepoTags"`
			Size     int64    `json:"Size"`
			Created  int64    `json:"Created"`
		}
		if json.Unmarshal(raw, &rows) == nil {
			for _, x := range rows {
				tags := []string{}
				for _, t := range x.RepoTags {
					if !strings.HasPrefix(t, "opm/cfg:") && t != "<none>:<none>" {
						tags = append(tags, t)
					}
				}
				if len(tags) == 0 {
					continue
				}
				out.Host = append(out.Host, HostImage{ID: x.ID, RepoTags: tags, Size: x.Size, Created: x.Created})
			}
		}
	}
	sort.Slice(out.Host, func(i, j int) bool { return out.Host[i].RepoTags[0] < out.Host[j].RepoTags[0] })
	return out, 200, nil
}

func (a *Agent) handleResolveSiteImage(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req SiteImageResolveRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	rc := getRC(r)
	res, he := a.resolveSiteImage(r.Context(), req, rc.actor)
	if he != nil {
		return nil, 0, he
	}
	return res, 200, nil
}
