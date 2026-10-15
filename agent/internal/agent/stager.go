package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/driver"
	"opm/agent/internal/edge"
	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// errFinal marks a staging failure that waits for restage (validation, dry run, digest mismatch).
type errFinal struct{ msg string }

func (e *errFinal) Error() string { return e.msg }

func final(format string, args ...any) error { return &errFinal{fmt.Sprintf(format, args...)} }

func (a *Agent) startStaging(revID string) {
	a.mu.Lock()
	if _, running := a.stagers[revID]; running {
		a.mu.Unlock()
		return
	}
	c, cancel := context.WithCancel(a.ctx)
	a.stagers[revID] = cancel
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			a.mu.Lock()
			delete(a.stagers, revID)
			a.mu.Unlock()
			cancel()
		}()
		defer func() { // §21 #110 a panic while staging never takes the agent down (staging restarts later)
			if r := recover(); r != nil {
				a.log.Error("staging", "staging panicked", "revId", revID, "panic", fmt.Sprint(r))
			}
		}()
		a.stage(c, revID)
	}()
}

func (a *Agent) stopStaging(revID string) {
	a.mu.Lock()
	if c, ok := a.stagers[revID]; ok {
		c()
	}
	a.mu.Unlock()
}

// resumeStaging restarts staging for open proposals that are not ready on this site (agent restart).
func (a *Agent) resumeStaging() {
	for _, p := range a.openProposals() {
		if p.Status == model.ProposalApproved || p.Status == model.ProposalApplying {
			continue
		}
		rev, ok := a.revision(p.RevID)
		if !ok || !contains(a.participatingSites(rev), a.site) {
			continue
		}
		if row, ok := a.staging(p.RevID, a.site); ok && (row.Staging == model.StagingReady || row.Staging == model.StagingFailed) {
			continue
		}
		a.startStaging(p.RevID)
	}
}

// updateOwnStaging records this site's staging (row + proposal map + cloud report).
func (a *Agent) updateOwnStaging(revID string, st model.SiteStaging, detail *model.StagingDetail, report bool) {
	t := nowTs()
	st.UpdatedAt = &t
	a.setStaging(revID, a.site, st, detail)
	if detail != nil {
		a.mu.Lock()
		cp := *detail
		cp.Staging = st
		a.details[revID] = &cp
		a.mu.Unlock()
	}
	if p, ok := a.proposal(revID); ok && !model.IsTerminal(p.Status) {
		p.Sites[a.site] = st
		a.recomputeStatus(p)
		a.putProposal(p)
	}
	if report && a.isBranchRev(revID) {
		report = false // §21 #104 branch objects are not proposals
	}
	if report {
		pr := st.Progress
		r := model.ProposalStatusReport{SiteSlug: a.site, Staging: st.Staging, Progress: &pr}
		if st.Error != nil {
			r.Error = model.Some(*st.Error)
		} else {
			r.Error = model.Null[string]()
		}
		a.queueStatus(revID, r)
	}
}

type stageBlob struct {
	need   blobNeed
	state  string
	source *string
}

func (a *Agent) stage(ctx context.Context, revID string) {
	start := time.Now()
	rev, ok := a.revision(revID)
	if !ok {
		return
	}
	detail := &model.StagingDetail{SiteSlug: a.site, Layers: model.Count{}, Files: model.Count{}, Blobs: []model.StagingBlob{}, Issues: []model.ValidationIssue{}}
	fail := func(msg string, issues []model.ValidationIssue) {
		if issues != nil {
			detail.Issues = issues
		}
		e := msg
		st := model.SiteStaging{Staging: model.StagingFailed, Progress: 0, Error: &e}
		a.updateOwnStaging(revID, st, detail, true)
		a.stagedActivity(revID, fmt.Sprintf("staging of seq %d failed: %s", rev.Seq, msg),
			map[string]any{"revId": revID, "result": "failed", "error": msg, "durationMs": time.Since(start).Milliseconds()})
		a.log.Warn("stager", "staging failed", "revId", revID, "error", msg)
	}
	a.updateOwnStaging(revID, model.SiteStaging{Staging: model.StagingDownloading, Progress: 0}, detail, true)
	cfg := revConfig(rev)
	site := siteOf(cfg, a.site)
	var builts []*driver.Built
	if site != nil {
		// 1. validate (agent mode, catalog of the revision) + resolve this site's expressions
		vctx := a.validationContext(&rev.Catalog)
		is := validate.ValidateConfig(cfg, vctx)
		if !is.Ok() {
			fail(issueText(is.Errors()[0]), is.List)
			return
		}
		// locations resolved against agentHome: "./" vs absolute, never nested, never an agent directory (V_LOCATION)
		if li := validate.CheckResolvedLocations(cfg, a.site, a.dataDir); len(li) > 0 {
			fail(issueText(li[0]), li)
			return
		}
		// a volume kind change while the volume holds data → "use a new volume name" (round E)
		headCfg, _ := a.headConfig()
		if err := a.checkVolumeKinds(headCfg, cfg); err != nil {
			fail(err.Error(), nil)
			return
		}
		var err error
		builts, err = a.buildSiteOpts(rev, site, true)
		if err != nil {
			fail(err.Error(), nil)
			return
		}
		re, err := a.resolveEdge(cfg, site)
		if err != nil {
			fail("edge: "+err.Error(), nil)
			return
		}
		for _, h := range re.Hosts {
			if h.TLS.Mode == "acme" && h.TLS.Challenge == "dns-01" {
				if _, err := dnsConfigFor(cfg, h); err != nil {
					fail("edge: "+err.Error(), nil)
					return
				}
			}
		}
		// 2 + 4. blobs (images + files)
		var blobs []*stageBlob
		seen := map[string]bool{}
		add := func(n blobNeed) {
			if n.Hex == "" || seen[n.Hex] {
				return
			}
			seen[n.Hex] = true
			blobs = append(blobs, &stageBlob{need: n})
		}
		for _, b := range builts {
			if !b.Enabled {
				continue
			}
			img := b.Image
			ref := model.FetchRef{ServiceSlug: img.ServiceSlug, Version: img.Version}
			if img.Digest != "" { // §21 #104 a local image (drift) is already in docker: no blobs
				add(blobNeed{Hex: hexOf(img.Digest), Kind: "manifest", Ref: ref})
				add(blobNeed{Hex: hexOf(img.ConfigDigest), Kind: "config", Ref: ref})
				for _, l := range img.Layers {
					add(blobNeed{Hex: hexOf(l.Digest), Size: l.SizeBytes, Kind: "layer", Ref: ref})
					detail.Layers.Total++
				}
			}
			for _, fh := range b.FileBlobs {
				detail.Files.Total++
				size := int64(0)
				for _, f := range cfg.Files {
					if f.Content != nil && hexOf(f.Content.Sha256) == fh {
						size = f.Content.SizeBytes
						add(blobNeed{Hex: fh, Size: size, Kind: "file", Ref: model.FetchRef{FileSlug: f.FileSlug, RevID: revID}})
					}
				}
			}
		}
		for _, h := range site.Config.Edge.Hosts {
			if h.TLS.Mode == "files" {
				for _, f := range cfg.Files {
					if f.FileSlug == h.TLS.CertFileSlug && f.Content != nil {
						detail.Files.Total++
						add(blobNeed{Hex: hexOf(f.Content.Sha256), Size: f.Content.SizeBytes, Kind: "file", Ref: model.FetchRef{FileSlug: f.FileSlug, RevID: revID}})
					}
				}
			}
		}
		// volume starter content (round E): seed files + archives
		for _, n := range seedBlobs(cfg, site, revID) {
			if !seen[n.Hex] {
				detail.Files.Total++
			}
			add(n)
		}
		// cross-check digests with the cloud when reachable (mismatch → failed)
		if err := a.crossCheckManifests(ctx, builts); err != nil {
			var ef *errFinal
			if errors.As(err, &ef) {
				fail(err.Error(), nil)
				return
			}
		}
		if err := a.stageBlobs(ctx, revID, rev, blobs, detail); err != nil {
			if ctx.Err() != nil {
				return
			}
			fail(err.Error(), nil)
			return
		}
		// 2b. files with expressions: rendered ONCE per site (V_FILE_RENDER / V_FILE_JSON)
		if contents, blobs := a.fileContents(cfg, builts), a.seedBlobContents(cfg); len(contents) > 0 || len(blobs) > 0 {
			vctx.FileContents, vctx.BlobContents = contents, blobs // §21 #101 BlobContents: volume content with render expressions
			if is := validate.ValidateConfig(cfg, vctx); !is.Ok() {
				fail(issueText(is.Errors()[0]), is.List)
				return
			}
		}
		if builts, err = a.buildSite(rev, site); err != nil {
			fail(err.Error(), nil)
			return
		}
		// 2c. the one copy per site of every mounted file is staged next to its location; APPLY renames it in place
		staged := map[string]bool{}
		for _, b := range builts {
			if !b.Enabled {
				continue
			}
			for p, sf := range b.SiteFiles {
				if staged[p] {
					continue
				}
				staged[p] = true
				if err := driver.StageSiteFile(p, sf, rev.RevID); err != nil {
					fail("file "+sf.FileSlug+": "+err.Error(), nil)
					return
				}
			}
		}
		// 3. docker load
		for _, b := range builts {
			if !b.Enabled {
				continue
			}
			if b.Image.Digest == "" { // §21 #104 local image: make sure the integrity tag exists (no blobs to load from)
				if _, err := driver.EnsureReadable(ctx, a.dc, b.Image); err != nil {
					fail("local image "+b.Image.ServiceSlug+":"+b.Image.Version+": "+err.Error()+" (the image was removed from Docker)", nil)
					return
				}
			}
			present, err := driver.ImagePresent(ctx, a.dc, b.Image, a.blobPathOrDefault)
			if err != nil {
				fail("docker: "+err.Error(), nil)
				return
			}
			if !present {
				a.log.Info("stager", "docker load", "image", b.Image.ServiceSlug+":"+b.Image.Version)
				if err := driver.LoadImage(ctx, a.dc, b.Image, a.blobPathOrDefault); err != nil {
					fail("docker load "+b.Image.ServiceSlug+":"+b.Image.Version+": "+err.Error(), nil)
					return
				}
			}
		}
		// 5. not-found page
		if err := a.writeNotFoundPages(&rev.Catalog); err != nil {
			fail(err.Error(), nil)
			return
		}
		// 6. dry run (incl. caddy.Validate of the new edge config)
		if in, err := a.edgeInput(cfg, &rev.Catalog); err != nil {
			fail("edge: "+err.Error(), nil)
			return
		} else if in != nil {
			if err := edge.ValidateInput(in, filepath.Join(a.dataDir, "certs")); err != nil {
				fail("edge: "+err.Error(), nil)
				return
			}
		}
		if err := a.dryRun(ctx, rev, site, builts); err != nil {
			fail("dry run: "+err.Error(), nil)
			return
		}
	} else if err := a.dc.Ping(ctx); err != nil {
		fail("docker: "+err.Error(), nil)
		return
	}
	if ctx.Err() != nil {
		return
	}
	a.updateOwnStaging(revID, model.SiteStaging{Staging: model.StagingReady, Progress: 1}, detail, true)
	a.stagedActivity(revID, fmt.Sprintf("seq %d staged", rev.Seq), map[string]any{"revId": revID, "result": "ready", "error": nil, "durationMs": time.Since(start).Milliseconds()})
	a.log.Info("stager", "staging ready", "revId", revID, "durationMs", time.Since(start).Milliseconds())
	a.kickLoop("ping")
}

func hexOf(d string) string { return strings.TrimPrefix(d, "sha256:") }

// issueText is the staging error text of a validation issue (file render errors: "V_FILE_RENDER <file>:<line>:<col>: …").
func issueText(x model.ValidationIssue) string {
	if strings.HasPrefix(x.Code, "V_FILE_") {
		return x.Code + " " + x.Message
	}
	return fmt.Sprintf("%s: %s", x.Path, x.Message)
}

// fileContents reads the source of every render-'expressions' file mounted on this site (from the staged blobs).
func (a *Agent) fileContents(cfg *model.Config, builts []*driver.Built) map[string]string {
	need := map[string]bool{}
	for _, b := range builts {
		for _, h := range b.FileBlobs {
			need[h] = true
		}
	}
	if s := siteOf(cfg, a.site); s != nil {
		for _, slug := range seedFileSlugs(s) { // volume starter files are rendered once per site too (round E)
			for _, f := range cfg.Files {
				if f.FileSlug == slug && f.Content != nil {
					need[hexOf(f.Content.Sha256)] = true
				}
			}
		}
	}
	out := map[string]string{}
	for _, f := range cfg.Files {
		if f.Content == nil || f.RenderMode() != "expressions" || !need[hexOf(f.Content.Sha256)] {
			continue
		}
		if raw, err := os.ReadFile(driver.FileBlobPath(a.dataDir, hexOf(f.Content.Sha256))); err == nil {
			out[f.FileSlug] = string(raw)
		}
	}
	return out
}

func (a *Agent) blobPathOrDefault(h string) string {
	if p, ok := a.blobPath(h); ok {
		return p
	}
	return a.blobFile(h, "layer")
}

// buildSite builds the container specs of a site (expressions resolved, files rendered per container).
func (a *Agent) buildSite(rev *model.Revision, site *model.Site) ([]*driver.Built, error) {
	return a.buildSiteOpts(rev, site, false)
}

// buildSiteOpts: deferRender = files with render 'expressions' are not rendered (staging before the file blobs are
// here: only the blob list is used, the hash is meaningless).
func (a *Agent) buildSiteOpts(rev *model.Revision, site *model.Site, deferRender bool) ([]*driver.Built, error) {
	cfg := revConfig(rev)
	if ns := siteOf(cfg, site.SiteSlug); ns != nil {
		site = ns // the normalized site (round D: declared volumes, no legacy data sources)
	}
	hosts := a.siteHosts(cfg)
	sm := driver.SiteMapFor(cfg, &rev.Catalog, a.dep, hosts)
	var out []*driver.Built
	for i := range site.Config.Containers {
		c := &site.Config.Containers[i]
		b, err := driver.Build(driver.BuildParams{DataDir: a.dataDir, DeploymentID: a.dep, SiteSlug: site.SiteSlug, RevID: rev.RevID, RevSeq: rev.Seq,
			Config: cfg, Catalog: &rev.Catalog, Container: c, SiteMap: sm, Hosts: hosts, DeferRender: deferRender, Names: a.cfg.ContainerNames})
		if err != nil {
			return nil, err
		}
		a.log.AddSecrets(b.Secrets...)
		out = append(out, b)
	}
	return out, nil
}

func (a *Agent) crossCheckManifests(ctx context.Context, builts []*driver.Built) error {
	if !a.hasInternet() {
		return nil
	}
	for _, b := range builts {
		if !b.Enabled || b.Image.Digest == "" { // §21 #104 local images (drift) have no registry manifest
			continue
		}
		var m model.ImageManifestResponse
		err := a.cloudJSON(ctx, "GET", "/v1/agent/images/"+b.Image.ServiceSlug+"/"+b.Image.Version+"/manifest", nil, &m, callOpts{timeout: 30 * time.Second, noRelay: true, noTunnel: true})
		if err != nil {
			continue // cross-check only when the cloud answers
		}
		if m.Digest != b.Image.Digest || m.Config.Digest != b.Image.ConfigDigest || len(m.Layers) != len(b.Image.Layers) {
			return final("catalog digest mismatch for %s:%s", b.Image.ServiceSlug, b.Image.Version)
		}
		for i := range m.Layers {
			if m.Layers[i].Digest != b.Image.Layers[i].Digest {
				return final("catalog digest mismatch for %s:%s", b.Image.ServiceSlug, b.Image.Version)
			}
		}
	}
	return nil
}

// stageBlobs downloads every missing blob (parallel, retried on network errors) and keeps progress current.
func (a *Agent) stageBlobs(ctx context.Context, revID string, rev *model.Revision, blobs []*stageBlob, detail *model.StagingDetail) error {
	var mu sync.Mutex
	var total int64
	for _, b := range blobs {
		total += max64(b.need.Size, 1)
	}
	snapshot := func() {
		mu.Lock()
		var have int64
		detail.Blobs = detail.Blobs[:0]
		detail.Layers.Have, detail.Files.Have = 0, 0
		for _, b := range blobs {
			if a.haveBlob(b.need.Hex) {
				b.state = "have"
				b.source = a.blobSource(b.need.Hex)
			}
			if b.state == "have" {
				have += max64(b.need.Size, 1)
				if b.need.Kind == "layer" {
					detail.Layers.Have++
				}
				if b.need.Kind == "file" {
					detail.Files.Have++
				}
			}
			st := b.state
			if st == "" {
				st = "waiting"
			}
			detail.Blobs = append(detail.Blobs, model.StagingBlob{Sha256: "sha256:" + b.need.Hex, Kind: b.need.Kind, SizeBytes: b.need.Size, Source: b.source, State: st})
		}
		mu.Unlock()
		prog := 0.0
		if total > 0 {
			prog = float64(have) / float64(total)
		}
		if prog >= 1 {
			prog = 0.99
		}
		a.updateOwnStaging(revID, model.SiteStaging{Staging: model.StagingDownloading, Progress: prog}, detail, false)
	}
	snapshot()
	lastReport := time.Now()
	errs := make(chan error, len(blobs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, b := range blobs {
		if a.haveBlob(b.need.Hex) {
			continue
		}
		wg.Add(1)
		go func(b *stageBlob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			backoff := a.tm.StageRetryMin
			for ctx.Err() == nil {
				err := a.ensureBlob(ctx, b.need, func(st, src string) {
					mu.Lock()
					b.state = st
					if src != "" {
						s := src
						b.source = &s
					}
					mu.Unlock()
				})
				if err == nil {
					mu.Lock()
					b.state = "have"
					mu.Unlock()
					return
				}
				if ctx.Err() != nil {
					return
				}
				var ef *errFinal
				if errors.As(err, &ef) {
					errs <- err
					return
				}
				mu.Lock()
				b.state = "failed"
				mu.Unlock()
				a.log.Warn("stager", "blob download failed (retrying)", "sha256", short(b.need.Hex), "error", err.Error(), "retryInSec", backoff.Seconds())
				if !sleepCtx(ctx, backoff) {
					return
				}
				backoff *= 2
				if backoff > a.tm.StageRetryMax {
					backoff = a.tm.StageRetryMax
				}
			}
		}(b)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			return err
		case <-done:
			snapshot()
			for _, b := range blobs {
				if !a.haveBlob(b.need.Hex) {
					return fmt.Errorf("blob %s missing", short(b.need.Hex))
				}
			}
			return nil
		case <-tick.C:
			snapshot()
			if time.Since(lastReport) > 5*time.Second {
				lastReport = time.Now()
				a.reportStagingTick(revID)
			}
		}
	}
}

// reportStagingTick sends the periodic download progress of a proposal to the cloud. §21 #104 branch objects (a merge
// on the agent branch) are not proposals: the cloud has no proposal for them, so nothing is sent (as updateOwnStaging).
func (a *Agent) reportStagingTick(revID string) {
	if a.isBranchRev(revID) {
		return
	}
	row, _ := a.staging(revID, a.site)
	pr := row.Progress
	a.queueStatus(revID, model.ProposalStatusReport{SiteSlug: a.site, Staging: model.StagingDownloading, Progress: &pr, Error: model.Null[string]()})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (a *Agent) writeNotFoundPages(cat *model.RevisionCatalog) error {
	for id, p := range cat.NotFoundPages {
		path := filepath.Join(a.dataDir, "notfound", id+".html")
		if err := os.WriteFile(path, []byte(p.HTMLCode), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// dryRun checks ports, mount sources and docker connectivity (§9.5 step 6).
func (a *Agent) dryRun(ctx context.Context, rev *model.Revision, site *model.Site, builts []*driver.Built) error {
	if err := a.dc.Ping(ctx); err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	type need struct {
		port  int
		proto string
		what  string
	}
	var needs []need
	sc := site.Config
	needs = append(needs, need{sc.AgentPort, "tcp", "agentPort"})
	if sc.AgentGuiAliasPort != nil {
		needs = append(needs, need{*sc.AgentGuiAliasPort, "tcp", "agentGuiAliasPort"})
	}
	needs = append(needs, need{sc.Edge.HTTPPort, "tcp", "edge.httpPort"})
	if sc.Edge.HTTPSPort != nil {
		needs = append(needs, need{*sc.Edge.HTTPSPort, "tcp", "edge.httpsPort"})
	}
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		for _, p := range b.Ports {
			if p.HostPort != nil {
				needs = append(needs, need{*p.HostPort, p.Protocol, b.Slug + "." + p.Name})
			}
		}
		for _, sf := range b.SiteFiles {
			if sf.Src != "" {
				if _, err := os.Stat(sf.Src); err != nil {
					return fmt.Errorf("%s: content of file %s missing", b.Slug, sf.FileSlug)
				}
			}
		}
	}
	// docker names (§21 #88): a name held by a container that this site does not manage fails staging
	if err := a.drv.CheckNames(ctx, builts); err != nil {
		return err
	}
	held := a.heldPorts(ctx)
	for _, n := range needs {
		if held[fmt.Sprintf("%s/%d", n.proto, n.port)] {
			continue
		}
		if !driver.PortFree(n.port, n.proto) {
			return fmt.Errorf("port %d/%s (%s) is used by another program on this host", n.port, n.proto, n.what)
		}
	}
	return nil
}

// heldPorts: ports this agent listens on + host ports published by OPM containers of this site.
func (a *Agent) heldPorts(ctx context.Context) map[string]bool {
	held := map[string]bool{}
	a.mu.Lock()
	for _, l := range a.listeners {
		held[fmt.Sprintf("tcp/%d", l.port)] = true
	}
	if a.edgeRunning {
		if a.edgeHTTP != nil {
			held[fmt.Sprintf("tcp/%d", *a.edgeHTTP)] = true
		}
		if a.edgeHTTPS != nil {
			held[fmt.Sprintf("tcp/%d", *a.edgeHTTPS)] = true
		}
	}
	a.mu.Unlock()
	ex, err := a.drv.Existing(ctx)
	if err == nil {
		for _, e := range ex {
			cj, err := a.dc.ContainerInspect(ctx, e.ID)
			if err != nil {
				continue
			}
			for k, binds := range cj.NetworkSettings.Ports {
				proto := "tcp"
				if strings.HasSuffix(k, "/udp") {
					proto = "udp"
				}
				for _, b := range binds {
					if b.HostPort != "" && b.HostIP != "127.0.0.1" {
						held[proto+"/"+b.HostPort] = true
					}
				}
			}
		}
	}
	return held
}

// detectedHosts: the auto address of every known site on THIS agent (SPEC §21 #51): itself = its advertiseHost
// (agent.json, else the first detected IPv4), the peers = PeerInfo.advertiseHost from the cloud view / mesh.
func (a *Agent) detectedHosts() map[string]string {
	out := map[string]string{}
	for _, p := range a.peerRows() {
		if p.AdvertiseHost != "" {
			out[p.SiteSlug] = p.AdvertiseHost
		}
	}
	if a.cfg.AdvertiseHost != "" {
		out[a.site] = a.cfg.AdvertiseHost
	}
	return out
}

// siteHosts resolves every site address (configured host, else the detected one; unknown = nil → E_NULL at use).
func (a *Agent) siteHosts(cfg *model.Config) map[string]validate.SiteHost {
	return validate.ResolveSiteHosts(cfg, a.dep, validate.HostOptions{Detected: a.detectedHosts()})
}

// validationContext is the agent-mode validation context of a revision catalog (+ the detected site addresses).
func (a *Agent) validationContext(cat *model.RevisionCatalog) validate.Context {
	v := validate.ContextFromCatalog(validate.ModeAgent, a.dep, cat)
	v.DetectedHosts = a.detectedHosts()
	return v
}

// hostOf resolves the address of a site ("" = unknown).
func (a *Agent) hostOf(cfg *model.Config, slug string) string {
	if h, ok := a.siteHosts(cfg)[slug]; ok && h.Host != nil {
		return *h.Host
	}
	return ""
}

// resolveEdge resolves the edge expressions of a site (hostnames, redirect.to, setHeaders) — edgecfg.ResolveEdge.
func (a *Agent) resolveEdge(cfg *model.Config, site *model.Site) (*model.Edge, error) {
	return edgecfg.ResolveEdge(cfg, a.dep, site, a.siteHosts(cfg))
}

// restage restarts staging on this site when it failed.
func (a *Agent) restageLocal(revID string) bool {
	row, ok := a.staging(revID, a.site)
	if ok && row.Staging != model.StagingFailed {
		return false
	}
	rev, ok2 := a.revision(revID)
	if !ok2 || !contains(a.participatingSites(rev), a.site) {
		return false
	}
	a.startStaging(revID)
	return true
}

// stagedActivity writes proposal.staged for proposals only (§21 #104: branch objects are logged in the debug log).
func (a *Agent) stagedActivity(revID, summary string, data map[string]any) {
	if a.isBranchRev(revID) {
		a.log.Info("stager", summary, "revId", revID)
		return
	}
	a.activity(nil, "system", "proposal.staged", summary, data)
}
