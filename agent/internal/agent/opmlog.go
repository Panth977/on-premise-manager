package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
)

func (a *Agent) handleSiteActivities(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	before := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil {
			return nil, 0, invalid("bad cursor", nil)
		}
		before = n
	}
	items, cur := a.activitiesPage(before, qInt(r, "limit", 100), r.URL.Query().Get("type"), r.URL.Query().Get("actor"))
	return model.PageResponse[model.Activity]{Items: items, Cursor: cur}, 200, nil
}

// handleActivitiesExport: NDJSON of Activity for seq from..to (mesh read for bundles).
func (a *Agent) handleActivitiesExport(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	from := qInt(r, "from", 1)
	to := qInt(r, "to", 0)
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, x := range a.activitiesRange(from, to, 0) {
		line, _ := canonical.Canonicalize(toAny(x))
		_, _ = io.WriteString(w, line+"\n")
	}
	return nil, 0, nil
}

func (a *Agent) fetchActivities(r *http.Request, site string, from, to int) ([]model.Activity, error) {
	if site == a.site {
		return a.activitiesRange(from, to, 0), nil
	}
	path := fmt.Sprintf("/_opm/peer/site/activities/export?from=%d&to=%d", from, to)
	if to <= 0 {
		path = fmt.Sprintf("/_opm/peer/site/activities/export?from=%d", from)
	}
	resp, done, err := a.peerStream(r.Context(), site, "GET", path, nil, nil, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	defer done()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("site %s: status %d", site, resp.StatusCode)
	}
	var out []model.Activity
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var x model.Activity
		if json.Unmarshal(sc.Bytes(), &x) == nil {
			out = append(out, x)
		}
	}
	return out, sc.Err()
}

// handleDownloadActivities builds an .opmlog (§13.3) signed by THIS site (the agent the person opened).
func (a *Agent) handleDownloadActivities(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	from := qInt(r, "from", 1)
	to := qInt(r, "to", 0)
	inclDebug := qBool(r, "includeDebug")
	sites := []string{a.site}
	all := false
	// mounted at /_opm/api/sites/<self>/activities/download; "*" handled by handleSiteRouteAll
	if v := r.Context().Value(allSitesKey); v != nil {
		all = true
		sites = []string{}
		for _, s := range a.allSites() {
			if s == a.site || a.reachable(s) {
				sites = append(sites, s)
			}
		}
	}
	var entries []model.Activity
	ranges := []model.LogSiteRange{}
	for _, s := range sites {
		list, err := a.fetchActivities(r, s, from, to)
		if err != nil {
			return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
		}
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
		for i := range list {
			list[i].UploadedVia, list[i].UploadedAt = nil, nil
		}
		ranges = append(ranges, model.LogSiteRange{SiteSlug: s, FromSeq: list[0].Seq, ToSeq: list[len(list)-1].Seq, Count: len(list)})
		entries = append(entries, list...)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].SiteSlug != entries[j].SiteSlug {
			return entries[i].SiteSlug < entries[j].SiteSlug
		}
		return entries[i].Seq < entries[j].Seq
	})
	var nd bytes.Buffer
	for _, e := range entries {
		line, err := canonical.Canonicalize(toAny(e))
		if err != nil {
			return nil, 0, herr(model.ErrInternal, err.Error(), nil)
		}
		nd.WriteString(line + "\n")
	}
	debug := []model.LogDebugRef{}
	debugFiles := map[string][]byte{}
	if inclDebug {
		var fromT, toT time.Time
		if len(entries) > 0 {
			fromT = parseTs(entries[0].Ts).Add(-time.Minute)
			toT = time.Now()
		}
		for _, s := range sites {
			var b bytes.Buffer
			if s == a.site {
				_, _ = a.log.Export(&b, fromT, toT)
			} else {
				q := ""
				if !fromT.IsZero() {
					q = "?since=" + fromT.UTC().Format(time.RFC3339) + "&limit=5000"
				}
				var lr model.ListResponse[model.DebugLine]
				if err := a.fetchSiteJSON(r.Context(), r, s, "debug"+q, &lr); err == nil {
					for i := len(lr.Items) - 1; i >= 0; i-- {
						lb, _ := json.Marshal(lr.Items[i])
						b.Write(append(lb, '\n'))
					}
				}
			}
			p := "debug/" + s + ".jsonl"
			debugFiles[p] = b.Bytes()
			debug = append(debug, model.LogDebugRef{SiteSlug: s, Path: p, SizeBytes: int64(b.Len()), Sha256: canonical.Sha256Tagged(b.Bytes())})
		}
	}
	man := model.OpmlogManifest{Format: "opmlog/1", DeploymentID: a.dep, CreatedAt: nowTs(), CreatedBy: rc.actor,
		SignedBy: model.SignedBy{SiteSlug: a.site, PublicKey: sign.PublicKeyB64(a.pub)}, Sites: ranges,
		Activities: model.BundlePart{Path: "activities.ndjson", SizeBytes: int64(nd.Len()), Sha256: canonical.Sha256Tagged(nd.Bytes())}, Debug: debug}
	mj, err := canonical.Canonicalize(toAny(man))
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(a.key, []byte(mj)))
	name := "all"
	if !all {
		name = a.site
	}
	fromSeq, toSeq := from, to
	if len(ranges) > 0 {
		fromSeq, toSeq = ranges[0].FromSeq, ranges[0].ToSeq
		for _, rg := range ranges {
			if rg.FromSeq < fromSeq {
				fromSeq = rg.FromSeq
			}
			if rg.ToSeq > toSeq {
				toSeq = rg.ToSeq
			}
		}
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("%s-%s-%d-%d.opmlog", a.dep, name, fromSeq, toSeq)))
	tw := tar.NewWriter(w)
	_ = tarBytes(tw, "manifest.json", []byte(mj))
	_ = tarBytes(tw, "activities.ndjson", nd.Bytes())
	for _, d := range debug {
		_ = tarBytes(tw, d.Path, debugFiles[d.Path])
	}
	_ = tarBytes(tw, "signature.sig", []byte(sig))
	_ = tw.Close()
	if inclDebug {
		a.activity(rc.actorPtr(), "agent", "debug.downloaded", "activity bundle with debug log downloaded", actorData(rc, map[string]any{"sites": sites, "fromSeq": fromSeq, "toSeq": toSeq}))
	}
	return nil, 0, nil
}

type ctxAll int

const allSitesKey ctxAll = 1

var _ = strings.TrimSpace
