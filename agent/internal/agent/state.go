package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/ulid"
	"opm/agent/internal/validate"
)

// ───────────────────────── head + revisions

func (a *Agent) head() *model.HeadHash {
	var h model.HeadHash
	if ok, _ := a.db.GetKV("head", &h); ok && h.RevID != "" {
		return &h
	}
	return nil
}

func (a *Agent) headRevIDOrNil() any {
	if h := a.head(); h != nil {
		return h.RevID
	}
	return nil
}

func (a *Agent) headRevision() *model.Revision {
	h := a.head()
	if h == nil {
		return nil
	}
	r, _ := a.revision(h.RevID)
	return r
}

// revConfig is the config of a revision as the agent runs it: a config written before round D (no site volumes, legacy
// `data` sources) is normalized (validate.NormalizeConfigVolumes); the stored revision (and its hash) never changes.
func revConfig(r *model.Revision) *model.Config {
	return validate.NormalizeRoundE(validate.NormalizeConfigVolumes(&r.Config, r.Catalog.Images))
}

// headConfig returns the head config (normalized, see revConfig; empty config without a head) and its catalog.
func (a *Agent) headConfig() (*model.Config, *model.RevisionCatalog) {
	if r := a.headRevision(); r != nil {
		return revConfig(r), &r.Catalog
	}
	c := validate.EmptyConfig()
	return &c, emptyCatalog()
}

// headConfigRO is headConfig for READ-ONLY hot paths (peer ping, container counts, uptime sampler): the decoded
// config is kept per head revision id (revisions never change once stored), so a 5–30 s loop no longer queries and
// decodes the whole config every tick (§21 #112). Callers must not modify the result.
func (a *Agent) headConfigRO() (*model.Config, *model.RevisionCatalog) {
	id := ""
	if h := a.head(); h != nil {
		id = h.RevID
	}
	a.roMu.Lock()
	if a.roCfg != nil && a.roRev == id {
		cfg, cat := a.roCfg, a.roCat
		a.roMu.Unlock()
		return cfg, cat
	}
	a.roMu.Unlock()
	cfg, cat := a.headConfig()
	a.roMu.Lock()
	a.roRev, a.roCfg, a.roCat = id, cfg, cat
	a.roMu.Unlock()
	return cfg, cat
}

func emptyCatalog() *model.RevisionCatalog {
	return &model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
}

func normalizeConfig(c *model.Config) {
	if c.Variables == nil {
		c.Variables = []model.Variable{}
	}
	if c.Secrets == nil {
		c.Secrets = []model.SecretDef{}
	}
	if c.Files == nil {
		c.Files = []model.FileDef{}
	}
	if c.Sites == nil {
		c.Sites = []model.Site{}
	}
}

func normalizeCatalog(c *model.RevisionCatalog) {
	if c.Services == nil {
		c.Services = map[string]model.CatalogService{}
	}
	if c.Images == nil {
		c.Images = map[string]model.ImageSpec{}
	}
	if c.NotFoundPages == nil {
		c.NotFoundPages = map[string]model.CatalogNotFoundPage{}
	}
}

// ErrOldShape: a revision in an old shape (pre-phase-U serviceId + tag, or pre-round-C one container per service).
var ErrOldShape = errors.New("old revision shape")

// checkShape refuses old-shape revisions: containers without containerSlug / serviceSlug / version (the pre-round-C
// shape had `port` / `volume` and no containerSlug), depends without containerSlug, proxy routes without containerSlug,
// catalog services / images not keyed by slug / slug:version. There is no migration on sites: the cloud republishes.
func checkShape(r *model.Revision) error {
	old := func(what string) error {
		return fmt.Errorf("revision %s: %w (%s); this agent needs container instances (containerSlug, ports, volumes) — publish the deployment again from the cloud", r.RevID, ErrOldShape, what)
	}
	for _, s := range r.Config.Sites {
		for _, c := range s.Config.Containers {
			if c.ServiceSlug == "" || c.Version == "" {
				return old("site " + s.SiteSlug + ": a container has no serviceSlug / version")
			}
			if c.ContainerSlug == "" || c.Ports == nil || c.Volumes == nil {
				return old("site " + s.SiteSlug + ": container " + c.ServiceSlug + " has no containerSlug / ports / volumes")
			}
			for _, d := range c.Depends {
				if d.ContainerSlug == "" {
					return old("site " + s.SiteSlug + ": container " + c.ContainerSlug + " depends without containerSlug")
				}
			}
		}
		for _, rt := range s.Config.Edge.Routes {
			if rt.Action.Type == "proxy" && rt.Action.ContainerSlug == "" {
				return old("site " + s.SiteSlug + ": route " + rt.RouteID + " proxies without containerSlug")
			}
		}
	}
	for k, sv := range r.Catalog.Services {
		if sv.ServiceSlug == "" || sv.ServiceSlug != k {
			return old("catalog service " + k + " is not keyed by its serviceSlug")
		}
	}
	for k, im := range r.Catalog.Images {
		if im.ServiceSlug == "" || im.Version == "" || k != im.ServiceSlug+":"+im.Version {
			return old("catalog image " + k + " is not keyed by serviceSlug:version")
		}
	}
	return nil
}

// verifyRevision refuses old-shape revisions, then recomputes hash + catalogHash.
func verifyRevision(r *model.Revision) error {
	if err := checkShape(r); err != nil {
		return err
	}
	normalizeConfig(&r.Config)
	normalizeCatalog(&r.Catalog)
	h, err := canonical.HashCanonical(r.Config)
	if err != nil {
		return err
	}
	if h != r.Hash {
		return fmt.Errorf("revision %s: config hash mismatch (%s ≠ %s)", r.RevID, h, r.Hash)
	}
	ch, err := canonical.HashCanonical(r.Catalog)
	if err != nil {
		return err
	}
	if ch != r.CatalogHash {
		return fmt.Errorf("revision %s: catalog hash mismatch", r.RevID)
	}
	return nil
}

func (a *Agent) revision(revID string) (*model.Revision, bool) {
	var r model.Revision
	var parent, rb sql.NullString
	var cfg, cat string
	err := a.db.QueryRow(`SELECT revId, seq, parentRevId, origin, actor, createdAt, config, hash, catalog, catalogHash, summary, kind, rollbackOf FROM revisions WHERE revId = ?`, revID).
		Scan(&r.RevID, &r.Seq, &parent, &r.Origin, &r.Actor, &r.CreatedAt, &cfg, &r.Hash, &cat, &r.CatalogHash, &r.Summary, &r.Kind, &rb)
	if err != nil {
		return nil, false
	}
	if parent.Valid {
		p := parent.String
		r.ParentRevID = &p
	}
	_ = json.Unmarshal([]byte(cfg), &r.Config)
	_ = json.Unmarshal([]byte(cat), &r.Catalog)
	normalizeConfig(&r.Config)
	normalizeCatalog(&r.Catalog)
	if rb.Valid && rb.String != "" && rb.String != "null" {
		var x model.RollbackRef
		if json.Unmarshal([]byte(rb.String), &x) == nil {
			r.RollbackOf = &x
		}
	}
	return &r, true
}

func (a *Agent) putRevision(r *model.Revision) error {
	if err := verifyRevision(r); err != nil {
		return err
	}
	var rb any
	if r.RollbackOf != nil {
		rb = store.JSON(r.RollbackOf)
	}
	_, err := a.db.Exec(`INSERT INTO revisions(revId, seq, parentRevId, origin, actor, createdAt, config, hash, catalog, catalogHash, summary, kind, rollbackOf)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(revId) DO NOTHING`,
		r.RevID, r.Seq, store.NullStr(r.ParentRevID), r.Origin, r.Actor, r.CreatedAt, store.JSON(r.Config), r.Hash, store.JSON(r.Catalog), r.CatalogHash, r.Summary, r.Kind, rb)
	return err
}

func (a *Agent) listRevisions(limit int) []model.RevisionSummary {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := a.db.Query(`SELECT revId, seq, parentRevId, origin, actor, createdAt, hash, summary, kind FROM revisions ORDER BY seq DESC, revId DESC LIMIT ?`, limit)
	if err != nil {
		return []model.RevisionSummary{}
	}
	defer rows.Close()
	out := []model.RevisionSummary{}
	for rows.Next() {
		var s model.RevisionSummary
		var parent sql.NullString
		if rows.Scan(&s.RevID, &s.Seq, &parent, &s.Origin, &s.Actor, &s.CreatedAt, &s.Hash, &s.Summary, &s.Kind) == nil {
			if parent.Valid {
				p := parent.String
				s.ParentRevID = &p
			}
			out = append(out, s)
		}
	}
	return out
}

// maskConfig returns the ConfigView (secrets masked).
func maskConfig(c *model.Config) model.ConfigView {
	v := model.ConfigView{Variables: c.Variables, Files: []model.FileDef{}, Sites: c.Sites, Secrets: []model.SecretDefView{}}
	for _, f := range c.Files { // views always carry render (legacy documents: the default for the editor)
		v.Files = append(v.Files, validate.NormalizeFileDef(f))
	}
	for _, s := range c.Secrets {
		set := s.Value != nil && *s.Value != ""
		var val *string
		if set {
			m := model.Mask
			val = &m
		}
		v.Secrets = append(v.Secrets, model.SecretDefView{SecretSlug: s.SecretSlug, Label: s.Label, Description: s.Description, Required: s.Required, Generate: s.Generate, Value: val, IsSet: set, RotatedAt: s.RotatedAt})
	}
	if v.Variables == nil {
		v.Variables = []model.Variable{}
	}
	if v.Files == nil {
		v.Files = []model.FileDef{}
	}
	if v.Sites == nil {
		v.Sites = []model.Site{}
	}
	v.Sites = maskPresetLiterals(v.Sites)
	return v
}

// maskPresetLiterals (§21 #103): static dev-tool preset passwords / tokens are shown as MASK in every config view
// (copies; the revision is not changed). A local edit that sends them back masked keeps the head's preset
// (unmaskPresetLiterals in buildLocalConfig).
func maskPresetLiterals(sites []model.Site) []model.Site {
	out := sites
	copied := false
	for i := range sites {
		list := sites[i].Config.DevToolList()
		has := false
		for _, p := range list {
			if p.HasLiteral() {
				has = true
			}
		}
		if !has {
			continue
		}
		if !copied {
			out = append([]model.Site(nil), sites...)
			copied = true
		}
		ml := make([]model.DevToolPreset, len(list))
		for j, p := range list {
			ml[j] = p.MaskedLiterals()
		}
		out[i].Config.DevTools = &ml
	}
	return out
}

func revisionView(r *model.Revision) model.RevisionView {
	return model.RevisionView{RevID: r.RevID, Seq: r.Seq, ParentRevID: r.ParentRevID, Origin: r.Origin, Actor: r.Actor, CreatedAt: r.CreatedAt,
		Config: maskConfig(&r.Config), Hash: r.Hash, Catalog: r.Catalog, CatalogHash: r.CatalogHash, Summary: r.Summary, Kind: r.Kind, RollbackOf: r.RollbackOf}
}

func siteOf(c *model.Config, slug string) *model.Site {
	for i := range c.Sites {
		if c.Sites[i].SiteSlug == slug {
			return &c.Sites[i]
		}
	}
	return nil
}

func siteSlugs(c *model.Config) []string {
	out := []string{}
	for _, s := range c.Sites {
		out = append(out, s.SiteSlug)
	}
	return out
}

func union(a, b []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, x := range append(append([]string{}, a...), b...) {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func svcSlugs(cat *model.RevisionCatalog) map[string]string {
	m := map[string]string{}
	for slug := range cat.Services {
		m[slug] = slug
	}
	return m
}

// ───────────────────────── proposals

func (a *Agent) proposal(revID string) (*model.Proposal, bool) {
	var data string
	if err := a.db.QueryRow(`SELECT data FROM proposals WHERE revId = ?`, revID).Scan(&data); err != nil {
		return nil, false
	}
	var p model.Proposal
	if json.Unmarshal([]byte(data), &p) != nil {
		return nil, false
	}
	if p.Sites == nil {
		p.Sites = map[string]model.SiteStaging{}
	}
	return &p, true
}

func (a *Agent) putProposal(p *model.Proposal) {
	if p.Sites == nil {
		p.Sites = map[string]model.SiteStaging{}
	}
	_, err := a.db.Exec(`INSERT INTO proposals(revId, status, createdAt, data) VALUES(?,?,?,?) ON CONFLICT(revId) DO UPDATE SET status = excluded.status, data = excluded.data`,
		p.RevID, p.Status, p.CreatedAt, store.JSON(p))
	if err != nil {
		a.log.Error("stager", "store proposal failed", "revId", p.RevID, "error", err.Error())
	}
}

func (a *Agent) proposals() []model.Proposal {
	rows, err := a.db.Query(`SELECT data FROM proposals ORDER BY revId DESC`)
	if err != nil {
		return nil
	}
	var raws []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			raws = append(raws, s)
		}
	}
	rows.Close()
	out := []model.Proposal{}
	for _, s := range raws {
		var p model.Proposal
		if json.Unmarshal([]byte(s), &p) == nil {
			if p.Sites == nil {
				p.Sites = map[string]model.SiteStaging{}
			}
			out = append(out, p)
		}
	}
	return out
}

func (a *Agent) openProposals() []model.Proposal {
	out := []model.Proposal{}
	for _, p := range a.proposals() {
		if !model.IsTerminal(p.Status) {
			out = append(out, p)
		}
	}
	return out
}

// participatingSites of a proposal = parent config sites ∪ proposal config sites.
func (a *Agent) participatingSites(rev *model.Revision) []string {
	var parent []string
	if rev.ParentRevID != nil {
		if pr, ok := a.revision(*rev.ParentRevID); ok {
			parent = siteSlugs(&pr.Config)
		}
	}
	return union(parent, siteSlugs(&rev.Config))
}

// ───────────────────────── staging rows

type stagingRow struct {
	model.SiteStaging
	Detail *model.StagingDetail
}

func (a *Agent) setStaging(revID, site string, st model.SiteStaging, detail *model.StagingDetail) {
	if st.UpdatedAt == nil {
		t := nowTs()
		st.UpdatedAt = &t
	}
	var det any
	if detail != nil {
		det = store.JSON(detail)
	}
	_, _ = a.db.Exec(`INSERT INTO staging(revId, siteSlug, staging, progress, error, detail, updatedAt) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(revId, siteSlug) DO UPDATE SET staging = excluded.staging, progress = excluded.progress, error = excluded.error,
		detail = COALESCE(excluded.detail, staging.detail), updatedAt = excluded.updatedAt`,
		revID, site, st.Staging, st.Progress, store.NullStr(st.Error), det, a.now().Unix())
}

func (a *Agent) staging(revID, site string) (stagingRow, bool) {
	var r stagingRow
	var errS, det sql.NullString
	var upd int64
	err := a.db.QueryRow(`SELECT staging, progress, error, detail, updatedAt FROM staging WHERE revId = ? AND siteSlug = ?`, revID, site).
		Scan(&r.Staging, &r.Progress, &errS, &det, &upd)
	if err != nil {
		return r, false
	}
	if errS.Valid {
		e := errS.String
		r.Error = &e
	}
	if det.Valid {
		var d model.StagingDetail
		if json.Unmarshal([]byte(det.String), &d) == nil {
			r.Detail = &d
		}
	}
	return r, true
}

// ───────────────────────── activities (hash chain, §13.5)

// activity appends one record to this site's log. actor nil = no actor.
func (a *Agent) activity(actor *string, origin, typ, summary string, data map[string]any) *model.Activity {
	if data == nil {
		data = map[string]any{}
	}
	// normalize data through JSON (numbers → float64) so the hash input equals what is stored/sent
	var norm map[string]any
	_ = json.Unmarshal([]byte(store.JSON(data)), &norm)
	if norm == nil {
		norm = map[string]any{}
	}
	if len(store.JSON(norm)) > 16*1024 {
		norm = map[string]any{"truncated": true}
	}
	a.actMu.Lock()
	defer a.actMu.Unlock()
	var lastSeq int
	var lastHash sql.NullString
	_ = a.db.QueryRow(`SELECT seq, hash FROM activities ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &lastHash)
	seq := lastSeq + 1
	var prev *string
	if seq > 1 && lastHash.Valid {
		h := lastHash.String
		prev = &h
	}
	act := model.Activity{ActivityID: a.site + ":" + fmt.Sprint(seq), DeploymentID: a.dep, SiteSlug: a.site, Seq: seq, Ts: nowTs(),
		Actor: actor, Origin: origin, Type: typ, Summary: summary, Data: norm, PrevHash: prev}
	h, err := canonical.ActivityHash(canonical.ActivityFields{ActivityID: act.ActivityID, DeploymentID: act.DeploymentID, SiteSlug: act.SiteSlug,
		Seq: float64(seq), Ts: act.Ts, Actor: actor, Origin: origin, Type: typ, Summary: summary, Data: norm, PrevHash: prev})
	if err != nil {
		a.log.Error("pipe", "activity hash failed", "type", typ, "error", err.Error())
		return nil
	}
	act.Hash = h
	_, err = a.db.Exec(`INSERT INTO activities(seq, activityId, ts, actor, origin, type, summary, data, prevHash, hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		seq, act.ActivityID, act.Ts, store.NullStr(actor), origin, typ, summary, store.JSON(norm), store.NullStr(prev), h)
	if err != nil {
		a.log.Error("pipe", "activity insert failed", "type", typ, "error", err.Error())
		return nil
	}
	a.log.Debug("pipe", "activity", "seq", seq, "type", typ)
	a.kickLoop("pipe")
	return &act
}

func strp(s string) *string { return &s }

func scanActivities(rows *sql.Rows, dep, site string) []model.Activity {
	out := []model.Activity{}
	for rows.Next() {
		var x model.Activity
		var actor, prev sql.NullString
		var data string
		if rows.Scan(&x.Seq, &x.ActivityID, &x.Ts, &actor, &x.Origin, &x.Type, &x.Summary, &data, &prev, &x.Hash) != nil {
			continue
		}
		x.DeploymentID, x.SiteSlug = dep, site
		if actor.Valid {
			s := actor.String
			x.Actor = &s
		}
		if prev.Valid {
			s := prev.String
			x.PrevHash = &s
		}
		_ = json.Unmarshal([]byte(data), &x.Data)
		if x.Data == nil {
			x.Data = map[string]any{}
		}
		out = append(out, x)
	}
	return out
}

// activitiesRange returns entries with from ≤ seq ≤ to (to ≤ 0 = last), ascending.
func (a *Agent) activitiesRange(from, to int, limit int) []model.Activity {
	if to <= 0 {
		to = 1 << 31
	}
	if limit <= 0 {
		limit = 1 << 31
	}
	rows, err := a.db.Query(`SELECT seq, activityId, ts, actor, origin, type, summary, data, prevHash, hash FROM activities WHERE seq >= ? AND seq <= ? ORDER BY seq ASC LIMIT ?`, from, to, limit)
	if err != nil {
		return []model.Activity{}
	}
	defer rows.Close()
	return scanActivities(rows, a.dep, a.site)
}

// activitiesPage returns entries newest first; cursor = seq to start below.
func (a *Agent) activitiesPage(beforeSeq, limit int, typ, actor string) ([]model.Activity, *string) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if beforeSeq <= 0 {
		beforeSeq = 1 << 31
	}
	q := `SELECT seq, activityId, ts, actor, origin, type, summary, data, prevHash, hash FROM activities WHERE seq < ?`
	args := []any{beforeSeq}
	if typ != "" {
		q += ` AND (type = ? OR type LIKE ?)`
		args = append(args, typ, strings.TrimSuffix(typ, ".")+".%")
	}
	if actor != "" {
		q += ` AND actor = ?`
		args = append(args, strings.ToLower(actor))
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return []model.Activity{}, nil
	}
	items := scanActivities(rows, a.dep, a.site)
	rows.Close()
	var cur *string
	if len(items) > limit {
		items = items[:limit]
		c := fmt.Sprint(items[len(items)-1].Seq)
		cur = &c
	}
	return items, cur
}

func (a *Agent) lastSeq() int {
	var s sql.NullInt64
	_ = a.db.QueryRow(`SELECT MAX(seq) FROM activities`).Scan(&s)
	return int(s.Int64)
}

// ───────────────────────── kv helpers

func (a *Agent) kvInt(key string) int {
	var v int
	_, _ = a.db.GetKV(key, &v)
	return v
}

func (a *Agent) keyHash() (string, int) {
	var k model.AccessKeyRef
	if ok, _ := a.db.GetKV("keyHash", &k); ok {
		return k.Hash, k.Version
	}
	return "", 0
}

func (a *Agent) lock() *model.UpdateLock {
	var l model.UpdateLock
	if ok, _ := a.db.GetKV("lock", &l); ok && l.UpdateID != "" {
		return &l
	}
	return nil
}

var errNotFound = errors.New("not found")

func newID() string { return ulid.New() }
