package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/diff"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/validate"
)

type apiFn = func(w http.ResponseWriter, r *http.Request) (any, int, *httpError)

// apiHandler is /_opm/api/* (§7).
func (a *Agent) apiHandler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, kind authKind, fn apiFn) { mux.HandleFunc(pattern, a.withAuth(kind, fn)) }
	h("GET /_opm/api/info", authPublic, a.handleInfo)
	h("GET /_opm/api/health", authPublic, a.handleHealth) // §21 #91
	h("POST /_opm/api/login", authPublic, a.handleLogin)
	h("POST /_opm/api/logout", authSession, a.handleLogout)
	h("GET /_opm/api/session", authSession, a.handleSession)
	h("GET /_opm/api/agent/remote-gui", authSession, a.handleGetRemoteGui) // §21 #118
	h("GET /_opm/api/agent/storage", authSession, a.handleStorage)         // §21 #119
	h("POST /_opm/api/agent/move-home/check", authSession, a.handleMoveHomeCheck)
	h("POST /_opm/api/agent/move-home", authActor, a.handleMoveHome)
	h("POST /_opm/api/agent/move-home/delete-old", authActor, a.handleDeleteOldHome)
	h("PUT /_opm/api/agent/remote-gui", authActor, a.handlePutRemoteGui)
	h("POST /_opm/api/actor", authSession, a.handleActor)
	h("GET /_opm/api/actors/recent", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		lim := qInt(r, "limit", 10)
		if lim <= 0 || lim > 10 {
			lim = 10
		}
		return model.RecentActorsResponse{Actors: a.actors(lim)}, 200, nil
	})
	h("GET /_opm/api/overview", authSession, a.handleOverview)
	h("GET /_opm/api/config", authSession, a.handleConfig)
	// §21 #113 site images: the images a local edit can use + resolve a docker reference / host image (siteimages.go)
	h("GET /_opm/api/images", authSession, a.handleSiteImages)
	h("POST /_opm/api/images/resolve", authActor, a.handleResolveSiteImage)
	h("GET /_opm/api/revisions", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		return model.ListResponse[model.RevisionSummary]{Items: a.listRevisions(qInt(r, "limit", 100))}, 200, nil
	})
	h("GET /_opm/api/revisions/{revId}", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		rev, ok := a.revision(r.PathValue("revId"))
		if !ok {
			return nil, 0, herr(model.ErrNotFound, "revision not found", nil)
		}
		return revisionView(rev), 200, nil
	})
	mux.HandleFunc("POST /_opm/api/secrets/{secretSlug}/reveal", noStore(a.withAuth(authActor, a.handleRevealSecret)))
	h("GET /_opm/api/files/{fileSlug}/content", authSession, a.handleFileContent)
	h("POST /_opm/api/blobs", authActor, a.handleUploadBlob)
	// §21 #107 commit staging (nothing reaches the blob store before Commit)
	h("POST /_opm/api/stages", authActor, a.handleCreateStage)
	h("PUT /_opm/api/stages/{stageId}/blobs/{sha256}", authActor, a.handlePutStageBlob)
	h("DELETE /_opm/api/stages/{stageId}", authActor, a.handleDeleteStage)
	h("POST /_opm/api/proposals/local/preview", authActor, a.legacyGone(a.handlePreviewLocal)) // §21 #104: 410 in branch mode
	h("POST /_opm/api/proposals/local", authActor, a.legacyGone(a.handleCreateLocal))
	h("GET /_opm/api/proposals", authSession, a.handleListProposals)
	h("GET /_opm/api/proposals/{revId}", authSession, a.handleGetProposal)
	h("POST /_opm/api/proposals/{revId}/restage", authActor, a.legacyGone(a.handleRestage))
	h("POST /_opm/api/proposals/{revId}/approve/start", authActor, a.legacyGone(a.handleApproveStart))
	h("POST /_opm/api/proposals/{revId}/approve", authActor, a.legacyGone(a.handleApprove))
	a.registerBranchAPI(h) // §21 #104
	h("DELETE /_opm/api/intents/{intentId}", authActor, a.handleCancelIntent)
	h("GET /_opm/api/intents/{intentId}", authSession, a.handleGetIntent)        // §21 #95
	h("POST /_opm/api/intents/{intentId}/speed", authActor, a.handleIntentSpeed) // §21 #95
	h("GET /_opm/api/updates/current", authSession, a.handleCurrentUpdate)
	h("GET /_opm/api/updates/{updateId}", authSession, a.handleGetUpdate)
	h("POST /_opm/api/updates/{updateId}/abandon", authActor, a.handleAbandon)
	h("POST /_opm/api/updates/{updateId}/terminate", authActor, a.handleTerminate) // §21 #110
	h("GET /_opm/api/snapshots", authSession, a.handleListSnapshots)
	h("POST /_opm/api/snapshots", authActor, a.handleCreateSnapshot)
	h("POST /_opm/api/snapshots/import", authActor, a.handleImportSnapshot)
	h("GET /_opm/api/snapshots/{snapshotId}", authSession, a.handleGetSnapshot)
	h("PATCH /_opm/api/snapshots/{snapshotId}", authActor, a.handlePatchSnapshot)
	h("DELETE /_opm/api/snapshots/{snapshotId}", authActor, a.handleDeleteSnapshot)
	h("GET /_opm/api/snapshots/{snapshotId}/download", authSession, a.handleDownloadSnapshot)
	h("GET /_opm/api/snapshots/{snapshotId}/plan", authSession, a.handleRollbackPlan)
	h("POST /_opm/api/snapshots/{snapshotId}/rollback/start", authActor, a.handleRollbackStart)
	h("POST /_opm/api/snapshots/{snapshotId}/rollback", authActor, a.handleRollback)
	h("GET /_opm/api/certs", authSession, a.handleListCerts)
	h("GET /_opm/api/certs/local-ca.crt", authPublic, a.handleLocalCAAny)
	h("POST /_opm/api/certs/{hostname}/renew", authActor, a.handleRenewCertAny)
	h("GET /_opm/api/tunnel", authSession, a.handleTunnelState)
	h("POST /_opm/api/tunnel/start", authActor, a.handleTunnelStart)
	h("POST /_opm/api/tunnel/stop", authActor, a.handleTunnelStop)
	h("POST /_opm/api/tunnel/revoke", authActor, a.handleTunnelRevoke)
	h("GET /_opm/api/tunnel/next", authActor, a.handleTunnelNext)
	h("POST /_opm/api/tunnel/result", authActor, a.handleTunnelResult)
	h("POST /_opm/api/tunnel/result/{reqId}/chunk", authActor, a.handleTunnelChunk)
	h("POST /_opm/api/tunnel/result/{reqId}/complete", authActor, a.handleTunnelComplete)
	// §21 #90 mesh "Upgrade all" (selfmesh.go)
	h("GET /_opm/api/agent/upgrade-all", authSession, a.handleUpgradeAllState)
	h("POST /_opm/api/agent/upgrade-all", authActor, a.handleStartUpgradeAll)
	h("POST /_opm/api/agent/upgrade-all/cancel", authActor, a.handleCancelUpgradeAll)
	// §21 #89 operator toolbox (tools_hook.go): self / "*" / forwarded incl. SSE + the terminal WebSocket
	mux.HandleFunc("/_opm/api/sites/{siteSlug}/tools/{rest...}", a.handleToolsRoute)
	mux.HandleFunc("/_opm/api/sites/{siteSlug}/devtools/{rest...}", a.handleDevtoolsRoute) // §21 #97 dev tools (devtools_hook.go)
	mux.HandleFunc("/_opm/api/sites/{siteSlug}/{rest...}", a.handleSiteRoute)
	mux.HandleFunc("/_opm/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, herr(model.ErrNotFound, "no such API endpoint: "+r.Method+" "+r.URL.Path, nil))
	})
	return mux
}

// siteMux holds the per-site handlers (local requests and /_opm/peer/site/<rest>).
func (a *Agent) siteMux() http.Handler {
	a.siteOnce.Do(func() { a.siteH = a.buildSiteMux() })
	return a.siteH
}

func (a *Agent) buildSiteMux() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, kind authKind, fn apiFn) { mux.HandleFunc(pattern, a.withAuth(kind, fn)) }
	h("GET /status", authSession, a.handleSiteStatus)
	h("GET /status-cache", authSession, a.handleSiteStatus)
	for _, part := range []string{"overview", "containers", "uptime", "edge"} {
		h("GET /status/"+part, authSession, a.handleStatusPart(part))
	}
	h("GET /volumes", authSession, a.handleSiteVolumes)
	h("POST /volumes/{volumeSlug}/reset/start", authActor, a.handleResetVolumeStart)
	h("POST /volumes/{volumeSlug}/reset", authActor, a.handleResetVolume)
	// §21 #96 volume manager (volops.go)
	h("GET /volume-ops", authSession, a.handleListVolumeOps)
	h("GET /volume-ops/{opId}", authSession, a.handleGetVolumeOp)
	h("POST /volumes/{volumeSlug}/empty/start", authActor, a.volumeIntentStart("volume-empty"))
	h("POST /volumes/{volumeSlug}/empty", authActor, a.handleEmptyVolume)
	h("POST /volumes/{volumeSlug}/copy", authActor, a.handleCopyVolume)
	h("POST /volumes/{volumeSlug}/restore/upload", authActor, a.handleRestoreUpload)
	h("POST /volumes/{volumeSlug}/restore/start", authActor, a.volumeIntentStart("volume-restore"))
	h("POST /volumes/{volumeSlug}/restore", authActor, a.handleRestoreVolume)
	h("POST /volumes/{volumeSlug}/snapshot", authActor, a.handleSnapshotVolume)
	h("GET /volumes/{volumeSlug}/backups", authSession, a.handleVolumeBackups) // §21 #112
	h("GET /agent/process", authSession, a.handleAgentProcess)                 // §21 #112 self-metrics
	// §21 #121 every site is managed from any site: the agent page's cards of another site come over the mesh
	h("GET /agent/health", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		h := a.health(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		return h, 200, nil
	})
	h("GET /agent/storage", authSession, a.handleStorage)
	h("POST /agent/move-home/check", authActor, a.handleMoveHomeCheck)
	h("POST /agent/move-home", authActor, a.handleMoveHome)
	h("POST /agent/move-home/delete-old", authActor, a.handleDeleteOldHome)
	h("GET /agent/remote-gui", authSession, a.handleGetRemoteGui)
	h("PUT /agent/remote-gui", authActor, a.handlePutRemoteGui)
	h("GET /volume-copies", authSession, a.handleListVolumeCopies)
	h("POST /volume-copies/{copyId}/delete", authActor, a.handleDeleteVolumeCopy)
	h("GET /intents/{intentId}", authSession, a.handleGetIntent) // §21 #95 (intents held by this site, e.g. a forwarded volume reset)
	h("POST /intents/{intentId}/speed", authActor, a.handleIntentSpeed)
	h("DELETE /intents/{intentId}", authActor, a.handleCancelIntent)
	h("GET /containers/{containerSlug}/logs", authSession, a.handleContainerLogs)
	h("GET /activities", authSession, a.handleSiteActivities)
	h("GET /activities/export", authSession, a.handleActivitiesExport)
	h("GET /activities/download", authActor, a.handleDownloadActivities)
	h("GET /debug", authSession, a.handleSiteDebug)
	mux.Handle("/tools/", a.toolsSiteHandler())       // §21 #89 operator toolbox (tools_hook.go)
	mux.Handle("/devtools/", a.devtoolsSiteHandler()) // §21 #97 dev tools (devtools_hook.go)
	a.registerSelfRoutes(h)                           // §21 #90 agent self-management (self.go)
	a.registerVolumeSyncRoutes(h)                     // §21 #101 volume content sync (vcapply.go)
	a.registerFileRoutes(h)                           // §21 #123 shared files (files.go)
	a.registerSiteBranchAPI(h)                        // §21 #111 per-site branch + its runs (branch.go)
	h("GET /transfers", authSession, a.handleListTransfers)
	h("POST /transfers/upload", authActor, a.handleSendFromBrowser)
	h("POST /transfers/from-path", authActor, a.handleSendFromPath)
	h("GET /transfers/fs", authSession, a.handleHostFs) // §21 #100 server filesystem picker (hostfs.go)
	h("POST /transfers/{transferId}/save", authActor, a.handleSaveTransfer)
	h("GET /transfers/{transferId}/download", authSession, a.handleDownloadTransfer)
	h("GET /proposals/{revId}/staging", authSession, a.handleStagingDetail)
	h("POST /proposals/{revId}/restage", authActor, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		return map[string]bool{"restarted": a.restageLocal(r.PathValue("revId"))}, 200, nil
	})
	h("GET /tunnel-state", authSession, a.handleTunnelState)
	h("POST /tunnel-revoke", authActor, a.handleTunnelRevoke)
	h("GET /certs", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		return model.ListResponse[model.CertStatus]{Items: a.certStatuses()}, 200, nil
	})
	h("POST /certs/{hostname}/renew", authActor, a.handleRenewCert)
	h("GET /local-ca.crt", authPublic, a.handleLocalCA)
	h("GET /snapshot-doc/{snapshotId}", authSession, func(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
		if getRC(r).peer == nil { // plain secrets: mesh only
			return nil, 0, herr(model.ErrNotFound, "no such site endpoint", nil)
		}
		p, err := a.loadPartAny(r.PathValue("snapshotId"))
		if err != nil {
			return nil, 0, herr(model.ErrNotFound, "snapshot not on this site", nil)
		}
		return model.SnapshotDetailResponse{Snapshot: p.snap}, 200, nil
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, herr(model.ErrNotFound, "no such site endpoint: "+r.URL.Path, nil))
	})
	return mux
}

// handleSiteRoute dispatches /_opm/api/sites/{siteSlug}/{rest...}: self → local; "*" (GET) → merged; other → forwarded.
func (a *Agent) handleSiteRoute(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("siteSlug")
	rest := r.PathValue("rest")
	rc := getRC(r)
	if slug == a.site {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		r2.URL.RawPath = ""
		a.siteMux().ServeHTTP(w, r2)
		return
	}
	if e := a.checkSession(r, rc); e != nil {
		writeErr(w, e)
		return
	}
	if slug == "*" {
		if r.Method != http.MethodGet {
			writeErr(w, herr(model.ErrInvalid, `"*" is only for GET`, nil))
			return
		}
		a.handleMerged(w, r, rest)
		return
	}
	if !contains(a.enrolledSites(), slug) {
		cfg, _ := a.headConfig()
		if siteOf(cfg, slug) == nil {
			writeErr(w, herr(model.ErrNotFound, "site "+slug+" does not exist", nil))
			return
		}
	}
	if !a.reachable(slug) {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+" is not reachable", nil))
		return
	}
	// §21 #121 through the cloud no site's remote GUI is switched (the target cannot see that the person is remote)
	if r.Method == http.MethodPut && strings.HasPrefix(rest, "agent/remote-gui") && remoteOf(r) != nil {
		writeErr(w, herr(model.ErrForbidden, "switch the remote GUI on / off on a site itself, not through the cloud", nil))
		return
	}
	// actor-required forwards: check the actor here (the target trusts the signed headers)
	if r.Method != http.MethodGet || strings.HasSuffix(rest, "activities/download") {
		if e := a.checkActor(r, rc); e != nil {
			writeErr(w, e)
			return
		}
	}
	if r.Method != http.MethodGet && (strings.HasPrefix(rest, "volumes/") || strings.HasPrefix(rest, "volume-copies/") || strings.HasPrefix(rest, "volume-sync/")) {
		a.forwardVolumeAction(w, r, slug, rest)
		return
	}
	a.forwardSite(w, r, slug, rest)
}

// statusWriter remembers the status of a forwarded answer.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) {
	s.status = c
	s.ResponseWriter.WriteHeader(c)
}

// forwardVolumeAction forwards a volume write (§21 #96) and logs it on THIS site too (the target logs the action).
func (a *Agent) forwardVolumeAction(w http.ResponseWriter, r *http.Request, slug, rest string) {
	sw := &statusWriter{ResponseWriter: w, status: 200}
	a.forwardSite(sw, r, slug, rest)
	rc := getRC(r)
	action := rest
	if i := strings.Index(action, "?"); i >= 0 {
		action = action[:i]
	}
	a.activity(rc.actorPtr(), "agent", "volume.action-forwarded", trim200(fmt.Sprintf("%s %s forwarded to %s → %d", r.Method, action, slug, sw.status)),
		actorData(rc, map[string]any{"siteSlug": slug, "forwardedTo": slug, "method": r.Method, "action": action, "status": sw.status}))
}

// forwardSite proxies a per-site request over the mesh (body streamed through a temp file for raw uploads).
func (a *Agent) forwardSite(w http.ResponseWriter, r *http.Request, slug, rest string) {
	rc := getRC(r)
	path := "/_opm/peer/site/" + rest
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	hdr := map[string]string{}
	if rc.actor != "" {
		hdr["X-OPM-Actor"] = rc.actor
	}
	if rc.sessionID != "" { // also on GETs without an actor (§21 #95 siteIntent reads a session-owned intent)
		hdr["X-OPM-Session"] = rc.sessionID + "@" + a.site
	}
	if v := r.Header.Get("Range"); v != "" {
		hdr["Range"] = v
	}
	var resp *http.Response
	var done func()
	var err error
	if r.Method == http.MethodGet {
		resp, done, err = a.peerStream(r.Context(), slug, "GET", path, nil, hdr, 30*time.Minute)
	} else {
		tmp, terr := os.CreateTemp(a.dataDir+"/tmp", "fwd-*")
		if terr != nil {
			writeErr(w, herr(model.ErrInternal, terr.Error(), nil))
			return
		}
		_, cerr := io.Copy(tmp, io.LimitReader(r.Body, 2<<30+1))
		tmp.Close()
		defer os.Remove(tmp.Name())
		if cerr != nil {
			writeErr(w, herr(model.ErrInvalid, cerr.Error(), nil))
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			hdr["Content-Type"] = ct
		}
		resp, done, err = a.peerUploadFile(r.Context(), slug, r.Method, path, tmp.Name(), hdr, 30*time.Minute)
	}
	if err != nil {
		writeErr(w, herr(model.ErrUnavailable, "site "+slug+": "+err.Error(), nil))
		return
	}
	defer done()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Content-Range", "Accept-Ranges", "X-OPM-Warning", "Retry-After"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// fetchSiteJSON reads a per-site GET (self in process, peer over the mesh).
func (a *Agent) fetchSiteJSON(ctx context.Context, r *http.Request, slug, rest string, out any) error {
	if slug == a.site {
		rec := httptest.NewRecorder()
		u, _ := url.Parse("/" + rest)
		r2 := r.Clone(ctx)
		r2.Method = http.MethodGet
		r2.URL = u
		r2.Body = http.NoBody
		a.siteMux().ServeHTTP(rec, r2)
		if rec.Code >= 300 {
			return fmt.Errorf("status %d", rec.Code)
		}
		return json.Unmarshal(rec.Body.Bytes(), out)
	}
	resp, done, err := a.peerStream(ctx, slug, "GET", "/_opm/peer/site/"+rest, nil, nil, 30*time.Second)
	if err != nil {
		return err
	}
	defer done()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("site %s: status %d", slug, resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// allSites = head config sites ∪ enrolled peers ∪ self.
func (a *Agent) allSites() []string {
	cfg, _ := a.headConfig()
	return union(union(siteSlugs(cfg), a.enrolledSites()), []string{a.site})
}

func (a *Agent) handleMerged(w http.ResponseWriter, r *http.Request, rest string) {
	base := strings.SplitN(rest, "?", 2)[0]
	q := r.URL.RawQuery
	withQ := func(p string) string {
		if q != "" {
			return p + "?" + q
		}
		return p
	}
	switch base {
	case "status", "status/overview", "status/containers", "status/uptime", "status/edge":
		// every site at once (one slow site no longer holds up the others); the full status is remembered for
		// sites that stop answering, a part only fills in what the cache does not have
		sites := a.allSites()
		items := make([]model.SiteStatus, len(sites))
		var wg sync.WaitGroup
		for i, s := range sites {
			if s != a.site && !a.reachable(s) {
				items[i] = a.cachedStatus(s)
				continue
			}
			wg.Add(1)
			go func(i int, s string) {
				defer wg.Done()
				var lr model.ListResponse[model.SiteStatus]
				if err := a.fetchSiteJSON(r.Context(), r, s, withQ(base), &lr); err != nil || len(lr.Items) == 0 {
					items[i] = a.cachedStatus(s)
					return
				}
				if base == "status" {
					a.rememberStatus(s, lr.Items[0])
				}
				items[i] = lr.Items[0]
			}(i, s)
		}
		wg.Wait()
		writeJSON(w, 200, model.ListResponse[model.SiteStatus]{Items: items})
	case "activities":
		lim := qInt(r, "limit", 100)
		if lim <= 0 || lim > 1000 {
			lim = 100
		}
		cur := r.URL.Query().Get("cursor") // "<ts>|<site>|<seq>"
		var all []model.Activity
		for _, s := range a.allSites() {
			if s != a.site && !a.reachable(s) {
				continue
			}
			vals := url.Values{"limit": {fmt.Sprint(lim * 2)}}
			if t := r.URL.Query().Get("type"); t != "" {
				vals.Set("type", t)
			}
			if t := r.URL.Query().Get("actor"); t != "" {
				vals.Set("actor", t)
			}
			var pr model.PageResponse[model.Activity]
			if err := a.fetchSiteJSON(r.Context(), r, s, "activities?"+vals.Encode(), &pr); err == nil {
				all = append(all, pr.Items...)
			}
		}
		key := func(x model.Activity) string { return fmt.Sprintf("%s|%s|%010d", x.Ts, x.SiteSlug, x.Seq) }
		sort.Slice(all, func(i, j int) bool { return key(all[i]) > key(all[j]) })
		out := []model.Activity{}
		for _, x := range all {
			if cur != "" && key(x) >= cur {
				continue
			}
			out = append(out, x)
			if len(out) == lim {
				break
			}
		}
		var next *string
		if len(out) == lim {
			k := key(out[len(out)-1])
			next = &k
		}
		writeJSON(w, 200, model.PageResponse[model.Activity]{Items: out, Cursor: next})
	case "debug":
		var all []model.DebugLine
		for _, s := range a.allSites() {
			if s != a.site && !a.reachable(s) {
				continue
			}
			var lr model.ListResponse[model.DebugLine]
			if err := a.fetchSiteJSON(r.Context(), r, s, withQ("debug"), &lr); err == nil {
				for _, l := range lr.Items {
					l.Site = s
					all = append(all, l)
				}
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Ts > all[j].Ts })
		lim := qInt(r, "limit", 500)
		if lim <= 0 || lim > 5000 {
			lim = 500
		}
		if len(all) > lim {
			all = all[:lim]
		}
		if all == nil {
			all = []model.DebugLine{}
		}
		writeJSON(w, 200, model.ListResponse[model.DebugLine]{Items: all})
	case "activities/download":
		if e := a.checkActor(r, getRC(r)); e != nil {
			writeErr(w, e)
			return
		}
		r2 := r.WithContext(context.WithValue(r.Context(), allSitesKey, true))
		r2.URL.Path = "/activities/download"
		a.siteMux().ServeHTTP(w, r2)
	case "agent": // §21 #90: one AgentSelfInfo per reachable site
		items := []model.AgentSelfInfo{}
		for _, s := range a.allSites() {
			if s != a.site && !a.reachable(s) {
				continue
			}
			var info model.AgentSelfInfo
			if err := a.fetchSiteJSON(r.Context(), r, s, "agent", &info); err == nil {
				info.Self = info.SiteSlug == a.site
				items = append(items, info)
			}
		}
		writeJSON(w, 200, model.ListResponse[model.AgentSelfInfo]{Items: items})
	case "volumes": // round D: per site only (the GUI calls it once per site)
		writeErr(w, herr(model.ErrNotFound, `"*" is not supported for volumes: ask one site`, nil))
	default:
		writeErr(w, herr(model.ErrInvalid, `"*" is supported for status, activities, debug and agent`, nil))
	}
}

// ───────────────────────── simple deployment-wide handlers

func (a *Agent) handleInfo(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	hash, kv := a.keyHash()
	ci := a.consoleInfo()
	if ci.ConsoleURL == nil && a.cfg.ConsoleURL != "" {
		u := a.cfg.ConsoleURL
		ci.ConsoleURL = &u
	}
	api := a.cfg.APIBase
	return model.AgentInfoResponse{DeploymentID: a.dep, SiteSlug: a.site, AgentVersion: a.version, Enrolled: a.enrolled, HasAccessKey: hash != "",
		KeyVersion: kv, ServerTime: nowTs(), Surface: getRC(r).surface, ConsoleURL: ci.ConsoleURL, APIBase: &api, DeploymentLabel: ci.DeploymentLabel,
		Schema: store.SchemaVersion}, 200, nil
}

// consoleInfoKV: the console origin + deployment label last sent by the cloud (enroll answer / cloud view).
type consoleInfoKV struct {
	ConsoleURL      *string `json:"consoleUrl"`
	DeploymentLabel *string `json:"deploymentLabel"`
}

func (a *Agent) consoleInfo() consoleInfoKV {
	var c consoleInfoKV
	_, _ = a.db.GetKV("consoleInfo", &c)
	return c
}

// storeConsoleInfo keeps the values the cloud sent (nil = not sent → the stored value stays).
func (a *Agent) storeConsoleInfo(consoleURL, label *string) {
	if consoleURL == nil && label == nil {
		return
	}
	c := a.consoleInfo()
	changed := false
	if consoleURL != nil && (c.ConsoleURL == nil || *c.ConsoleURL != *consoleURL) {
		c.ConsoleURL, changed = consoleURL, true
	}
	if label != nil && (c.DeploymentLabel == nil || *c.DeploymentLabel != *label) {
		c.DeploymentLabel, changed = label, true
	}
	if changed {
		_ = a.db.SetKV("consoleInfo", c)
	}
}

func (a *Agent) handleOverview(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	cfg, _ := a.headConfig()
	out := model.OverviewResponse{DeploymentID: a.dep, Self: a.site, ServerTime: nowTs(), Sites: []model.SiteTile{}, Links: map[string]map[string]any{}, Deleted: a.isDeleted()}
	if hr := a.headRevision(); hr != nil {
		out.Head = &model.OverviewHead{RevID: hr.RevID, Seq: hr.Seq, Hash: hr.Hash, Summary: hr.Summary, CreatedAt: hr.CreatedAt}
	}
	route := a.route()
	var lastOk *string
	var lc string
	if ok, _ := a.db.GetKV("lastCloudOkAt", &lc); ok {
		lastOk = &lc
	}
	out.Cloud = model.OverviewCloud{Route: route, LastOkAt: lastOk}
	if v := a.cloudView(); v != nil {
		f, s := v.FetchedAt, v.Source
		out.Cloud.FetchedAt, out.Cloud.Source = &f, &s
	}
	enrolled := map[string]*peerRow{}
	for _, p := range a.peerRows() {
		pp := p
		enrolled[p.SiteSlug] = &pp
	}
	var newestOpen *model.Proposal
	for _, p := range a.openProposals() {
		pp := p
		if newestOpen == nil || pp.RevID > newestOpen.RevID {
			newestOpen = &pp
		}
	}
	for _, slug := range a.allSites() {
		t := model.SiteTile{SiteSlug: slug, Self: slug == a.site, Enrolled: enrolled[slug] != nil || (slug == a.site && a.enrolled), Reachable: a.reachable(slug)}
		if s := siteOf(cfg, slug); s != nil {
			lbl := s.Label
			t.Label = &lbl
			t.InConfig = true
			// configured / expression host; null for an auto site (the GUI shows advertiseHost then)
			if h, ok := a.siteHosts(cfg)[slug]; ok && !h.Auto && h.Host != nil {
				hv := *h.Host
				t.Host = &hv
			}
		}
		if pr := enrolled[slug]; pr != nil {
			ah, ap := pr.AdvertiseHost, pr.AgentPort
			t.AdvertiseHost, t.AgentPort = &ah, &ap
			if pr.LastAnswerAt > 0 {
				ls := tsOf(time.Unix(pr.LastAnswerAt, 0))
				t.LastSeenAt = &ls
			}
		}
		pw := a.lastPing(slug)
		if slug == a.site {
			ls := nowTs()
			t.LastSeenAt = &ls
			ah, ap := a.cfg.AdvertiseHost, a.currentAgentPort()
			t.AdvertiseHost, t.AgentPort = &ah, &ap
		}
		if pw != nil {
			v := pw.AgentVersion
			t.AgentVersion = &v
			if pw.Head != nil {
				t.Head = &model.RevRef{RevID: pw.Head.RevID, Seq: pw.Head.Seq}
			}
			cr := pw.CloudRoute
			t.CloudRoute = &cr
			t.Containers = pw.Containers
			t.Lock = pw.Lock
			if pw.Lock != nil && out.Lock == nil {
				out.Lock = pw.Lock
			}
			if newestOpen != nil {
				for _, ps := range pw.Proposals {
					if ps.RevID == newestOpen.RevID {
						t.Staging = &model.SiteTileStaging{RevID: ps.RevID, Staging: ps.Staging}
					}
				}
			}
			if t.Label == nil && pw.Label != nil {
				t.Label = pw.Label
			}
			if t.Host == nil && pw.Host != nil {
				t.Host = pw.Host
			}
			row := map[string]any{}
			for k, v := range pw.Links {
				if v == nil {
					row[k] = nil
				} else {
					row[k] = *v
				}
			}
			row[slug] = "self"
			out.Links[slug] = row
		}
		out.Sites = append(out.Sites, t)
	}
	if l := a.lock(); l != nil {
		out.Lock = l
	}
	return out, 200, nil
}

func (a *Agent) handleConfig(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	hr := a.headRevision()
	if hr == nil {
		c := validate.EmptyConfig()
		return model.AgentConfigResponse{Head: nil, Config: maskConfig(&c), Catalog: nil}, 200, nil
	}
	cat := hr.Catalog
	known := a.knownCatalog(hr)
	// normalized (round D); §21 #105: no `working` (GUI edits live in the browser)
	return model.AgentConfigResponse{Head: a.head(), Config: maskConfig(revConfig(hr)), Catalog: &cat, KnownCatalog: &known}, 200, nil
}

// noStore marks a value-returning endpoint: Cache-Control no-store on success and on every error (SPEC §21 #84 c/e).
func noStore(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

// revealWindow counts value-returning requests of one session in one fixed minute.
type revealWindow struct {
	minute int64 // unix minute
	n      int
}

// revealAllowed applies REVEAL_RATE_PER_MINUTE per session per fixed minute (in memory; SPEC §21 #84 e).
func (a *Agent) revealAllowed(key string) *httpError {
	now := a.clock()
	min := now.Unix() / 60
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, w := range a.revealN { // drop old windows
		if w.minute != min {
			delete(a.revealN, k)
		}
	}
	w := a.revealN[key]
	if w.minute != min {
		w = revealWindow{minute: min}
	}
	if w.n >= model.RevealRatePerMinute {
		retry := (min+1)*60 - now.Unix()
		if retry < 1 {
			retry = 1
		}
		e := herr(model.ErrRateLimited, fmt.Sprintf("too many reveals; try again in %d s", retry), map[string]any{"retryAfterSec": retry})
		e.header = map[string]string{"Retry-After": strconv.FormatInt(retry, 10)}
		return e
	}
	w.n++
	a.revealN[key] = w
	return nil
}

func (a *Agent) handleRevealSecret(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	slug := r.PathValue("secretSlug")
	key := rc.sessionID
	if key == "" && rc.peer != nil {
		key = "peer:" + rc.actor
	}
	if e := a.revealAllowed(key); e != nil {
		return nil, 0, e
	}
	hr := a.headRevision()
	if hr == nil {
		return nil, 0, herr(model.ErrNotFound, "no head", nil)
	}
	for _, s := range hr.Config.Secrets {
		if s.SecretSlug == slug {
			if s.Value == nil {
				return nil, 0, herr(model.ErrNotFound, "secret "+slug+" has no value", nil)
			}
			a.activity(rc.actorPtr(), "agent", "secret.revealed", "secret "+slug+" revealed", actorData(rc, map[string]any{"secretSlug": slug, "actor": rc.actor, "where": "agent", "siteSlug": a.site}))
			return model.RevealSecretResponse{Value: *s.Value, HideAfterSec: model.RevealSeconds}, 200, nil
		}
	}
	return nil, 0, herr(model.ErrNotFound, "secret "+slug+" does not exist", nil)
}

func (a *Agent) handleFileContent(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	slug := r.PathValue("fileSlug")
	hr := a.headRevision()
	if hr == nil {
		return nil, 0, herr(model.ErrNotFound, "no head", nil)
	}
	for _, f := range hr.Config.Files {
		if f.FileSlug == slug {
			if f.Content == nil {
				return nil, 0, herr(model.ErrNotFound, "file "+slug+" has no content", nil)
			}
			p, ok := a.blobPath(hexOf(f.Content.Sha256))
			if !ok {
				return nil, 0, herr(model.ErrNotFound, "file content is not on this site", nil)
			}
			fh, err := os.Open(p)
			if err != nil {
				return nil, 0, herr(model.ErrNotFound, err.Error(), nil)
			}
			defer fh.Close()
			st, _ := fh.Stat()
			w.Header().Set("Content-Type", f.Mime)
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", slug))
			http.ServeContent(w, r, "", st.ModTime(), fh)
			return nil, 0, nil
		}
	}
	return nil, 0, herr(model.ErrNotFound, "file "+slug+" does not exist", nil)
}

func (a *Agent) handleUploadBlob(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 50<<20+1))
	if err != nil {
		return nil, 0, herr(model.ErrInvalid, err.Error(), nil)
	}
	if len(data) > 50<<20 {
		return nil, 0, herr(model.ErrTooLarge, "at most 50 MiB", nil)
	}
	h, err := a.storeBlobBytes(data, "file", "local")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	return model.BlobRef{Sha256: "sha256:" + h, SizeBytes: int64(len(data))}, 201, nil
}

// ───────────────────────── proposals (list / detail)

func (a *Agent) handleListProposals(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	head := a.head()
	var open, term []model.ProposalListItem
	for _, p := range a.proposals() {
		item := model.ProposalListItem{Proposal: p, IsHeadParent: (head == nil && p.ParentRevID == nil) || (head != nil && p.ParentRevID != nil && *p.ParentRevID == head.RevID)}
		if model.IsTerminal(p.Status) {
			term = append(term, item)
		} else {
			open = append(open, item)
		}
	}
	lim := qInt(r, "limit", 20)
	if lim <= 0 {
		lim = 20
	}
	if len(term) > lim {
		term = term[:lim]
	}
	items := append(append([]model.ProposalListItem{}, open...), term...)
	return model.ListResponse[model.ProposalListItem]{Items: items}, 200, nil
}

func (a *Agent) handleGetProposal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("revId")
	p, ok := a.proposal(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "proposal not found", nil)
	}
	rev, ok := a.revision(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "revision not found", nil)
	}
	var base *model.Config
	slugOf := svcSlugs(&rev.Catalog)
	if rev.ParentRevID != nil {
		if pr, ok := a.revision(*rev.ParentRevID); ok {
			base = revConfig(pr)
			for k, v := range svcSlugs(&pr.Catalog) {
				if _, ok := slugOf[k]; !ok {
					slugOf[k] = v
				}
			}
		}
	}
	changes := diff.Configs(base, revConfig(rev), func(id string) string {
		if s, ok := slugOf[id]; ok {
			return s
		}
		return id
	})
	staging := []model.StagingDetail{}
	for _, s := range a.participatingSites(rev) {
		staging = append(staging, a.stagingDetailOf(r.Context(), r, id, s))
	}
	return model.ProposalDetailResponse{Proposal: *p, Revision: revisionView(rev), Changes: changes, Staging: staging, Gate: a.gateForProposal(p, rev)}, 200, nil
}

func (a *Agent) stagingDetailOf(ctx context.Context, r *http.Request, revID, site string) model.StagingDetail {
	empty := model.StagingDetail{SiteSlug: site, Staging: model.SiteStaging{Staging: model.StagingPending}, Blobs: []model.StagingBlob{}, Issues: []model.ValidationIssue{}}
	if site == a.site {
		if d := a.localStagingDetail(revID); d != nil {
			return *d
		}
		return empty
	}
	if row, ok := a.staging(revID, site); ok {
		empty.Staging = row.SiteStaging
	}
	if a.reachable(site) {
		var d model.StagingDetail
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := a.fetchSiteJSON(cctx, r, site, "proposals/"+revID+"/staging", &d); err == nil {
			return d
		}
	}
	return empty
}

func (a *Agent) localStagingDetail(revID string) *model.StagingDetail {
	a.mu.Lock()
	d := a.details[revID]
	a.mu.Unlock()
	row, ok := a.staging(revID, a.site)
	if d == nil && ok && row.Detail != nil {
		d = row.Detail
	}
	if d == nil {
		if !ok {
			return nil
		}
		d = &model.StagingDetail{SiteSlug: a.site, Blobs: []model.StagingBlob{}, Issues: []model.ValidationIssue{}}
	}
	cp := *d
	if ok {
		cp.Staging = row.SiteStaging
	}
	if cp.Blobs == nil {
		cp.Blobs = []model.StagingBlob{}
	}
	if cp.Issues == nil {
		cp.Issues = []model.ValidationIssue{}
	}
	return &cp
}

func (a *Agent) handleStagingDetail(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	d := a.localStagingDetail(r.PathValue("revId"))
	if d == nil {
		return model.StagingDetail{SiteSlug: a.site, Staging: model.SiteStaging{Staging: model.StagingPending}, Blobs: []model.StagingBlob{}, Issues: []model.ValidationIssue{}}, 200, nil
	}
	return d, 200, nil
}

func (a *Agent) handleRestage(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("revId")
	p, ok := a.proposal(id)
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "proposal not found", nil)
	}
	if model.IsTerminal(p.Status) {
		return nil, 0, herr(model.ErrConflict, "the proposal is "+p.Status, nil)
	}
	rc := getRC(r)
	for s, st := range p.Sites {
		if st.Staging != model.StagingFailed {
			continue
		}
		if s == a.site {
			a.restageLocal(id)
			continue
		}
		if a.reachable(s) {
			hdr := map[string]string{"X-OPM-Actor": rc.actor, "X-OPM-Session": rc.sessionID + "@" + a.site}
			resp, done, err := a.peerStream(r.Context(), s, "POST", "/_opm/peer/site/proposals/"+id+"/restage", nil, hdr, 15*time.Second)
			if err == nil {
				done()
				_ = resp
			}
		}
	}
	return model.OkResponse{Ok: true}, 200, nil
}

var _ = bytes.NewReader
