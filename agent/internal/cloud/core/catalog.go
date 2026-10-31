package core

import (
	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// ServiceView is serviceView: service document → API view (docID "" = undefined).
func ServiceView(d *doc.Map, docID string) *doc.Map {
	p := cloud.Pub(d)
	rest := p.Without("serviceId", "slug", "repository", "defaultRepository")
	return rest.Shallow().Set("serviceSlug", or(rest, "serviceSlug", or(p, "slug", docID))).Set("icon", or(rest, "icon", nil))
}

// ImageView is imageView: image document → API view (docID, parentSlug "" = undefined).
func ImageView(d *doc.Map, docID, parentSlug string) (*doc.Map, error) {
	p := cloud.Pub(d)
	rest := p.Without("serviceId", "tag", "repository")
	out := rest.Shallow().Set("version", or(rest, "version", or(p, "tag", docID))).Set("serviceSlug", or(rest, "serviceSlug", parentSlug))
	if vols, ok := out.Val("volumes").([]any); ok {
		l := make([]any, len(vols))
		for i, v := range vols {
			l[i] = v
			switch v.(type) {
			case *doc.Map, []any:
				n, err := shared.NormalizeImageVolume(v)
				if err != nil {
					return nil, err
				}
				l[i] = n
			}
		}
		out.Set("volumes", l)
	}
	return out, nil
}

// Catalog is Catalog. Services and Images are objects keyed by slug and "<service>:<version>"; Site (nil = absent) holds
// the site images known to this write.
type Catalog struct {
	Services, Images *doc.Map
	NotFoundPageIds  []string
	Site             *doc.Map
}

// WithSiteImages is withSiteImages: the catalog plus the site images (digest "") of revision catalogs.
func WithSiteImages(cat *Catalog, from ...*doc.Map) *Catalog {
	services := cat.Site.Map("services").Shallow()
	images := cat.Site.Map("images").Shallow()
	for _, rc := range from {
		if rc == nil {
			continue
		}
		for _, k := range rc.Map("images").Keys() {
			im := rc.Map("images").Map(k)
			if im.Truthy("digest") || cat.Images.Has(k) || images.Has(k) {
				continue
			}
			images.Set(k, im)
			slug := im.Str("serviceSlug")
			if s := rc.Map("services").Map(slug); s != nil && !services.Has(slug) {
				services.Set(slug, s)
			} else if !services.Has(slug) {
				services.Set(slug, doc.Obj("serviceSlug", slug, "label", slug, "icon", nil))
			}
		}
	}
	if images.Len() == 0 {
		return cat
	}
	c := *cat
	c.Site = doc.Obj("services", services, "images", images, "notFoundPages", doc.New())
	return &c
}

// LoadCatalog is loadCatalog.
func LoadCatalog() (*Catalog, error) {
	svc, err := cloud.Col.Services().Get()
	if err != nil {
		return nil, err
	}
	imgs, err := cloud.DB().CollectionGroup("images").Get()
	if err != nil {
		return nil, err
	}
	pages, err := cloud.Col.NotFoundPages().Select("label").Get()
	if err != nil {
		return nil, err
	}
	cat := &Catalog{Services: doc.New(), Images: doc.New(), NotFoundPageIds: []string{}}
	for _, d := range svc.Docs {
		s := ServiceView(d.Data(), d.ID())
		cat.Services.Set(s.Str("serviceSlug"), s)
	}
	for _, d := range imgs.Docs {
		i, err := ImageView(d.Data(), d.ID(), d.Ref().Parent().Parent().ID())
		if err != nil {
			return nil, err
		}
		cat.Images.Set(shared.ImageKey(i.Str("serviceSlug"), i.Str("version")), i)
	}
	for _, d := range pages.Docs {
		cat.NotFoundPageIds = append(cat.NotFoundPageIds, d.ID())
	}
	SetImageVolumes(ImageVolumes(cat))
	return cat, nil
}

// PrimeImageVolumes is primeImageVolumes: refresh the image-volume cache of normalize-on-read when older than its TTL.
func PrimeImageVolumes() {
	if ImageVolumesFresh() {
		return
	}
	q, err := cloud.DB().CollectionGroup("images").Select("volumes").Get()
	if err != nil {
		return
	}
	out := doc.New()
	for _, d := range q.Docs {
		parent := d.Ref().Parent().Parent()
		if parent == nil || parent.Parent().ID() != "services" {
			continue
		}
		v, ok := d.Data().Val("volumes").([]any)
		if !ok {
			v = []any{}
		}
		out.Set(shared.ImageKey(parent.ID(), d.ID()), doc.Obj("volumes", v))
	}
	SetImageVolumes(out)
}

// CatalogImage is catalogImage.
func CatalogImage(i *doc.Map) *doc.Map {
	return doc.Obj("kind", present(i, "kind"), "ports", present(i, "ports"), "env", present(i, "env"), "volumes", present(i, "volumes"), "healthcheck", present(i, "healthcheck"), "importStatus", present(i.Map("import"), "status"))
}

// ImageVolumes is imageVolumes.
func ImageVolumes(cat *Catalog) *doc.Map {
	out := doc.New()
	for _, k := range cat.Images.Keys() {
		v, ok := cat.Images.Map(k).Val("volumes").([]any)
		if !ok {
			v = []any{}
		}
		out.Set(k, doc.Obj("volumes", v))
	}
	si := cat.Site.Map("images")
	for _, k := range si.Keys() {
		if !out.Has(k) {
			out.Set(k, doc.Obj("volumes", or(si.Map(k), "volumes", []any{})))
		}
	}
	return out
}

// LoadDetectedHosts is loadDetectedHosts: siteSlug → advertiseHost of the enrolled sites.
func LoadDetectedHosts(deploymentID string) (*doc.Map, error) {
	q, err := cloud.Col.Agents(deploymentID).Select("advertiseHost").Get()
	if err != nil {
		return nil, err
	}
	out := doc.New()
	for _, d := range q.Docs {
		if h := d.Data().Str("advertiseHost"); h != "" {
			out.Set(d.ID(), h)
		}
	}
	return out, nil
}

// ValidationCtx is validationCtx. secretsSet nil = undefined (use []string{} for none); detectedHosts nil = absent.
func ValidationCtx(mode, deploymentID string, cat *Catalog, secretsSet []string, detectedHosts *doc.Map) *doc.Map {
	services, images := doc.New(), doc.New()
	for _, k := range cat.Services.Keys() {
		services.Set(k, doc.Obj("label", present(cat.Services.Map(k), "label")))
	}
	for _, k := range cat.Images.Keys() {
		images.Set(k, CatalogImage(cat.Images.Map(k)))
	}
	ss, si := cat.Site.Map("services"), cat.Site.Map("images")
	for _, k := range ss.Keys() {
		if !services.Has(k) {
			services.Set(k, doc.Obj("label", present(ss.Map(k), "label")))
		}
	}
	for _, k := range si.Keys() {
		if !images.Has(k) {
			i := si.Map(k)
			images.Set(k, doc.Obj("kind", present(i, "kind"), "ports", present(i, "ports"), "env", present(i, "env"), "volumes", present(i, "volumes"), "healthcheck", present(i, "healthcheck"), "importStatus", "ready"))
		}
	}
	out := doc.Obj("mode", mode, "deploymentId", deploymentID, "services", services, "images", images, "notFoundPageIds", strs(cat.NotFoundPageIds))
	if secretsSet != nil {
		out.Set("secretsSet", strs(secretsSet))
	}
	if detectedHosts != nil {
		out.Set("detectedHosts", detectedHosts)
	}
	return out
}

// ImageSpec is imageSpec.
func ImageSpec(i *doc.Map) (*doc.Map, error) {
	imp := i.Map("import")
	if imp.Val("status") != "ready" || !i.Truthy("digest") || !imp.Truthy("configDigest") {
		id := JSString(present(i, "serviceSlug")) + ":" + JSString(present(i, "version"))
		return nil, cloud.E.Bad("images["+id+"]", "V_IMAGE_NOT_READY", "image "+id+" is not imported yet")
	}
	size := or(imp, "sizeBytes", doc.Undefined)
	if size == doc.Undefined {
		t := 0.0
		for _, l := range imp.Arr("layers") {
			t += l.(*doc.Map).Num("sizeBytes")
		}
		size = t
	}
	return doc.Obj("serviceSlug", present(i, "serviceSlug"), "version", present(i, "version"), "digest", present(i, "digest"), "configDigest", present(imp, "configDigest"), "platform", present(i, "platform"), "manifestMediaType", or(imp, "manifestMediaType", "application/vnd.oci.image.manifest.v1+json"), "layers", present(imp, "layers"), "sizeBytes", size, "kind", present(i, "kind"), "entrypoint", present(i, "entrypoint"), "command", present(i, "command"), "workdir", present(i, "workdir"), "user", present(i, "user"), "ports", present(i, "ports"), "env", present(i, "env"), "volumes", present(i, "volumes"), "healthcheck", present(i, "healthcheck"), "stopTimeoutSec", present(i, "stopTimeoutSec")), nil
}

// BuildRevisionCatalog is buildRevisionCatalog: services + ImageSpec of every container version + the referenced not-found
// pages. Uploaded service icons are embedded as data: URLs.
func BuildRevisionCatalog(config *doc.Map, cat *Catalog) (*doc.Map, error) {
	services, images, pages := doc.New(), doc.New(), doc.New()
	var pageIDs []string
	seenPage := map[string]bool{}
	for _, sv := range config.Arr("sites") {
		site := sv.(*doc.Map)
		sc := site.Map("config")
		if id := sc.Str("notFoundPageId"); id != "" && !seenPage[id] {
			seenPage[id] = true
			pageIDs = append(pageIDs, id)
		}
		for _, cv := range sc.Arr("containers") {
			c := cv.(*doc.Map)
			slug, version := c.Str("serviceSlug"), c.Str("version")
			key := shared.ImageKey(slug, version)
			s := cat.Services.Map(slug)
			i := cat.Images.Map(key)
			var si *doc.Map
			if i == nil {
				si = cat.Site.Map("images").Map(key)
			}
			if si != nil {
				ssl := si.Str("serviceSlug")
				ss := cat.Site.Map("services").Map(ssl)
				if ss == nil {
					ss = doc.Obj("serviceSlug", ssl, "label", ssl, "icon", nil)
				}
				if !services.Has(ssl) {
					services.Set(ssl, doc.Obj("serviceSlug", present(ss, "serviceSlug"), "label", present(ss, "label"), "icon", or(ss, "icon", nil)))
				}
				images.Set(key, si)
				continue
			}
			if s == nil || i == nil {
				cs := JSString(or(c, "containerSlug", present(c, "serviceSlug")))
				return nil, cloud.E.Bad("sites["+JSString(present(site, "siteSlug"))+"].config.containers["+cs+"]", "V_REF", "image "+key+" (container "+cs+") does not exist")
			}
			services.Set(s.Str("serviceSlug"), doc.Obj("serviceSlug", present(s, "serviceSlug"), "label", present(s, "label"), "icon", or(s, "icon", nil)))
			spec, err := ImageSpec(i)
			if err != nil {
				return nil, err
			}
			images.Set(key, spec)
		}
	}
	icons := []any{}
	for _, k := range services.Keys() {
		icons = append(icons, services.Map(k).Val("icon"))
	}
	embedded, err := EmbedIcons(icons)
	if err != nil {
		return nil, err
	}
	for _, k := range services.Keys() {
		x := services.Map(k)
		if ic := x.Str("icon"); ic != "" {
			if v, ok := embedded[ic]; ok {
				x.Set("icon", v)
			}
		}
	}
	if len(pageIDs) > 0 {
		refs := make([]*driver.DocRef, len(pageIDs))
		for i, id := range pageIDs {
			refs[i] = cloud.Col.NotFoundPages().Doc(id)
		}
		snaps, err := cloud.DB().GetAll(refs...)
		if err != nil {
			return nil, err
		}
		for _, sn := range snaps {
			if !sn.Exists() {
				return nil, cloud.E.Bad("sites", "V_REF", "not-found page "+sn.ID()+" does not exist")
			}
			p := sn.Data()
			pages.Set(sn.ID(), doc.Obj("notFoundPageId", sn.ID(), "label", present(p, "label"), "htmlCode", present(p, "htmlCode")))
		}
	}
	return doc.Obj("services", services, "images", images, "notFoundPages", pages), nil
}

func configUsage(kind, id string, label any, cfg *doc.Map, source string, out *[]*doc.Map) {
	for _, sv := range cfg.Arr("sites") {
		site := sv.(*doc.Map)
		sc := site.Map("config")
		slug := present(site, "siteSlug")
		if sc.Truthy("notFoundPageId") {
			*out = append(*out, doc.Obj("kind", kind, "id", id, "label", label, "siteSlug", slug, "containerSlug", "", "serviceSlug", "", "version", "", "notFoundPageId", sc.Val("notFoundPageId"), "source", source))
		}
		for _, cv := range sc.Arr("containers") {
			c := cv.(*doc.Map)
			*out = append(*out, doc.Obj("kind", kind, "id", id, "label", label, "siteSlug", slug, "containerSlug", or(c, "containerSlug", present(c, "serviceSlug")), "serviceSlug", present(c, "serviceSlug"), "version", present(c, "version"), "notFoundPageId", nil, "source", source))
		}
	}
}

// ScanUsage is scanUsage: everything that references catalog items; entries are UsageEntry maps.
func ScanUsage(proposals bool) (entries, deployments []*doc.Map, err error) {
	tpls, err := cloud.Col.Templates().Get()
	if err != nil {
		return nil, nil, err
	}
	deps, err := cloud.Col.Deployments().Where("deleted", "==", false).Get()
	if err != nil {
		return nil, nil, err
	}
	out := []*doc.Map{}
	for _, d := range tpls.Docs {
		t := cloud.DecodeTemplate(d.Data())
		configUsage("template", t.Str("templateId"), present(t, "label"), t, "template", &out)
	}
	deployments = []*doc.Map{}
	var headRefs []*driver.DocRef
	for _, s := range deps.Docs {
		d := cloud.DecodeDeployment(s.Data())
		deployments = append(deployments, d)
		if d.Truthy("head") {
			headRefs = append(headRefs, cloud.Col.Revisions(d.Str("deploymentId")).Doc(d.Map("head").Str("revId")))
		}
	}
	heads := map[string]*doc.Map{}
	if len(headRefs) > 0 {
		snaps, err := cloud.DB().GetAll(headRefs...)
		if err != nil {
			return nil, nil, err
		}
		for _, h := range snaps {
			if h.Exists() {
				heads[h.Ref().Parent().Parent().ID()] = cloud.DecodeRevision(h.Data()).Map("config")
			}
		}
	}
	for _, d := range deployments {
		id := d.Str("deploymentId")
		configUsage("deployment", id, present(d, "label"), d.Map("draft"), "draft", &out)
		if h := heads[id]; h != nil {
			configUsage("deployment", id, present(d, "label"), h, "head", &out)
		}
	}
	if proposals {
		open, err := cloud.DB().CollectionGroup("proposals").Where("status", "in", OPEN_STATUSES).Get()
		if err != nil {
			return nil, nil, err
		}
		refs := make([]*driver.DocRef, 0, len(open.Docs))
		for _, p := range open.Docs {
			refs = append(refs, p.Ref().Parent().Parent().Collection("revisions").Doc(p.ID()))
		}
		if len(refs) > 0 {
			revs, err := cloud.DB().GetAll(refs...)
			if err != nil {
				return nil, nil, err
			}
			for _, r := range revs {
				if !r.Exists() {
					continue
				}
				depID := r.Ref().Parent().Parent().ID()
				for _, d := range deployments {
					if d.Str("deploymentId") == depID {
						configUsage("deployment", depID, present(d, "label"), cloud.DecodeRevision(r.Data()).Map("config"), "proposal", &out)
						break
					}
				}
			}
		}
	}
	return out, deployments, nil
}

// DedupeUsage is dedupeUsage.
func DedupeUsage(list []*doc.Map) []any {
	seen := map[string]bool{}
	out := []any{}
	for _, u := range list {
		k := ""
		for _, f := range []string{"kind", "id", "siteSlug", "containerSlug", "serviceSlug", "version"} {
			k += JSString(present(u, f)) + "|"
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		o := doc.New()
		for _, f := range []string{"kind", "id", "label", "siteSlug", "containerSlug", "serviceSlug", "version"} {
			o.Set(f, present(u, f))
		}
		out = append(out, o)
	}
	return out
}

// OPEN_STATUSES is OPEN_STATUSES.
var OPEN_STATUSES = []string{"published", "staging", "ready", "approved", "applying"}

// IsTerminal is isTerminal.
func IsTerminal(s string) bool {
	for _, x := range shared.PROPOSAL_TERMINAL {
		if x == s {
			return true
		}
	}
	return false
}
