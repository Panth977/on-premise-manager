package agent

// §21 #104 drift detection (docs-v2/branches-commits.md §6): anything changed on the site NOT via the agent —
// containers created / changed / removed by hand, image, env, ports, mounts, restart, command, resources, per-site file
// and secret copies, persist-to-cloud volume content — read from Docker + the filesystem and expressed as config
// changes on top of the branch head (liveConfig). What cannot be expressed as config becomes a drift note.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/branch"
	"opm/agent/internal/canonical"
	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
)

// driftScan is the stored scan (the DriftReport + what the agent needs to commit / discard it).
type driftScan struct {
	model.DriftReport
	LiveConfig   model.Config               `json:"liveConfig"`
	LocalCatalog model.RevisionCatalog      `json:"localCatalog"`
	Adopt        map[string][]string        `json:"adopt"`     // item key → docker ids of hand-made containers that become managed
	Remove       map[string][]string        `json:"remove"`    // item key / note fingerprint → docker ids removed by a discard
	TakeBytes    map[string]*driftTakeBytes `json:"takeBytes"` // note fingerprint → the edited bytes (stored blob)
}

type driftTakeBytes struct {
	FileSlug string `json:"fileSlug"`
	Sha      string `json:"sha"`
	Size     int64  `json:"size"`
}

func (a *Agent) lastDrift() *driftScan {
	var raw string
	if err := a.db.QueryRow(`SELECT report FROM drift WHERE id = 1`).Scan(&raw); err != nil {
		return nil
	}
	var d driftScan
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil
	}
	return &d
}

func (a *Agent) saveDrift(d *driftScan) {
	_, _ = a.db.Exec(`INSERT INTO drift(id, scannedAt, headCommitId, report) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET scannedAt=excluded.scannedAt, headCommitId=excluded.headCommitId, report=excluded.report`,
		d.ScannedAt, store.NullStr(d.HeadCommitID), store.JSON(d))
}

// adopt records hand-made containers that a drift commit made part of the config: the next apply may replace them.
func (a *Agent) adopt(ids []string) {
	if len(ids) == 0 {
		return
	}
	var cur []string
	_, _ = a.db.GetKV("adoptedContainers", &cur)
	cur = append(cur, ids...)
	_ = a.db.SetKV("adoptedContainers", cur)
}

func (a *Agent) isAdopted(id string) bool {
	var cur []string
	_, _ = a.db.GetKV("adoptedContainers", &cur)
	for _, x := range cur {
		if x == id {
			return true
		}
	}
	return false
}

// driftLoop: every 60 s, on Docker container events (debounced 5 s) and on kicks (commits, rescans).
func (a *Agent) driftLoop(ctx context.Context) {
	kick := a.kick["drift"]
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	ev := time.NewTicker(10 * time.Second)
	defer ev.Stop()
	lastEv := time.Now()
	pending := time.Time{}
	a.rescanDriftNow(ctx)
	a.pushDrift(true) // §21 #106: once at start (the cloud may have lost it)
	// §21 #112: the shared events stream (no polling); nil while the cache is off → poll /events every 10 s as before
	var stream <-chan docker.Event
	if a.dc != nil {
		stream = a.dc.SubscribeEvents()
	}
	driftEvent := func(e docker.Event) bool {
		switch e.Action {
		case "create", "destroy", "rename", "die", "start", "update":
			return e.Type == "container"
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			a.rescanDriftNow(ctx)
		case <-kick:
			a.rescanDriftNow(ctx)
		case e := <-stream:
			if driftEvent(e) && a.branchMode() {
				pending = time.Now().Add(5 * time.Second)
			}
		case <-ev.C:
			now := time.Now()
			if a.dc != nil && a.branchMode() && !a.dc.CacheStats().Live {
				if evs, err := a.dc.Events(ctx, lastEv, now, map[string][]string{"type": {"container"}, "event": {"create", "destroy", "rename", "die", "start"}}); err == nil && len(evs) > 0 {
					pending = now.Add(5 * time.Second)
				}
			}
			lastEv = now
			if !pending.IsZero() && now.After(pending) {
				pending = time.Time{}
				a.rescanDriftNow(ctx)
			}
		}
	}
}

// rescanDriftNow scans unless an update holds the lock (the agent itself is changing containers).
func (a *Agent) rescanDriftNow(ctx context.Context) *driftScan {
	if !a.branchMode() || a.lock() != nil || a.head() == nil || a.dc == nil {
		return a.lastDrift()
	}
	prev := a.lastDrift()
	d, err := a.scanDrift(ctx)
	if err != nil {
		a.log.Warn("branch", "drift scan", "error", err.Error())
		return prev
	}
	a.saveDrift(d)
	a.pushDrift(false)
	if c := a.maybeAutoCommitDrift(ctx, time.Now()); c != nil { // §21 #113 drift policy auto-commit
		return a.lastDrift()
	}
	if sig := driftSignature(d); prev == nil || sig != driftSignature(prev) {
		if len(d.Items)+len(d.Notes) > 0 {
			keys := []string{}
			for _, it := range d.Items {
				keys = append(keys, it.Key)
			}
			a.activity(nil, "system", "drift.detected", fmt.Sprintf("%d changes made on the server outside the agent", len(d.Items)+len(d.Notes)), map[string]any{"items": len(d.Items), "notes": len(d.Notes), "keys": firstN(keys, 50)})
		}
	}
	return d
}

// driftPush is the §21 #106 drift report for the cloud (GitHub mirror deployment/<slug>/uncommitted/<site>): the
// uncommitted items + notes and the live config (head with every drift item applied; secrets masked). Cleared = no
// items, no notes, liveConfig nil.
func (a *Agent) driftPush() model.AgentDriftPushRequest {
	req := model.AgentDriftPushRequest{ScannedAt: nowTs(), Items: []model.DriftItemSummary{}, Notes: []model.DriftNoteSummary{}}
	if h := a.head(); h != nil {
		id := h.RevID
		req.HeadCommitID = &id
	}
	u := a.uncommittedNow()
	if u.report != nil {
		req.ScannedAt = u.report.ScannedAt
	}
	for _, it := range u.items {
		req.Items = append(req.Items, model.DriftItemSummary{Key: it.Key, Origin: it.Origin, Detail: it.Detail})
	}
	for _, n := range u.notes {
		req.Notes = append(req.Notes, model.DriftNoteSummary{Fingerprint: n.Fingerprint, Label: n.Label, Detail: n.Detail})
	}
	sort.Slice(req.Items, func(i, j int) bool { return req.Items[i].Key < req.Items[j].Key })
	sort.Slice(req.Notes, func(i, j int) bool { return req.Notes[i].Fingerprint < req.Notes[j].Fingerprint })
	if len(req.Items)+len(req.Notes) > 0 {
		v := maskConfig(&u.combined)
		req.LiveConfig = &v
	}
	fp, _ := canonical.HashCanonical(map[string]any{"head": req.HeadCommitID, "items": req.Items, "notes": req.Notes, "live": req.LiveConfig})
	req.Fingerprint = fp
	return req
}

// pushDrift queues the drift report for the cloud when its fingerprint changed since the last push (force: always —
// once at start). Store-and-forward through the outbox; a newer report replaces a queued older one (dedupe key).
// An old cloud without the route answers 404: the outbox drops it.
func (a *Agent) pushDrift(force bool) {
	if !a.branchMode() || a.head() == nil {
		return
	}
	req := a.driftPush()
	var last string
	_, _ = a.db.GetKV("driftPushed", &last)
	if !force && last == req.Fingerprint {
		return
	}
	_ = a.db.SetKV("driftPushed", req.Fingerprint)
	a.enqueue("branch-drift", "branch-drift", "POST", "/v1/agent/branch/drift", req)
}

func driftSignature(d *driftScan) string {
	var parts []string
	for _, it := range d.Items {
		parts = append(parts, it.Key+"="+it.SourceHash)
	}
	for _, n := range d.Notes {
		parts = append(parts, n.Fingerprint)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func (a *Agent) handleRescanDrift(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	// §21 #110 a running update never blocks this (the lock is advisory)
	if a.branchMode() && a.head() != nil {
		if _, err := a.scanDrift(r.Context()); err != nil {
			return nil, 0, herr(model.ErrUnavailable, "drift scan: "+err.Error(), nil)
		}
	}
	d := a.rescanDriftNow(r.Context())
	if d == nil {
		return model.DriftReport{ScannedAt: nowTs(), Items: []model.DriftItem{}, Notes: []model.DriftNote{}, LocalImages: []string{}}, 200, nil
	}
	return d.DriftReport, 200, nil
}

// ───────────────────────── docker live state

type liveContainer struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Image      string            `json:"Image"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Mounts        []docker.Mount                  `json:"Mounts"`
		Binds         []string                        `json:"Binds"`
		PortBindings  map[string][]docker.PortBinding `json:"PortBindings"`
		RestartPolicy docker.RestartPolicy            `json:"RestartPolicy"`
		NanoCpus      int64                           `json:"NanoCpus"`
		Memory        int64                           `json:"Memory"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
}

type liveImage struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Env          []string            `json:"Env"`
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Volumes      map[string]struct{} `json:"Volumes"`
		WorkingDir   string              `json:"WorkingDir"`
		User         string              `json:"User"`
	} `json:"Config"`
	Size int64 `json:"Size"`
}

func (a *Agent) inspectLive(ctx context.Context, id string) (*liveContainer, error) {
	raw, err := a.dc.InspectRaw(ctx, id)
	if err != nil {
		return nil, err
	}
	var c liveContainer
	return &c, json.Unmarshal(raw, &c)
}

func (a *Agent) inspectImage(ctx context.Context, id string, cache map[string]*liveImage) *liveImage {
	if im, ok := cache[id]; ok {
		return im
	}
	raw, err := a.dc.GetRaw(ctx, "/images/"+id+"/json", nil)
	if err != nil {
		cache[id] = nil
		return nil
	}
	var im liveImage
	if json.Unmarshal(raw, &im) != nil {
		cache[id] = nil
		return nil
	}
	cache[id] = &im
	return &im
}

func envMap(l []string) map[string]string {
	m := map[string]string{}
	for _, kv := range l {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var slugClean = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeSlug(s string, taken map[string]bool) string {
	s = slugClean.ReplaceAllString(strings.ToLower(strings.TrimPrefix(s, "/")), "-")
	s = strings.Trim(s, "-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "c-" + s
	}
	if len(s) > 36 {
		s = strings.TrimRight(s[:36], "-")
	}
	if len(s) < 2 {
		s += "x"
	}
	base, n := s, 2
	for taken[s] {
		s = fmt.Sprintf("%s-%d", base, n)
		n++
	}
	taken[s] = true
	return s
}

func dockerRestartToConfig(n string) string {
	switch n {
	case "always", "unless-stopped":
		return "always"
	case "on-failure":
		return "on-failure"
	}
	return "never"
}

func publishedSet(pb map[string][]docker.PortBinding) map[string]bool {
	out := map[string]bool{}
	for cp, l := range pb {
		for _, b := range l {
			if b.HostPort == "" || b.HostPort == "0" {
				continue // loopback ephemeral publication of the agent
			}
			ip := b.HostIP
			if ip == "" {
				ip = "0.0.0.0"
			}
			out[cp+"|"+ip+"|"+b.HostPort] = true
		}
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func mountSet(l []docker.Mount) map[string]bool {
	out := map[string]bool{}
	for _, m := range l {
		out[fmt.Sprintf("%s|%s|%v", m.Source, m.Target, m.ReadOnly)] = true
	}
	return out
}

func liveMounts(c *liveContainer) []docker.Mount {
	out := append([]docker.Mount{}, c.HostConfig.Mounts...)
	for _, b := range c.HostConfig.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			m := docker.Mount{Type: "bind", Source: parts[0], Target: parts[1]}
			if len(parts) > 2 && strings.Contains(parts[2], "ro") {
				m.ReadOnly = true
			}
			out = append(out, m)
		}
	}
	return out
}

func fileHex(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ───────────────────────── the scan

func (a *Agent) scanDrift(ctx context.Context) (*driftScan, error) {
	hr := a.headRevision()
	if hr == nil {
		return nil, fmt.Errorf("no head")
	}
	headID := hr.RevID
	now := nowTs()
	d := &driftScan{DriftReport: model.DriftReport{ScannedAt: now, HeadCommitID: &headID, Items: []model.DriftItem{}, Notes: []model.DriftNote{}, LocalImages: []string{}},
		LocalCatalog: model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}},
		Adopt:        map[string][]string{}, Remove: map[string][]string{}, TakeBytes: map[string]*driftTakeBytes{}}
	var live model.Config // a deep copy of the raw head config (the items compare with the cloud's bytes)
	if err := branch.FromTree(branch.ToTree(hr.Config), &live); err != nil {
		return nil, err
	}
	details := map[string]model.DriftItem{} // key → origin/detail (merged into the computed items below)
	note := func(fp, key, label, detail string, actions ...string) {
		var k *string
		if key != "" {
			k = &key
		}
		d.Notes = append(d.Notes, model.DriftNote{Fingerprint: fp, Key: k, Label: label, Detail: detail, Actions: actions})
	}
	mark := func(key, origin, detail string) {
		details[key] = model.DriftItem{Origin: origin, Detail: detail, ObservedAt: now}
	}
	site := siteOf(&live, a.site)
	if site == nil {
		return d, nil
	}
	norm := siteOf(revConfig(hr), a.site)
	builts, err := a.buildSite(hr, norm)
	if err != nil {
		return nil, fmt.Errorf("build head: %w", err)
	}
	byslug := map[string]*driver.Built{}
	for _, b := range builts {
		byslug[b.ContainerSlug] = b
	}
	all, err := a.dc.ContainerList(ctx, true)
	if err != nil {
		return nil, err
	}
	network := driver.NetworkName(a.dep, a.site)
	imgCache := map[string]*liveImage{}
	used := map[string]bool{}
	catalogs := []*model.RevisionCatalog{&hr.Catalog}
	for _, ref := range a.cloudCommitMetas(model.CloudRecentCommits) {
		if cc, ok := a.cloudCommit(ref.CommitID); ok && cc.Catalog != nil {
			catalogs = append(catalogs, cc.Catalog)
		}
	}
	// §21 #113 site images resolved in the GUI (a container started with one is not a "new local image")
	if si := a.siteImageCatalog(); len(si.Images) > 0 {
		catalogs = append(catalogs, &si)
	}
	// sameImage: a container's image id is the config digest (classic store) or the manifest / index digest
	// (containerd image store, Docker ≥ 29): compare with the id of the agent's integrity tag opm/cfg:<config hex>
	ids := map[string]string{}
	localID := func(configDigest string) string {
		if v, ok := ids[configDigest]; ok {
			return v
		}
		v := ""
		if im, err := a.dc.ImageInspect(ctx, driver.ImageRef(configDigest)); err == nil {
			v = im.ID
		}
		ids[configDigest] = v
		return v
	}
	// healed: integrity tags put back this scan (a `docker image prune -a` on the containerd store removes them while
	// the running containers keep the image) — §21 #120
	healed := map[string]bool{}
	sameImage := func(liveID, configDigest, createdFrom string) bool {
		if liveID == configDigest || (liveID != "" && liveID == localID(configDigest)) {
			return true
		}
		// the agent created the container FROM opm/cfg:<config hex>: when that tag is gone the container still runs
		// that image (a tag pointing at another image is a real change, handled above)
		if createdFrom != "" && createdFrom == driver.ImageRef(configDigest) && localID(configDigest) == "" && liveID != "" {
			if !healed[configDigest] {
				healed[configDigest] = true
				repo, tag, _ := strings.Cut(createdFrom, ":")
				if err := a.dc.ImageTag(ctx, liveID, repo, tag); err == nil {
					ids[configDigest] = liveID
					a.log.Info("drift", "integrity tag restored (the image was untagged, not changed)", "tag", createdFrom, "image", shortID(liveID))
				}
			}
			return true
		}
		return false
	}
	// imageFor: the catalog image with this config digest, else a local image entry
	imageFor := func(id, hint string) (svc, ver string, spec *model.ImageSpec) {
		for _, cat := range catalogs {
			keys := make([]string, 0, len(cat.Images))
			for k := range cat.Images {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				im := cat.Images[k]
				if sameImage(id, im.ConfigDigest, hint) {
					if _, ok := hr.Catalog.Images[k]; !ok {
						d.LocalCatalog.Images[k] = im
						if s, ok := cat.Services[im.ServiceSlug]; ok {
							d.LocalCatalog.Services[im.ServiceSlug] = s
						}
					}
					return im.ServiceSlug, im.Version, &im
				}
			}
		}
		li := a.inspectImage(ctx, id, imgCache)
		sp, label := localImageSpec(li, id, hint)
		svc, ver = sp.ServiceSlug, sp.Version
		key := svc + ":" + ver
		d.LocalCatalog.Images[key] = sp
		d.LocalCatalog.Services[svc] = model.CatalogService{ServiceSlug: svc, Label: label}
		d.LocalImages = append(d.LocalImages, key)
		// readable + integrity tags so the engine can run it like a loaded image (§15.1.1); §21 #112 only when missing
		// (a tag is a Docker write: every 60 s scan used to tag again)
		hasTag := func(t string) bool {
			if li == nil {
				return false
			}
			for _, x := range li.RepoTags {
				if x == t {
					return true
				}
			}
			return false
		}
		if cfgTag := "opm/cfg:" + strings.TrimPrefix(id, "sha256:"); !hasTag(cfgTag) {
			_ = a.dc.ImageTag(ctx, id, "opm/cfg", strings.TrimPrefix(id, "sha256:"))
		}
		_, rrepo, rtag := driver.ReadableRef(sp)
		if !hasTag(rrepo + ":" + rtag) {
			_ = a.dc.ImageTag(ctx, id, rrepo, rtag)
		}
		return svc, ver, &sp
	}
	containers := site.Config.Containers
	idx := func(slug string) int {
		for i := range containers {
			if containers[i].ContainerSlug == slug {
				return i
			}
		}
		return -1
	}
	var removeSlugs []string
	for _, cc := range append([]model.Container{}, containers...) {
		b := byslug[cc.ContainerSlug]
		if b == nil || !cc.Enabled {
			continue
		}
		key := "sites/" + a.site + "/containers/" + cc.ContainerSlug
		var found *docker.ContainerSummary
		handmade := false
		for i := range all {
			c := &all[i]
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			if driver.IsFailedName(name) {
				continue
			}
			if c.Labels[driver.LDeployment] == a.dep && c.Labels[driver.LSite] == a.site && c.Labels[driver.LContainer] == cc.ContainerSlug {
				found = c
				break
			}
		}
		if found == nil {
			for i := range all {
				c := &all[i]
				if n := strings.TrimPrefix(firstName(c.Names), "/"); (n == b.Name || n == cc.ContainerSlug) && c.Labels[driver.LDeployment] == "" {
					found, handmade = c, true
					break
				}
			}
		}
		if found == nil {
			removeSlugs = append(removeSlugs, cc.ContainerSlug)
			mark(key, "docker", "container "+cc.ContainerSlug+" was removed by hand")
			continue
		}
		used[found.ID] = true
		lc, err := a.inspectLive(ctx, found.ID)
		if err != nil {
			continue
		}
		if handmade {
			d.Adopt[key] = append(d.Adopt[key], found.ID)
			d.Remove[key] = append(d.Remove[key], found.ID)
		}
		why := "changed on the server"
		if handmade {
			why = "container " + cc.ContainerSlug + " was recreated by hand"
		}
		exp := b.CreateConfig(hr.RevID, hr.Seq)
		i := idx(cc.ContainerSlug)
		c := &containers[i]
		// image
		img := a.inspectImage(ctx, lc.Image, imgCache)
		if !sameImage(lc.Image, b.Image.ConfigDigest, lc.Config.Image) {
			svc, ver, _ := imageFor(lc.Image, lc.Config.Image)
			c.ServiceSlug, c.Version = svc, ver
			for _, k := range []string{key + "/image"} {
				d.Adopt[k] = d.Adopt[key]
				d.Remove[k] = append(d.Remove[k], found.ID)
				mark(k, "docker", why+": image "+lc.Config.Image+" ("+shortID(lc.Image)+")")
			}
		}
		// env
		le, ee := envMap(lc.Config.Env), envMap(exp.Env)
		ie := map[string]string{}
		if img != nil {
			ie = envMap(img.Config.Env)
		}
		rowOf := func(name string) int {
			for j := range c.Env {
				if c.Env[j].Name == name {
					return j
				}
			}
			return -1
		}
		names := map[string]bool{}
		for k := range le {
			names[k] = true
		}
		for k := range ee {
			names[k] = true
		}
		sorted := make([]string, 0, len(names))
		for k := range names {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			lv, lok := le[n]
			ev, eok := ee[n]
			if lok && eok && lv == ev {
				continue
			}
			if lok && !eok {
				if iv, ok := ie[n]; ok && iv == lv {
					continue // a default of the docker image itself (PATH …)
				}
			}
			ekey := key + "/env/" + n
			j := rowOf(n)
			switch {
			case !lok: // missing on the server
				if j >= 0 {
					c.Env = append(c.Env[:j], c.Env[j+1:]...)
					mark(ekey, "docker", why+": env "+n+" removed")
				} else {
					note("env-default-removed:"+cc.ContainerSlug+":"+n, ekey, cc.ContainerSlug+" · env "+n, "the image default of "+n+" was removed on the server — an image default cannot be unset in the config", "discard", "accept")
				}
			case j >= 0 && c.Env[j].SecretRef != nil:
				sl := *c.Env[j].SecretRef
				for s := range live.Secrets {
					if live.Secrets[s].SecretSlug == sl {
						v := lv
						live.Secrets[s].Value = &v
					}
				}
				mark("secrets/"+sl+"/value", "docker", why+": the value of secret "+sl+" (env "+n+" of "+cc.ContainerSlug+") changed")
			case j >= 0 && c.Env[j].Value != nil && strings.Contains(*c.Env[j].Value, "secret."):
				note("env-secret-expr:"+cc.ContainerSlug+":"+n+":"+sha8(lv), ekey, cc.ContainerSlug+" · env "+n, "a value built from a secret changed on the server; it is never written into the config in clear", "discard", "accept")
			default:
				v := lv
				if j >= 0 {
					c.Env[j].Value, c.Env[j].SecretRef = &v, nil
				} else {
					c.Env = append(c.Env, model.ContainerEnv{Name: n, Value: &v})
				}
				mark(ekey, "docker", why+": env "+n)
			}
		}
		// command / entrypoint
		var imgCmd, imgEp []string
		if img != nil {
			imgCmd, imgEp = img.Config.Cmd, img.Config.Entrypoint
		}
		expCmd, expEp := exp.Cmd, exp.Entrypoint
		if len(expCmd) == 0 {
			expCmd = imgCmd
		}
		if len(expEp) == 0 {
			expEp = imgEp
		}
		{
			if !sameStrings(lc.Config.Cmd, expCmd) {
				if sameStrings(lc.Config.Cmd, imgCmd) {
					c.Command = nil
				} else {
					c.Command = append([]string{}, lc.Config.Cmd...)
				}
				mark(key+"/command", "docker", why+": command")
			}
			if !sameStrings(lc.Config.Entrypoint, expEp) {
				if sameStrings(lc.Config.Entrypoint, imgEp) {
					c.Entrypoint = nil
				} else {
					c.Entrypoint = append([]string{}, lc.Config.Entrypoint...)
				}
				mark(key+"/entrypoint", "docker", why+": entrypoint")
			}
		}
		// restart + resources
		if lc.HostConfig.RestartPolicy.Name != exp.HostConfig.RestartPolicy.Name {
			c.Restart = dockerRestartToConfig(lc.HostConfig.RestartPolicy.Name)
			mark(key+"/restart", "docker", why+": restart policy "+lc.HostConfig.RestartPolicy.Name)
		}
		if lc.HostConfig.NanoCpus != exp.HostConfig.NanoCpus || lc.HostConfig.Memory != exp.HostConfig.Memory {
			c.Resources = model.ContainerResources{}
			if lc.HostConfig.NanoCpus > 0 {
				v := float64(lc.HostConfig.NanoCpus) / 1e9
				c.Resources.Cpus = &v
			}
			if lc.HostConfig.Memory > 0 {
				v := float64(lc.HostConfig.Memory) / (1 << 20)
				c.Resources.MemoryMb = &v
			}
			mark(key+"/resources", "docker", why+": cpu / memory limits")
		}
		// ports
		if !sameSet(publishedSet(lc.HostConfig.PortBindings), publishedSet(exp.HostConfig.PortBindings)) {
			rows := []model.ContainerPort{}
			for _, pr := range b.Ports {
				row := model.ContainerPort{PortName: pr.Name, HostIP: "0.0.0.0", Edge: pr.Edge}
				for _, bnd := range lc.HostConfig.PortBindings[fmt.Sprintf("%d/%s", pr.ContainerPort, pr.Protocol)] {
					if bnd.HostPort != "" && bnd.HostPort != "0" {
						n, _ := strconv.Atoi(bnd.HostPort)
						row.HostPort = numOf(n)
						if bnd.HostIP == "127.0.0.1" {
							row.HostIP = "127.0.0.1"
						}
					}
				}
				rows = append(rows, row)
			}
			c.Ports = rows
			mark(key+"/ports", "docker", why+": published ports")
		}
		// mounts
		lm := liveMounts(lc)
		if !sameSet(mountSet(lm), mountSet(exp.HostConfig.Mounts)) {
			c.Volumes = a.mountsToRows(lm, &site.Config, &b.Image, &live)
			mark(key+"/volumes", "docker", why+": mounts")
		}
	}
	// containers removed by hand
	for _, slug := range removeSlugs {
		if i := idx(slug); i >= 0 {
			containers = append(containers[:i], containers[i+1:]...)
		}
	}
	// unknown containers on the site network (or labelled for this site with an unknown slug)
	taken := map[string]bool{}
	for _, c := range containers {
		taken[c.ContainerSlug] = true
	}
	for i := range all {
		sc := &all[i]
		if used[sc.ID] {
			continue
		}
		name := ""
		if len(sc.Names) > 0 {
			name = strings.TrimPrefix(sc.Names[0], "/")
		}
		if driver.IsFailedName(name) {
			continue
		}
		labelled := sc.Labels[driver.LDeployment] != ""
		if labelled && (sc.Labels[driver.LDeployment] != a.dep || sc.Labels[driver.LSite] != a.site) {
			continue // another deployment / site: never ours
		}
		if labelled {
			if _, known := byslug[sc.Labels[driver.LContainer]]; known {
				continue
			}
		}
		// §21 #112: an unlabelled container that the list shows off the site network is never ours — no inspect
		if _, onNet := sc.NetworkSettings.Networks[network]; !labelled && sc.NetworkSettings.Networks != nil && !onNet {
			continue
		}
		lc, err := a.inspectLive(ctx, sc.ID)
		if err != nil {
			continue
		}
		if _, onNet := lc.NetworkSettings.Networks[network]; !onNet && !labelled {
			continue
		}
		slug := sanitizeSlug(name, taken)
		svc, ver, spec := imageFor(lc.Image, lc.Config.Image)
		img := a.inspectImage(ctx, lc.Image, imgCache)
		nc := model.Container{ContainerSlug: slug, ServiceSlug: svc, Version: ver, Enabled: true, Env: []model.ContainerEnv{}, Ports: []model.ContainerPort{}, Volumes: []model.ContainerMount{}, Restart: dockerRestartToConfig(lc.HostConfig.RestartPolicy.Name), Depends: []model.ContainerRef{}}
		ie := map[string]string{}
		if img != nil {
			ie = envMap(img.Config.Env)
			if !sameStrings(lc.Config.Cmd, img.Config.Cmd) {
				nc.Command = lc.Config.Cmd
			}
			if !sameStrings(lc.Config.Entrypoint, img.Config.Entrypoint) {
				nc.Entrypoint = lc.Config.Entrypoint
			}
		}
		le := envMap(lc.Config.Env)
		ks := make([]string, 0, len(le))
		for k := range le {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			if iv, ok := ie[k]; ok && iv == le[k] {
				continue
			}
			v := le[k]
			nc.Env = append(nc.Env, model.ContainerEnv{Name: k, Value: &v})
		}
		for cp, l := range lc.HostConfig.PortBindings {
			num, proto, _ := strings.Cut(cp, "/")
			pn := ""
			for _, p := range spec.Ports {
				if p.Port.Num != nil && strconv.Itoa(int(*p.Port.Num)) == num && (p.Protocol == proto || p.Protocol == "") {
					pn = p.Name
				}
			}
			if pn == "" {
				pn = "p" + num
				n, _ := strconv.Atoi(num)
				spec.Ports = append(spec.Ports, model.ImagePort{Name: pn, Port: numOf(n), Protocol: proto})
				if k := svc + ":" + ver; d.LocalCatalog.Images[k].ConfigDigest != "" {
					d.LocalCatalog.Images[k] = *spec
				}
			}
			for _, bnd := range l {
				if bnd.HostPort == "" {
					continue
				}
				n, _ := strconv.Atoi(bnd.HostPort)
				ip := "0.0.0.0"
				if bnd.HostIP == "127.0.0.1" {
					ip = "127.0.0.1"
				}
				nc.Ports = append(nc.Ports, model.ContainerPort{PortName: pn, HostPort: numOf(n), HostIP: ip, Edge: true})
			}
		}
		sort.Slice(nc.Ports, func(i, j int) bool { return nc.Ports[i].PortName < nc.Ports[j].PortName })
		nc.Volumes = a.mountsToRows(liveMounts(lc), &site.Config, spec, &live)
		if lc.HostConfig.NanoCpus > 0 {
			v := float64(lc.HostConfig.NanoCpus) / 1e9
			nc.Resources.Cpus = &v
		}
		if lc.HostConfig.Memory > 0 {
			v := float64(lc.HostConfig.Memory) / (1 << 20)
			nc.Resources.MemoryMb = &v
		}
		containers = append(containers, nc)
		key := "sites/" + a.site + "/containers/" + slug
		d.Adopt[key] = append(d.Adopt[key], sc.ID)
		d.Remove[key] = append(d.Remove[key], sc.ID)
		mark(key, "docker", "container "+name+" ("+lc.Config.Image+") was created by hand on the site network")
	}
	site.Config.Containers = containers
	// per-site file + secret copies
	seenFile := map[string]bool{}
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		for p, sf := range b.SiteFiles {
			if seenFile[p] {
				continue
			}
			seenFile[p] = true
			h, n, err := fileHex(p)
			if err != nil || h == sf.Sha {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			blob, err := a.storeBlobBytes(data, "file", "local")
			if err != nil {
				continue
			}
			key := "files/" + sf.FileSlug
			if sf.Src != "" { // render none: the bytes ARE the content
				for i := range live.Files {
					if live.Files[i].FileSlug == sf.FileSlug {
						live.Files[i].Content = &model.BlobRef{Sha256: "sha256:" + blob, SizeBytes: n}
					}
				}
				mark(key, "file", "the file copy "+p+" was edited on the server")
			} else {
				fp := "file-rendered:" + sf.FileSlug + ":" + blob
				d.TakeBytes[fp] = &driftTakeBytes{FileSlug: sf.FileSlug, Sha: blob, Size: n}
				note(fp, key, "File "+sf.FileSlug, "the rendered copy "+p+" was edited on the server (it is rendered from expressions — keep it as plain content, accept, or discard)", "discard", "accept", "take-bytes")
			}
		}
		for p, val := range b.SecretFiles {
			if seenFile[p] {
				continue
			}
			seenFile[p] = true
			data, err := os.ReadFile(p)
			if err != nil || string(data) == string(val) {
				continue
			}
			slug := strings.TrimPrefix(p, driver.SecretPath(a.dataDir, ""))
			for i := range live.Secrets {
				if live.Secrets[i].SecretSlug == slug {
					v := string(data)
					live.Secrets[i].Value = &v
				}
			}
			mark("secrets/"+slug+"/value", "secret-file", "the secret file of "+slug+" was edited on the server")
		}
	}
	// §21 #117 persist-to-cloud volumes: the live tree is the volume's content on the server (per-file content items)
	contentLive, _ := a.liveContentConfig(&live)
	live = *contentLive
	if ls := siteOf(&live, a.site); ls != nil {
		for _, p := range a.persistVolumes() {
			if v := ls.Config.FindVolume(p.decl.VolumeSlug); v != nil {
				details["sites/"+a.site+"/volumes/"+p.decl.VolumeSlug+"/content"] = model.DriftItem{Origin: "volume-content", Detail: "the content of persist-to-cloud volume " + p.decl.VolumeSlug + " changed on the server", ObservedAt: now}
			}
		}
	}
	// the computed items (two-way: head → live)
	for _, it := range branch.Uncommitted(hr.Config, live, branch.Options{SecretValues: true, Scope: a.site}) {
		di := model.DriftItem{MergeItem: it, Origin: "docker", Detail: "changed on the server", ObservedAt: now}
		if x, ok := details[it.Key]; ok {
			di.Origin, di.Detail = x.Origin, x.Detail
		} else {
			// a field item of a hand-recreated container: inherit the container's detail
			for k, x := range details {
				if strings.HasPrefix(it.Key, k+"/") || strings.HasPrefix(k, it.Key+"/") {
					di.Origin, di.Detail = x.Origin, x.Detail
				}
			}
		}
		if ids, ok := d.Adopt[it.Key]; !ok {
			for k, v := range d.Adopt {
				if strings.HasPrefix(it.Key, k+"/") {
					d.Adopt[it.Key], d.Remove[it.Key] = v, d.Remove[k]
					ids = v
				}
			}
			_ = ids
		}
		d.Items = append(d.Items, di)
	}
	d.LiveConfig = live
	return d, nil
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func sha8(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// mountsToRows maps live binds to ContainerMount rows (image volume rows by mount path, else extra mounts).
func (a *Agent) mountsToRows(lm []docker.Mount, sc *model.SiteConfig, img *model.ImageSpec, cfg *model.Config) []model.ContainerMount {
	rows := []model.ContainerMount{}
	for _, m := range lm {
		if m.Type != "" && m.Type != "bind" {
			continue
		}
		src := model.VolumeSource{Kind: "host", Path: m.Source}
		for _, v := range sc.VolumeList() {
			if p := driver.VolumePath(a.dataDir, sc, v.VolumeSlug); p == m.Source || strings.TrimRight(p, "/") == strings.TrimRight(m.Source, "/") {
				src = model.VolumeSource{Kind: "volume", VolumeSlug: v.VolumeSlug}
			}
		}
		for i := range cfg.Files {
			if driver.FilePath(a.dataDir, &cfg.Files[i]) == m.Source {
				src = model.VolumeSource{Kind: "file", FileSlug: cfg.Files[i].FileSlug}
			}
		}
		if strings.HasPrefix(m.Source, driver.SecretPath(a.dataDir, "")) {
			src = model.VolumeSource{Kind: "secret", SecretSlug: strings.TrimPrefix(m.Source, driver.SecretPath(a.dataDir, ""))}
		}
		row := model.ContainerMount{Source: src, ReadOnly: m.ReadOnly}
		matched := false
		if img != nil {
			for _, iv := range img.Volumes {
				if iv.MountPath == m.Target {
					n := iv.Name
					row.ImageVolume = &n
					matched = true
				}
			}
		}
		if !matched {
			t := m.Target
			row.MountPath = &t
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		ki, kj := "", ""
		if rows[i].ImageVolume != nil {
			ki = "0" + *rows[i].ImageVolume
		} else if rows[i].MountPath != nil {
			ki = "1" + *rows[i].MountPath
		}
		if rows[j].ImageVolume != nil {
			kj = "0" + *rows[j].ImageVolume
		} else if rows[j].MountPath != nil {
			kj = "1" + *rows[j].MountPath
		}
		return ki < kj
	})
	return rows
}

func numOf(n int) model.NumOrExpr {
	f := float64(n)
	return model.NumOrExpr{Num: &f}
}

func firstName(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// removeDroppedAdopted (APPLY of a merge): hand-made containers adopted by a drift commit are not labelled, so the
// engine's plan cannot see them; the ones whose containerSlug the target config no longer has are stopped + removed
// here ("remove locally"; their data folders stay).
func (a *Agent) removeDroppedAdopted(ctx context.Context, rev *model.Revision) {
	var ids []string
	_, _ = a.db.GetKV("adoptedContainers", &ids)
	if len(ids) == 0 {
		return
	}
	keep := map[string]bool{}
	if s := siteOf(&rev.Config, a.site); s != nil {
		for _, c := range s.Config.Containers {
			if c.Enabled {
				keep[c.ContainerSlug] = true
				keep[a.drv.NameFor(c.ContainerSlug)] = true
			}
		}
	}
	all, err := a.dc.ContainerList(ctx, true)
	if err != nil {
		return
	}
	var rest []string
	for _, id := range ids {
		found := false
		for _, c := range all {
			if c.ID != id {
				continue
			}
			found = true
			if !keep[strings.TrimPrefix(firstName(c.Names), "/")] {
				_ = a.drv.StopRemove(ctx, id, 10)
				a.log.Info("branch", "hand-made container removed by the merge", "id", shortID(id), "name", firstName(c.Names))
				found = false
			}
		}
		if found {
			rest = append(rest, id)
		}
	}
	_ = a.db.SetKV("adoptedContainers", rest)
}
