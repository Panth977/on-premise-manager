package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/diff"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

func generateSecret(g *model.SecretGenerate) string {
	cs := map[string]string{
		"alnum":     "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
		"hex":       "0123456789abcdef",
		"base64url": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
	}[g.Charset]
	if cs == "" {
		cs = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	}
	n := g.Length
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(cs))))
		b[i] = cs[x.Int64()]
	}
	return string(b)
}

// buildLocalConfig turns a ConfigInput into a Config against the head (secrets: plainValue semantics).
func buildLocalConfig(head *model.Config, in model.ConfigInput) model.Config {
	out := model.Config{Variables: in.Variables, Files: in.Files, Sites: in.Sites, Secrets: []model.SecretDef{}}
	hs := map[string]model.SecretDef{}
	for _, s := range head.Secrets {
		hs[s.SecretSlug] = s
	}
	now := nowTs()
	for _, si := range in.Secrets {
		old := hs[si.SecretSlug]
		d := model.SecretDef{SecretSlug: si.SecretSlug, Label: si.Label, Description: si.Description, Required: si.Required, Generate: si.Generate, Value: old.Value, RotatedAt: old.RotatedAt}
		switch {
		case si.GenerateNow != nil && *si.GenerateNow && si.Generate != nil:
			v := generateSecret(si.Generate)
			d.Value, d.RotatedAt = &v, &now
		case si.PlainValue.Set && si.PlainValue.Value == nil:
			if old.Value != nil {
				d.RotatedAt = &now
			}
			d.Value = nil
		case si.PlainValue.Set:
			v := *si.PlainValue.Value
			if old.Value == nil || *old.Value != v {
				d.RotatedAt = &now
			}
			d.Value = &v
		}
		out.Secrets = append(out.Secrets, d)
	}
	normalizeConfig(&out)
	for i := range out.Sites {
		sc := &out.Sites[i].Config
		if sc.Volumes == nil { // an input without the site volumes keeps the head's declared volumes (round D)
			vols := []model.SiteVolume{}
			if hs := siteOf(head, out.Sites[i].SiteSlug); hs != nil {
				vols = append(vols, hs.Config.VolumeList()...)
			}
			sc.Volumes = &vols
		}
		if sc.DevTools == nil { // §21 #97 c: presets are cloud configuration; an input without them keeps the head's
			if hs := siteOf(head, out.Sites[i].SiteSlug); hs != nil {
				sc.DevTools = hs.Config.DevTools
			}
		} else if hs := siteOf(head, out.Sites[i].SiteSlug); hs != nil {
			sc.DevTools = unmaskPresetLiterals(*sc.DevTools, hs.Config.DevToolList())
		}
		sc.NormalizeDevTools() // never an empty list in a revision (§21 #97 a)
		if sc.Containers == nil {
			sc.Containers = []model.Container{}
		}
		if sc.Depends == nil {
			sc.Depends = []model.SiteRef{}
		}
		if sc.Edge.Hosts == nil {
			sc.Edge.Hosts = []model.EdgeHost{}
		}
		if sc.Edge.Routes == nil {
			sc.Edge.Routes = []model.EdgeRoute{}
		}
		for j := range sc.Containers {
			c := &sc.Containers[j]
			if c.Env == nil {
				c.Env = []model.ContainerEnv{}
			}
			if c.Volumes == nil {
				c.Volumes = []model.ContainerMount{}
			}
			if c.Ports == nil {
				c.Ports = []model.ContainerPort{}
			}
			if c.Depends == nil {
				c.Depends = []model.ContainerRef{}
			}
		}
		for j := range sc.Edge.Routes {
			if sc.Edge.Routes[j].Hosts == nil {
				sc.Edge.Routes[j].Hosts = []string{}
			}
		}
	}
	return out
}

func changedSections(ch []model.DiffChange) []string {
	m := map[string]bool{}
	for _, c := range ch {
		m[c.Section] = true
	}
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *Agent) prepareLocal(req model.LocalProposalRequest) (*model.Revision, *model.Config, []model.DiffChange, model.ValidationReport, *httpError) {
	hr := a.headRevision()
	if hr == nil {
		return nil, nil, nil, model.ValidationReport{}, herr(model.ErrStale, "this site has no head yet; the first setup comes from the cloud", nil)
	}
	if req.BaseRevID != hr.RevID {
		return nil, nil, nil, model.ValidationReport{}, herr(model.ErrStale, "the head changed; reload and edit again", map[string]any{"headRevId": hr.RevID})
	}
	base := revConfig(hr) // a pre-round-D head is edited in its normalized form (declared volumes)
	cfg := buildLocalConfig(base, req.Config)
	issues := validate.CheckLocalEdit(base, &cfg)
	is := validate.ValidateConfig(&cfg, a.validationContext(&hr.Catalog))
	all := append(append([]model.ValidationIssue{}, issues...), is.List...)
	slugs := svcSlugs(&hr.Catalog)
	changes := diff.Configs(base, &cfg, func(id string) string {
		if s, ok := slugs[id]; ok {
			return s
		}
		return id
	})
	if len(changes) == 0 {
		all = append(all, model.ValidationIssue{Path: "", Code: "V_NO_CHANGE", Message: "nothing changed compared to the head", Severity: "error"})
	}
	ok := true
	for _, x := range all {
		if x.Severity == "error" {
			ok = false
		}
	}
	return hr, &cfg, changes, model.ValidationReport{Ok: ok, Issues: all}, nil
}

func (a *Agent) handlePreviewLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	var req model.LocalProposalRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	_, _, changes, rep, e := a.prepareLocal(req)
	if e != nil {
		return nil, 0, e
	}
	return model.LocalProposalPreviewResponse{Changes: changes, Validation: rep}, 200, nil
}

func (a *Agent) handleCreateLocal(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.LocalProposalRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	req.Summary = strings.TrimSpace(req.Summary)
	if req.Summary == "" || len([]rune(req.Summary)) > 200 {
		return nil, 0, invalid("summary is required (1–200 characters)", []model.ValidationIssue{{Path: "summary", Code: "V_LENGTH", Message: "1–200 characters", Severity: "error"}})
	}
	hr, cfg, changes, rep, e := a.prepareLocal(req)
	if e != nil {
		return nil, 0, e
	}
	if !rep.Ok {
		return nil, 0, invalid("the local change is not valid", rep.Issues)
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	hash, _ := canonical.HashCanonical(cfg)
	parent := hr.RevID
	rev := &model.Revision{RevID: newID(), Seq: hr.Seq + 1, ParentRevID: &parent, Origin: "agent", Actor: rc.actor, CreatedAt: nowTs(),
		Config: *cfg, Hash: hash, Catalog: hr.Catalog, CatalogHash: hr.CatalogHash, Summary: req.Summary, Kind: "change"}
	p := &model.Proposal{RevID: rev.RevID, ParentRevID: &parent, Seq: rev.Seq, Summary: req.Summary, Origin: "agent", Kind: "change",
		CreatedAt: rev.CreatedAt, CreatedBy: rc.actor, Status: model.ProposalPublished, Sites: map[string]model.SiteStaging{}, UpdatedAt: rev.CreatedAt}
	for _, s := range siteSlugs(&hr.Config) {
		p.Sites[s] = model.SiteStaging{Staging: model.StagingPending}
	}
	if !a.receiveProposal(p, rev) {
		return nil, 0, herr(model.ErrInternal, "could not store the proposal", nil)
	}
	a.activity(rc.actorPtr(), "agent", "proposal.created", fmt.Sprintf("local proposal seq %d: %s", rev.Seq, req.Summary),
		actorData(rc, map[string]any{"revId": rev.RevID, "parentRevId": parent, "seq": rev.Seq, "summary": req.Summary, "sections": changedSections(changes)}))
	push := model.PeerProposalPush{Proposal: *p, Revision: *rev}
	for _, slug := range a.reachablePeers() {
		go func(s string) {
			c, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			defer cancel()
			if err := a.peerJSON(c, s, "POST", "/_opm/peer/proposals", push, nil, 30*time.Second); err != nil {
				a.log.Warn("mesh", "push proposal failed", "peer", s, "error", err.Error())
			}
		}(slug)
	}
	a.enqueue("local-proposal", "local:"+rev.RevID, "POST", "/v1/agent/proposals", model.RegisterLocalProposalRequest{Proposal: *p, Revision: *rev})
	cur, _ := a.proposal(rev.RevID)
	if cur == nil {
		cur = p
	}
	return model.LocalProposalResponse{Proposal: *cur, Changes: changes}, 201, nil
}

// ───────────────────────── the gate (§14.4)

func headEq(h *model.HeadHash, rev *string) bool {
	if h == nil {
		return rev == nil
	}
	return rev != nil && *rev == h.RevID
}

func (a *Agent) gateSites(target *model.Config) []string {
	cfg, _ := a.headConfig()
	return union(siteSlugs(cfg), siteSlugs(target))
}

func (a *Agent) gateForProposal(p *model.Proposal, rev *model.Revision) model.GateCheck {
	sites := a.gateSites(&rev.Config)
	g := model.GateCheck{Sites: sites, Conditions: []model.GateCondition{}}
	var bad []string
	// (a) parent
	for _, s := range sites {
		pw := a.lastPing(s)
		if pw == nil || !headEq(pw.Head, rev.ParentRevID) {
			bad = append(bad, s)
		}
	}
	g.Conditions = append(g.Conditions, cond("parent", bad, "every site's head is the proposal parent", "head differs on "))
	bad = a.unreachable(sites)
	g.Conditions = append(g.Conditions, cond("reachable", bad, "every site is enrolled and answered in the last 30 s", "not reachable / not installed: "))
	bad = nil
	for _, s := range sites {
		st := model.StagingPending
		if s == a.site {
			if row, ok := a.staging(rev.RevID, s); ok {
				st = row.Staging
			}
		} else if pw := a.lastPing(s); pw != nil {
			for _, ps := range pw.Proposals {
				if ps.RevID == rev.RevID {
					st = ps.Staging.Staging
				}
			}
		}
		if st != model.StagingReady {
			bad = append(bad, s+" ("+st+")")
		}
	}
	g.Conditions = append(g.Conditions, cond("staged", bad, "every site staged this revision", "not staged: "))
	g.Conditions = append(g.Conditions, idleWarn(a.lockedSites(sites))) // §21 #110 warns, never closes
	ok := p.Status == model.ProposalReady
	detail := "the proposal is ready"
	if !ok {
		detail = "the proposal is " + p.Status
	}
	g.Conditions = append(g.Conditions, model.GateCondition{Key: "status", Ok: ok, Detail: detail})
	g.Open = true
	for _, c := range g.Conditions {
		if !c.Ok {
			g.Open = false
		}
	}
	return g
}

// idleWarn: the "idle" condition is always ok (§21 #110); a running update only adds a warning (starting stops it).
func idleWarn(locked []string) model.GateCondition {
	if len(locked) == 0 {
		return model.GateCondition{Key: "idle", Ok: true, Detail: "no update is running"}
	}
	return model.GateCondition{Key: "idle", Ok: true, Warn: true, Detail: "an update is running on " + strings.Join(locked, ", ") + ": starting stops it"}
}

func cond(key string, bad []string, okText, badPrefix string) model.GateCondition {
	if len(bad) == 0 {
		return model.GateCondition{Key: key, Ok: true, Detail: okText}
	}
	return model.GateCondition{Key: key, Ok: false, Detail: badPrefix + strings.Join(bad, ", ")}
}

func (a *Agent) unreachable(sites []string) []string {
	enrolled := a.enrolledSites()
	var bad []string
	for _, s := range sites {
		if !contains(enrolled, s) || !a.reachable(s) {
			bad = append(bad, s)
		}
	}
	return bad
}

func (a *Agent) lockedSites(sites []string) []string {
	var bad []string
	seen := map[string]bool{}
	for _, s := range union(sites, a.enrolledSites()) {
		if seen[s] {
			continue
		}
		seen[s] = true
		if pw := a.lastPing(s); pw != nil && pw.Lock != nil {
			if s != a.site && a.reachable(s) {
				// the cached ping may predate the unlock: confirm with a fresh ping before refusing
				a.pingOne(a.ctx, s)
				if pw2 := a.lastPing(s); pw2 == nil || pw2.Lock == nil {
					continue
				}
			}
			bad = append(bad, s)
		}
	}
	return bad
}

// ───────────────────────── intents (60 s wait + typed ACCEPT)

func (a *Agent) createIntent(kind string, revID, snapID *string, rc *reqCtx) model.ApproveIntent {
	now := a.clock()
	nb := now.Add(a.opt.ApproveWait) // the latest ready time (all at ×1); §21 #95 fast-forward can make it earlier
	exp := nb.Add(600 * time.Second)
	in := model.ApproveIntent{IntentID: newID(), Kind: kind, RevID: revID, SnapshotID: snapID, Actor: rc.actor, SessionID: rc.sessionID, NotBefore: tsOf(nb), ExpiresAt: tsOf(exp), WaitSec: a.opt.ApproveWait.Seconds()}
	_, _ = a.db.Exec(`INSERT INTO intents(intentId, kind, revId, snapshotId, actor, sessionId, notBefore, expiresAt) VALUES(?,?,?,?,?,?,?,?)`,
		in.IntentID, kind, nullStrPtr(revID), nullStrPtr(snapID), rc.actor, rc.sessionID, nb.Unix(), exp.Unix())
	s := newIntentWait(a.opt.ApproveWait, now)
	a.iwMu.Lock()
	a.iw[in.IntentID] = s
	a.saveIntentWait(in.IntentID, s)
	a.iwMu.Unlock()
	return in
}

func nullStrPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// useIntent validates an intent for confirmation (not yet marking it used).
func (a *Agent) checkIntent(id, kind, target string, rc *reqCtx, typed string) *httpError {
	return a.checkIntentWord(id, kind, target, rc, typed, "ACCEPT")
}

// checkIntentWord: checkIntent with the word the person types (ACCEPT; the volume slug for volume-empty / -restore).
func (a *Agent) checkIntentWord(id, kind, target string, rc *reqCtx, typed, word string) *httpError {
	var k, sid string
	var rev, snap sql.NullString
	var nb, exp int64
	var used sql.NullInt64
	err := a.db.QueryRow(`SELECT kind, revId, snapshotId, sessionId, notBefore, expiresAt, usedAt FROM intents WHERE intentId = ?`, id).Scan(&k, &rev, &snap, &sid, &nb, &exp, &used)
	if err != nil {
		return invalid("no such intent; press Approve again", []model.ValidationIssue{{Path: "intentId", Code: "V_REF", Message: "unknown intent", Severity: "error"}})
	}
	tgt := rev.String
	if kind == "rollback" {
		tgt = snap.String
	}
	if k != kind || tgt != target || sid != rc.sessionID || used.Valid {
		return invalid("this intent is not valid for this action / session", []model.ValidationIssue{{Path: "intentId", Code: "V_REF", Message: "intent does not match", Severity: "error"}})
	}
	nowT := a.clock()
	if nowT.Unix() >= exp {
		return invalid("the intent expired; press Approve again", []model.ValidationIssue{{Path: "intentId", Code: "V_RANGE", Message: "expired", Severity: "error"}})
	}
	// §21 #95: the wait counts effective seconds (Σ speed × dt on the agent clock), not the wall clock vs notBefore
	a.iwMu.Lock()
	s := a.loadIntentWait(id, nb)
	s.advance(nowT)
	left := s.remaining()
	a.iwMu.Unlock()
	if left > 0 {
		return invalid(fmt.Sprintf("wait %d s before confirming", int(math.Ceil(left))), []model.ValidationIssue{{Path: "intentId", Code: "V_RANGE", Message: "wait", Severity: "error"}})
	}
	if strings.TrimSpace(typed) != word || (word == "ACCEPT" && typed != word) {
		return invalid(`type `+word+` exactly`, []model.ValidationIssue{{Path: "typed", Code: "V_FORMAT", Message: `must be "` + word + `"`, Severity: "error"}})
	}
	return nil
}

// markIntentUsed marks the intent used and returns its fast-forward record (§21 #95; nil = waited at ×1).
func (a *Agent) markIntentUsed(id string, actor string) *model.FastForwardUse {
	now := a.clock()
	_, _ = a.db.Exec(`UPDATE intents SET usedAt = ? WHERE intentId = ?`, now.Unix(), id)
	var ff *model.FastForwardUse
	a.iwMu.Lock()
	if s, ok := a.iw[id]; ok {
		s.advance(now)
		ff = s.fastForward(actor)
	} else {
		var raw string
		var s intentWait
		if a.db.QueryRow(`SELECT state FROM intent_waits WHERE intentId = ?`, id).Scan(&raw) == nil && json.Unmarshal([]byte(raw), &s) == nil {
			ff = s.fastForward(actor)
		}
	}
	a.iwMu.Unlock()
	a.dropIntentWait(id)
	return ff
}

func (a *Agent) handleApproveStart(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	id := r.PathValue("revId")
	p, ok := a.proposal(id)
	rev, ok2 := a.revision(id)
	if !ok || !ok2 {
		return nil, 0, herr(model.ErrNotFound, "proposal not found", nil)
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	g := a.gateForProposal(p, rev)
	if !g.Open {
		return nil, 0, herr(model.ErrGateClosed, "the gate is closed", map[string]any{"gate": g})
	}
	in := a.createIntent("update", &id, nil, getRC(r))
	return model.ApproveStartResponse{Intent: in, Gate: g}, 201, nil
}

func (a *Agent) handleApprove(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	id := r.PathValue("revId")
	p, ok := a.proposal(id)
	rev, ok2 := a.revision(id)
	if !ok || !ok2 {
		return nil, 0, herr(model.ErrNotFound, "proposal not found", nil)
	}
	var req model.ApproveRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if e := a.checkIntent(req.IntentID, "update", id, rc, req.Typed); e != nil {
		return nil, 0, e
	}
	// §21 #110 a running update never blocks this (the lock is advisory)
	g := a.gateForProposal(p, rev)
	if !g.Open {
		return nil, 0, herr(model.ErrGateClosed, "the gate closed again", map[string]any{"gate": g})
	}
	rc.ff = a.markIntentUsed(req.IntentID, rc.actor)
	run, e := a.startUpdate("update", rev, p, nil, rc)
	if e != nil {
		return nil, 0, e
	}
	return model.ApproveResponse{UpdateID: run.UpdateID, Run: *run}, 201, nil
}

func (a *Agent) handleCancelIntent(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	res, _ := a.db.Exec(`DELETE FROM intents WHERE intentId = ? AND sessionId = ? AND usedAt IS NULL`, r.PathValue("intentId"), rc.sessionID)
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, 0, herr(model.ErrNotFound, "no such pending intent", nil)
	}
	a.dropIntentWait(r.PathValue("intentId"))
	return map[string]bool{"cancelled": true}, 200, nil
}

// unmaskPresetLiterals (§21 #103): a preset sent back with its static password / token as MASK (config views mask them)
// that equals the head's preset once masked IS the head's preset (its raw bytes, so the hash and the V_LOCAL_DEVTOOLS
// comparison stay exact). Anything else is kept as sent (and refused by checkLocalEdit when it differs).
func unmaskPresetLiterals(in []model.DevToolPreset, head []model.DevToolPreset) *[]model.DevToolPreset {
	out := make([]model.DevToolPreset, len(in))
	for i, p := range in {
		out[i] = p
		if !p.HasLiteral() {
			continue
		}
		for _, h := range head {
			if h.PresetSlug == p.PresetSlug && h.HasLiteral() && sameJSON(p.MaskedLiterals(), h.MaskedLiterals()) {
				out[i] = h
			}
		}
	}
	return &out
}

func sameJSON(a, b any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	if ea != nil || eb != nil {
		return false
	}
	ca, ea := canonical.CanonicalizeJSON(ja)
	cb, eb := canonical.CanonicalizeJSON(jb)
	return ea == nil && eb == nil && ca == cb
}
