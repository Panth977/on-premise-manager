package core

import (
	"fmt"
	"sort"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

// CLOUD_LOG is CLOUD_LOG.
const CLOUD_LOG = "_cloud"

const maxDataBytes = 16 * 1024

// ActivityHash is activityHash.
func ActivityHash(a *doc.Map) (string, error) {
	s, err := shared.ActivityHashInput(a)
	if err != nil {
		return "", err
	}
	return cloud.Sha256Of([]byte(s)), nil
}

// CloudEntry is CloudEntry. Actor: string or nil; Origin "" = cloud.
type CloudEntry struct {
	Type, Summary string
	Data          *doc.Map
	Actor         any
	Origin        string
}

// CloudLog collects "_cloud" entries inside one transaction; merge DepUpdate() into the deployment write.
type CloudLog struct {
	DeploymentID string
	Written      []*doc.Map
	seq          float64
	last         any
}

// NewCloudLog is new CloudLog(deploymentId, dep).
func NewCloudLog(deploymentID string, dep *doc.Map) *CloudLog {
	return &CloudLog{DeploymentID: deploymentID, seq: dep.Num("cloudActivitySeq"), last: or(dep, "_cloudLastHash", nil)}
}

// Add is add: writes one entry. Data the canonical form cannot express panics (a TS throw: 500).
func (l *CloudLog) Add(tx *driver.Tx, e CloudEntry) *doc.Map {
	seq := l.seq + 1
	data := e.Data
	if data == nil {
		data = doc.New()
	}
	c, err := shared.Canonicalize(data)
	if err != nil {
		panic(err)
	}
	if doc.JSLen(c) > maxDataBytes {
		data = doc.Obj("truncated", true)
	}
	origin := e.Origin
	if origin == "" {
		origin = "cloud"
	}
	summary := doc.JSSlice(e.Summary, 300)
	if summary == "" {
		summary = e.Type
	}
	var prev any = l.last
	if seq == 1 {
		prev = nil
	}
	a := doc.Obj("activityId", CLOUD_LOG+":"+JSString(seq), "deploymentId", l.DeploymentID, "siteSlug", CLOUD_LOG, "seq", seq, "ts", doc.NowISO(), "actor", e.Actor, "origin", origin, "type", e.Type, "summary", summary, "data", data, "prevHash", prev)
	h, err := ActivityHash(a)
	if err != nil {
		panic(err)
	}
	a.Set("hash", h).Set("uploadedVia", nil).Set("uploadedAt", nil)
	tx.Create(cloud.Col.Activities(l.DeploymentID).Doc(a.Str("activityId")), cloud.EncodeActivity(a))
	l.seq, l.last = seq, h
	l.Written = append(l.Written, a)
	return a
}

// DepUpdate is depUpdate.
func (l *CloudLog) DepUpdate() *doc.Map {
	return doc.Obj("cloudActivitySeq", l.seq, "_cloudLastHash", l.last)
}

// Changed is the changed getter.
func (l *CloudLog) Changed() bool { return len(l.Written) > 0 }

// AppendCloud is appendCloud: "_cloud" entries in their own small transaction (read-only endpoints such as reveal).
func AppendCloud(deploymentID string, entries ...CloudEntry) error {
	if len(entries) == 0 {
		return nil
	}
	return cloud.DB().RunTransaction(func(tx *driver.Tx) error {
		ref := cloud.Col.Deployments().Doc(deploymentID)
		snap, err := tx.Get(ref)
		if err != nil || !snap.Exists() {
			return err
		}
		log := NewCloudLog(deploymentID, cloud.DecodeDeployment(snap.Data()))
		for _, e := range entries {
			log.Add(tx, e)
		}
		tx.Update(ref, log.DepUpdate())
		return nil
	})
}

func shapeError(v any, deploymentID, siteSlug string) string {
	a, ok := v.(*doc.Map)
	if !ok || a == nil {
		return "entry is not an object"
	}
	if a.Val("deploymentId") != deploymentID {
		return "deploymentId does not match"
	}
	if a.Val("siteSlug") != siteSlug {
		return "siteSlug does not match"
	}
	seq := present(a, "seq")
	if !isInt(seq) || seq.(float64) < 1 {
		return "seq must be an integer ≥ 1"
	}
	if want := siteSlug + ":" + JSString(seq); a.Val("activityId") != want {
		return "activityId must be " + want
	}
	ts, isStr := a.StrOK("ts")
	if isStr {
		_, isStr = doc.ParseISO(ts)
	}
	if !isStr {
		return "ts must be an ISO time"
	}
	if t, ok := a.StrOK("type"); !ok || !shared.RE.ActivityType.MatchString(t) {
		return "type has a bad format"
	}
	if _, ok := a.StrOK("summary"); !ok {
		return "summary must be a string"
	}
	if x := present(a, "actor"); x != nil {
		if _, ok := x.(string); !ok {
			return "actor must be a string or null"
		}
	}
	if o := a.Val("origin"); o != "agent" && o != "cloud" && o != "system" {
		return "origin must be agent | cloud | system"
	}
	if a.Map("data") == nil {
		return "data must be an object"
	}
	if x := present(a, "prevHash"); x != nil {
		if _, ok := x.(string); !ok {
			return "prevHash must be a string or null"
		}
	}
	if _, ok := a.StrOK("hash"); !ok {
		return "hash must be a string"
	}
	return ""
}

func seqOf(v any) any {
	if m, ok := v.(*doc.Map); ok {
		return present(m, "seq")
	}
	return doc.Undefined
}

// IngestSiteLog is ingestSiteLog: verify + store entries of ONE site log; the result is an IngestResult.
func IngestSiteLog(deploymentID, siteSlug string, entriesIn []any, via string) (*doc.Map, error) {
	entries := append([]any{}, entriesIn...)
	sort.SliceStable(entries, func(i, j int) bool {
		a, _ := seqOf(entries[i]).(float64)
		b, _ := seqOf(entries[j]).(float64)
		return a < b
	})
	var from, to any = 0.0, 0.0
	if len(entries) > 0 {
		if v := seqOf(entries[0]); !nullish(v) {
			from = v
		}
		if v := seqOf(entries[len(entries)-1]); !nullish(v) {
			to = v
		}
	}
	inserted, skipped, rejected := 0, 0, 0
	errs := []any{}
	reject := func(id, reason string) {
		rejected++
		errs = append(errs, doc.Obj("activityId", id, "reason", reason))
	}
	acts := cloud.Col.Activities(deploymentID)
	seen := map[float64]bool{}
	var seqs []float64
	addSeq := func(s float64) {
		if !seen[s] {
			seen[s] = true
			seqs = append(seqs, s)
		}
	}
	for _, e := range entries {
		if s := seqOf(e); isInt(s) {
			addSeq(s.(float64))
		}
	}
	if len(entries) > 0 {
		if s, ok := seqOf(entries[0]).(float64); ok && s > 1 {
			addSeq(s - 1)
		}
	}
	refs := make([]*driver.DocRef, len(seqs))
	for i, s := range seqs {
		refs[i] = acts.Doc(siteSlug + ":" + JSString(s))
	}
	stored := map[float64]*doc.Map{}
	for i := 0; i < len(refs); i += 300 {
		end := min(i+300, len(refs))
		snaps, err := cloud.DB().GetAll(refs[i:end]...)
		if err != nil {
			return nil, err
		}
		for _, s := range snaps {
			if s.Exists() {
				a := cloud.DecodeActivity(s.Data())
				stored[a.Num("seq")] = a
			}
		}
	}
	broken := false
	var prev *doc.Map
	var toCreate []*doc.Map
	at := doc.NowISO()
	for _, x := range entries {
		e, _ := x.(*doc.Map)
		id := e.Str("activityId")
		if _, ok := e.StrOK("activityId"); !ok {
			id = siteSlug + ":" + JSString(seqOf(x))
		}
		if broken {
			reject(id, "after a broken entry")
			continue
		}
		reason := shapeError(x, deploymentID, siteSlug)
		seq := e.Num("seq")
		if reason == "" {
			if h, err := ActivityHash(e); err != nil {
				return nil, err
			} else if h != e.Str("hash") {
				reason = "hash does not match the entry content"
			}
		}
		if reason == "" {
			var expected any
			known := true
			if seq == 1 {
				expected = nil
			} else if prev != nil && prev.Num("seq") == seq-1 {
				expected = prev.Val("hash")
			} else if s, ok := stored[seq-1]; ok {
				expected = s.Val("hash")
			} else {
				known = false
			}
			if !known {
				reason = "missing seq " + JSString(seq-1)
			} else if e.Val("prevHash") != expected {
				reason = "prevHash does not match the previous entry"
			}
		}
		if reason == "" && prev != nil && prev.Num("seq") == seq {
			reason = "duplicate seq"
		}
		if reason != "" {
			broken = true
			reject(id, reason)
			continue
		}
		if existing, ok := stored[seq]; ok {
			if existing.Val("hash") != e.Val("hash") {
				broken = true
				reject(id, "differs from the stored record")
				continue
			}
			skipped++
			prev = existing
			continue
		}
		clean := doc.Obj("activityId", e.Val("activityId"), "deploymentId", deploymentID, "siteSlug", siteSlug, "seq", seq, "ts", e.Val("ts"), "actor", e.Val("actor"), "origin", e.Val("origin"), "type", e.Val("type"), "summary", e.Val("summary"), "data", e.Val("data"), "prevHash", e.Val("prevHash"), "hash", e.Val("hash"), "uploadedVia", via, "uploadedAt", at)
		toCreate = append(toCreate, clean)
		prev = clean
	}
	if len(toCreate) > 0 {
		bw := cloud.DB().NewBulkWriter()
		bw.OnWriteError(func(err error, failed int) bool { return !driver.IsAlreadyExists(err) && failed < 5 })
		results := make([]*driver.BulkResult, len(toCreate))
		for i, a := range toCreate {
			results[i] = bw.Create(acts.Doc(a.Str("activityId")), cloud.EncodeActivity(a))
		}
		bw.Close()
		for _, r := range results {
			err := r.Wait()
			switch {
			case err == nil:
				inserted++
			case driver.IsAlreadyExists(err):
				skipped++
			default:
				return nil, err
			}
		}
	}
	ack, err := AdvanceAck(deploymentID, siteSlug)
	if err != nil {
		return nil, err
	}
	return doc.Obj("inserted", inserted, "skipped", skipped, "rejected", rejected, "errors", errs, "ackSeq", ack, "fromSeq", from, "toSeq", to), nil
}

// AdvanceAck is advanceAck: the highest contiguous seq stored for a site, persisted on the agent doc.
func AdvanceAck(deploymentID, siteSlug string) (float64, error) {
	agentRef := cloud.Col.Agents(deploymentID).Doc(siteSlug)
	snap, err := agentRef.Get()
	if err != nil {
		return 0, err
	}
	cur, hasCur := snap.Data().NumOK("ackSeq")
	ack := cur
	acts := cloud.Col.Activities(deploymentID)
	for {
		refs := make([]*driver.DocRef, 100)
		for i := range refs {
			refs[i] = acts.Doc(fmt.Sprintf("%s:%s", siteSlug, JSString(ack+1+float64(i))))
		}
		snaps, err := cloud.DB().GetAll(refs...)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, s := range snaps {
			if !s.Exists() {
				break
			}
			n++
		}
		ack += float64(n)
		if n < 100 {
			break
		}
	}
	if snap.Exists() && (!hasCur || ack != cur) {
		if err := agentRef.Update(doc.Obj("ackSeq", ack)); err != nil {
			return 0, err
		}
	}
	return ack, nil
}

// Inc is inc: FieldValue.increment(n).
func Inc(n float64) *doc.Transform { return doc.Increment(n) }
