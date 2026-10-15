package agent

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/driver"
	"opm/agent/internal/edge"
	"opm/agent/internal/model"
	"opm/agent/internal/store"
	"opm/agent/internal/validate"
)

// ───────────────────────── host metrics (§16)

var cpuPrev struct {
	sync.Mutex
	idle, total uint64
}

func (a *Agent) hostMetrics() model.HostMetrics {
	m := model.HostMetrics{}
	switch runtime.GOOS {
	case "linux":
		if f, err := os.Open("/proc/stat"); err == nil {
			sc := bufio.NewScanner(f)
			if sc.Scan() {
				fs := strings.Fields(sc.Text())
				var total, idle uint64
				for i, x := range fs[1:] {
					v, _ := strconv.ParseUint(x, 10, 64)
					total += v
					if i == 3 || i == 4 {
						idle += v
					}
				}
				cpuPrev.Lock()
				if cpuPrev.total > 0 && total > cpuPrev.total {
					m.CPUPct = 100 * (1 - float64(idle-cpuPrev.idle)/float64(total-cpuPrev.total))
				}
				cpuPrev.idle, cpuPrev.total = idle, total
				cpuPrev.Unlock()
			}
			f.Close()
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			kv := map[string]float64{}
			for _, ln := range strings.Split(string(b), "\n") {
				fs := strings.Fields(ln)
				if len(fs) >= 2 {
					v, _ := strconv.ParseFloat(fs[1], 64)
					kv[strings.TrimSuffix(fs[0], ":")] = v
				}
			}
			m.MemTotalMb = kv["MemTotal"] / 1024
			m.MemUsedMb = (kv["MemTotal"] - kv["MemAvailable"]) / 1024
		}
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if fs := strings.Fields(string(b)); len(fs) > 0 {
				if v, err := strconv.ParseFloat(fs[0], 64); err == nil {
					m.LoadAvg1 = &v
				}
			}
		}
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
			m.MemTotalMb = v / (1024 * 1024)
		}
		if out, err := exec.Command("vm_stat").Output(); err == nil {
			pages := map[string]float64{}
			page := 16384.0
			for _, ln := range strings.Split(string(out), "\n") {
				if strings.Contains(ln, "page size of") {
					fs := strings.Fields(ln)
					for i, f := range fs {
						if f == "of" && i+1 < len(fs) {
							page, _ = strconv.ParseFloat(fs[i+1], 64)
						}
					}
					continue
				}
				k, v, ok := strings.Cut(ln, ":")
				if ok {
					n, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "."), 64)
					pages[strings.TrimSpace(k)] = n
				}
			}
			used := pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]
			m.MemUsedMb = used * page / (1024 * 1024)
		}
		if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
			fs := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
			if len(fs) > 0 {
				if v, err := strconv.ParseFloat(fs[0], 64); err == nil {
					m.LoadAvg1 = &v
					m.CPUPct = 100 * v / float64(runtime.NumCPU())
					if m.CPUPct > 100 {
						m.CPUPct = 100
					}
				}
			}
		}
	}
	total, free := diskOf(a.dataDir)
	if total > 0 {
		m.DiskUsedPct = 100 * (1 - float64(free)/float64(total))
	}
	m.DiskFreeMb = float64(free) / (1024 * 1024)
	return m
}

// ───────────────────────── containers

func (a *Agent) containerCounts() model.ContainerCounts {
	cfg, _ := a.headConfigRO()
	s := siteOf(cfg, a.site)
	cc := model.ContainerCounts{}
	if s == nil {
		return cc
	}
	for _, c := range s.Config.Containers {
		if !c.Enabled {
			continue
		}
		cc.Total++
		if a.drv.Ready(c.ContainerSlug) {
			cc.Ready++
		}
	}
	return cc
}

func (a *Agent) containerStatuses(ctx context.Context) []model.ContainerStatus {
	out := []model.ContainerStatus{}
	hr := a.headRevision()
	var site *model.Site
	cat := emptyCatalog()
	if hr != nil {
		site = siteOf(&hr.Config, a.site)
		cat = &hr.Catalog
	}
	existing, _ := a.drv.Existing(ctx)
	seen := map[string]bool{}
	if site != nil {
		builts, _ := a.buildSite(hr, site)
		bmap := map[string]*driver.Built{}
		for _, b := range builts {
			bmap[b.ContainerSlug] = b
		}
		for _, c := range site.Config.Containers {
			seen[c.ContainerSlug] = true
			cs := model.ContainerStatus{SiteSlug: a.site, ContainerSlug: c.ContainerSlug, ServiceSlug: c.ServiceSlug, Version: c.Version, Image: c.ServiceSlug + ":" + c.Version, Enabled: c.Enabled, Managed: true,
				State: "missing", Health: "none", Ports: []model.ContainerPortStatus{}}
			if img, ok := cat.Images[validate.ImageKey(c.ServiceSlug, c.Version)]; ok {
				cs.Kind = img.Kind
				cs.Image, _, _ = driver.ReadableRef(img)
			}
			h := a.drv.HealthOf(c.ContainerSlug)
			cs.Health, cs.Ready, cs.Restarts = h.Health, h.Ready, h.Restarts
			if ex, ok := existing[c.ContainerSlug]; ok {
				a.fillContainer(ctx, &cs, ex, bmap[c.ContainerSlug])
			} else if !c.Enabled {
				cs.State = "disabled"
			}
			out = append(out, cs)
		}
	}
	for sid, ex := range existing {
		if seen[sid] {
			continue
		}
		cs := model.ContainerStatus{SiteSlug: a.site, ContainerSlug: sid, ServiceSlug: ex.Service, Managed: true, State: ex.State, Health: "none", Ports: []model.ContainerPortStatus{}}
		a.fillContainer(ctx, &cs, ex, nil)
		out = append(out, cs)
	}
	// containers on this host that OPM does not manage (greyed on the status page)
	if list, err := a.dc.ContainerList(ctx, true); err == nil {
		for _, c := range list {
			if c.Labels[driver.LDeployment] == a.dep {
				continue
			}
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			out = append(out, model.ContainerStatus{SiteSlug: a.site, ContainerSlug: name, ServiceSlug: "", Image: c.Image, Kind: "service", Managed: false,
				State: c.State, Health: "none", Ports: []model.ContainerPortStatus{}})
		}
	}
	return out
}

func (a *Agent) fillContainer(ctx context.Context, cs *model.ContainerStatus, ex driver.Existing, b *driver.Built) {
	cj, err := a.dc.ContainerInspect(ctx, ex.ID)
	if err != nil {
		return
	}
	cs.State = cj.State.Status
	if cs.Image == "" {
		cs.Image = cj.Config.Image
	}
	if !cj.State.Running {
		code := cj.State.ExitCode
		cs.ExitCode = &code
	}
	if cj.State.StartedAt != "" && !strings.HasPrefix(cj.State.StartedAt, "0001") {
		st := cj.State.StartedAt
		cs.StartedAt = &st
	}
	cs.Restarts = cj.RestartCount
	h := ex.Hash
	cs.SpecHash = &h
	if ex.RevSeq > 0 {
		s := ex.RevSeq
		cs.RevSeq = &s
	}
	if b != nil {
		for _, p := range b.Ports {
			ps := model.ContainerPortStatus{PortName: p.Name, ContainerPort: p.ContainerPort, HostPort: p.HostPort, Protocol: p.Protocol}
			cs.Ports = append(cs.Ports, ps)
		}
	}
	if cj.State.Running {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if st, err := a.dc.ContainerStats(sctx, ex.ID); err == nil {
			cpu, mem := st.CPUPct, st.MemMb
			cs.CPUPct, cs.MemMb = &cpu, &mem
		}
		cancel()
	}
}

func (a *Agent) handleSiteStatus(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	st := a.siteStatus(r.Context())
	return model.ListResponse[model.SiteStatus]{Items: []model.SiteStatus{st}}, 200, nil
}

func (a *Agent) siteStatus(ctx context.Context) model.SiteStatus {
	hm := a.hostMetrics()
	v := a.version
	now := nowTs()
	er := a.edgeReport()
	st := model.SiteStatus{SiteSlug: a.site, Reachable: true, LastSeenAt: &now, AgentVersion: &v, Host: &hm, Containers: a.containerStatuses(ctx),
		Uptime24h: a.uptime24h(), Uptime30d: a.uptime30d(), Certs: er.Certs, Edge: &er}
	if w := a.selfWarnings(); len(w) > 0 { // §21 #112
		st.AgentWarnings = w
	}
	return st
}

var statusCache sync.Map // instance/site → model.SiteStatus (cache of last answers; keyed per agent instance)

func (a *Agent) rememberStatus(site string, s model.SiteStatus) {
	statusCache.Store(a.instance+"/"+site, s)
}

func (a *Agent) cachedStatus(site string) model.SiteStatus {
	if v, ok := statusCache.Load(a.instance + "/" + site); ok {
		s := v.(model.SiteStatus)
		s.Reachable = false
		return s
	}
	s := model.SiteStatus{SiteSlug: site, Reachable: false, Containers: []model.ContainerStatus{}, Uptime24h: []model.UptimeSeries{}, Uptime30d: []model.UptimeSeries{}, Certs: []model.CertStatus{}}
	if p, ok := a.peerRow(site); ok && p.LastAnswerAt > 0 {
		t := tsOf(time.Unix(p.LastAnswerAt, 0))
		s.LastSeenAt = &t
		if p.LastPing != nil {
			v := p.LastPing.AgentVersion
			s.AgentVersion = &v
		}
	}
	return s
}

func (a *Agent) handleContainerLogs(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	sid := r.PathValue("containerSlug")
	tail := qInt(r, "tail", 200)
	if tail <= 0 {
		tail = 200
	}
	if tail > 5000 {
		tail = 5000
	}
	ex, err := a.drv.Existing(r.Context())
	if err != nil {
		return nil, 0, herr(model.ErrUnavailable, "docker: "+err.Error(), nil)
	}
	e, ok := ex[sid]
	if !ok {
		return nil, 0, herr(model.ErrNotFound, "no container "+sid+" on site "+a.site, nil)
	}
	since := ""
	if s := r.URL.Query().Get("since"); s != "" {
		if t := parseTs(s); !t.IsZero() {
			since = strconv.FormatInt(t.Unix(), 10)
		}
	}
	lines, err := a.dc.ContainerLogs(r.Context(), e.ID, tail, since)
	if err != nil {
		return nil, 0, herr(model.ErrUnavailable, "docker: "+err.Error(), nil)
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	out := []model.LogLine{}
	for _, l := range lines {
		if q != "" && !strings.Contains(strings.ToLower(validate.StripANSI(l.Text)), q) { // §21 #93: search ignores escape codes
			continue
		}
		out = append(out, model.LogLine{Ts: l.Ts, Stream: l.Stream, Text: l.Text})
	}
	return model.ContainerLogsResponse{SiteSlug: a.site, ContainerSlug: sid, ServiceSlug: e.Service, Lines: out}, 200, nil
}

func (a *Agent) handleSiteDebug(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	q := dlog.Query{Level: r.URL.Query().Get("level"), Module: r.URL.Query().Get("module"), Q: r.URL.Query().Get("q"), Limit: qInt(r, "limit", 500)}
	if s := r.URL.Query().Get("since"); s != "" {
		q.Since = parseTs(s)
	}
	return model.ListResponse[model.DebugLine]{Items: a.log.Read(q)}, 200, nil
}

// ───────────────────────── uptime (sampler 30 s, rollup 1 h)

func (a *Agent) sampleOnce(ctx context.Context) {
	cfg, _ := a.headConfigRO()
	s := siteOf(cfg, a.site)
	if s == nil {
		return
	}
	ts := time.Now().Unix()
	allUp := true
	for _, c := range s.Config.Containers {
		if !c.Enabled {
			continue
		}
		h := a.drv.HealthOf(c.ContainerSlug)
		state := "down"
		if h.ContainerID != "" {
			if h.Ready {
				state = "up"
			} else if running, _, err := a.drv.IsRunning(ctx, h.ContainerID); err == nil && running {
				state = "degraded"
			}
		}
		if state != "up" {
			allUp = false
		}
		_, _ = a.db.Exec(`INSERT OR REPLACE INTO uptime_raw(ts, subject, state, restarts) VALUES(?,?,?,?)`, ts, c.ContainerSlug, state, h.Restarts)
	}
	st := "up"
	if !allUp {
		st = "down"
	}
	_, _ = a.db.Exec(`INSERT OR REPLACE INTO uptime_raw(ts, subject, state) VALUES(?,?,?)`, ts, "site", st)
}

func (a *Agent) rollupOnce(ctx context.Context) {
	now := time.Now().Unix()
	cur := now - now%3600
	rows, err := a.db.Query(`SELECT (ts - ts % 3600) AS hour, subject, SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), COUNT(*) FROM uptime_raw WHERE ts < ? GROUP BY hour, subject`, cur+3600)
	if err == nil {
		type agg struct {
			hour      int64
			subj      string
			up, total int
		}
		var list []agg
		for rows.Next() {
			var x agg
			if rows.Scan(&x.hour, &x.subj, &x.up, &x.total) == nil {
				list = append(list, x)
			}
		}
		rows.Close()
		for _, x := range list {
			frac := float64(x.up) / float64(x.total)
			down := int64((1 - frac) * 3600)
			_, _ = a.db.Exec(`INSERT OR REPLACE INTO uptime_hourly(hour, subject, upFraction, downSeconds, samples) VALUES(?,?,?,?,?)`, x.hour, x.subj, frac, down, x.total)
		}
	}
	_, _ = a.db.Exec(`DELETE FROM uptime_raw WHERE ts < ?`, now-24*3600)
	_, _ = a.db.Exec(`DELETE FROM uptime_hourly WHERE hour < ?`, now-30*24*3600)
}

func (a *Agent) uptimeSubjects() []string {
	subs := []string{"site"}
	cfg, _ := a.headConfig()
	if s := siteOf(cfg, a.site); s != nil {
		for _, c := range s.Config.Containers {
			subs = append(subs, c.ContainerSlug)
		}
	}
	return subs
}

// uptime24h: 144 cells of 10 min.
func (a *Agent) uptime24h() []model.UptimeSeries {
	now := time.Now().Unix()
	start := now - now%600 - 143*600
	out := []model.UptimeSeries{}
	for _, subj := range a.uptimeSubjects() {
		cells := make([]model.UptimeCell, 144)
		for i := range cells {
			cells[i] = model.UptimeCell{T: tsOf(time.Unix(start+int64(i)*600, 0))}
		}
		rows, err := a.db.Query(`SELECT (ts - ts % 600), SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), COUNT(*) FROM uptime_raw WHERE subject = ? AND ts >= ? GROUP BY ts - ts % 600`, subj, start)
		if err == nil {
			for rows.Next() {
				var b int64
				var up, n int
				if rows.Scan(&b, &up, &n) == nil {
					i := int((b - start) / 600)
					if i >= 0 && i < 144 && n > 0 {
						cells[i].Up, cells[i].Samples = float64(up)/float64(n), n
					}
				}
			}
			rows.Close()
		}
		out = append(out, model.UptimeSeries{Subject: subj, Cells: cells})
	}
	return out
}

// uptime30d: 720 cells of 1 h.
func (a *Agent) uptime30d() []model.UptimeSeries {
	now := time.Now().Unix()
	start := now - now%3600 - 719*3600
	out := []model.UptimeSeries{}
	for _, subj := range a.uptimeSubjects() {
		cells := make([]model.UptimeCell, 720)
		for i := range cells {
			cells[i] = model.UptimeCell{T: tsOf(time.Unix(start+int64(i)*3600, 0))}
		}
		rows, err := a.db.Query(`SELECT hour, upFraction, samples FROM uptime_hourly WHERE subject = ? AND hour >= ?`, subj, start)
		if err == nil {
			for rows.Next() {
				var h int64
				var f float64
				var n int
				if rows.Scan(&h, &f, &n) == nil {
					i := int((h - start) / 3600)
					if i >= 0 && i < 720 {
						cells[i].Up, cells[i].Samples = f, n
					}
				}
			}
			rows.Close()
		}
		// the current hour from raw samples
		var up, n sql.NullInt64
		_ = a.db.QueryRow(`SELECT SUM(CASE WHEN state = 'up' THEN 1 ELSE 0 END), COUNT(*) FROM uptime_raw WHERE subject = ? AND ts >= ?`, subj, now-now%3600).Scan(&up, &n)
		if n.Int64 > 0 {
			cells[719].Up, cells[719].Samples = float64(up.Int64)/float64(n.Int64), int(n.Int64)
		}
		out = append(out, model.UptimeSeries{Subject: subj, Cells: cells})
	}
	return out
}

// ───────────────────────── certificates

func (a *Agent) handleListCerts(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	site := r.URL.Query().Get("site")
	if site == "" || site == a.site {
		return model.ListResponse[model.CertStatus]{Items: a.certStatuses()}, 200, nil
	}
	sites := []string{site}
	if site == "*" {
		sites = a.allSites()
	}
	items := []model.CertStatus{}
	for _, s := range sites {
		if s == a.site {
			items = append(items, a.certStatuses()...)
			continue
		}
		if !a.reachable(s) {
			continue
		}
		var lr model.ListResponse[model.CertStatus]
		if err := a.fetchSiteJSON(r.Context(), r, s, "certs", &lr); err == nil {
			items = append(items, lr.Items...)
		}
	}
	return model.ListResponse[model.CertStatus]{Items: items}, 200, nil
}

func (a *Agent) handleRenewCertAny(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	site := r.URL.Query().Get("site")
	if site == "" || site == a.site {
		return a.handleRenewCert(w, r)
	}
	if !a.reachable(site) {
		return nil, 0, herr(model.ErrUnavailable, "site "+site+" is not reachable", nil)
	}
	a.forwardSite(w, r, site, "certs/"+r.PathValue("hostname")+"/renew")
	return nil, 0, nil
}

func (a *Agent) handleRenewCert(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	host := r.PathValue("hostname")
	hr := a.headRevision()
	if hr == nil {
		return nil, 0, herr(model.ErrNotFound, "no head", nil)
	}
	site := siteOf(&hr.Config, a.site)
	if site == nil {
		return nil, 0, herr(model.ErrNotFound, "this site is not in the head config", nil)
	}
	re, err := a.resolveEdge(&hr.Config, site)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	var h *model.EdgeHost
	for i := range re.Hosts {
		if strings.EqualFold(re.Hosts[i].Hostname, host) {
			h = &re.Hosts[i]
		}
	}
	if h == nil {
		return nil, 0, herr(model.ErrNotFound, "host "+host+" is not on this edge", nil)
	}
	if h.TLS.Mode == "none" {
		return nil, 0, invalid("host "+host+" has TLS mode none", nil)
	}
	a.mu.Lock()
	if t, ok := a.revealAt["renew:"+host]; ok && time.Since(t) < time.Minute {
		a.mu.Unlock()
		e := herr(model.ErrRateLimited, "renew at most once per minute", nil)
		e.header = map[string]string{"Retry-After": "60"}
		return nil, 0, e
	}
	a.revealAt["renew:"+host] = time.Now()
	a.mu.Unlock()
	root := filepath.Join(a.dataDir, "certs")
	if h.TLS.Mode != "files" {
		edge.RemoveStoredCert(root, h.Hostname)
	}
	in, err := a.edgeInput(&hr.Config, &hr.Catalog)
	status := "renewed"
	if err == nil && in != nil {
		_, err = edge.Apply(a.instance, root, in.Build(""), true)
	}
	if err == nil && h.TLS.Mode != "files" {
		for i := 0; i < 50; i++ {
			if cs := edge.CertStatusFor(root, a.site, *h, nil); cs.NotAfter != nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	data := actorData(rc, map[string]any{"hostname": host, "mode": h.TLS.Mode, "actor": rc.actor})
	if err != nil {
		status = "failed"
		e := err.Error()
		a.mu.Lock()
		a.certErrs[h.Hostname] = e
		a.mu.Unlock()
		data["error"] = e
		a.activity(rc.actorPtr(), "agent", "cert.failed", "certificate renewal failed for "+host, data)
	} else {
		a.mu.Lock()
		delete(a.certErrs, h.Hostname)
		a.mu.Unlock()
		cs := edge.CertStatusFor(root, a.site, *h, nil)
		if cs.Issuer != nil {
			data["issuer"] = *cs.Issuer
		}
		if cs.NotAfter != nil {
			data["notAfter"] = *cs.NotAfter
		}
		a.activity(rc.actorPtr(), "agent", "cert.renewed", "certificate renewed for "+host, data)
	}
	var le *string
	if err != nil {
		s := err.Error()
		le = &s
	}
	return model.CertRenewResponse{Status: status, Cert: edge.CertStatusFor(root, a.site, *h, le)}, 200, nil
}

func (a *Agent) handleLocalCAAny(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	site := r.URL.Query().Get("site")
	if site == "" || site == a.site {
		return a.handleLocalCA(w, r)
	}
	if !a.reachable(site) {
		return nil, 0, herr(model.ErrUnavailable, "site "+site+" is not reachable", nil)
	}
	resp, done, err := a.peerStream(r.Context(), site, "GET", "/_opm/peer/site/local-ca.crt", nil, nil, 15*time.Second)
	if err != nil {
		return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
	}
	defer done()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = bufio.NewReader(resp.Body).WriteTo(w)
	return nil, 0, nil
}

func (a *Agent) handleLocalCA(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	b, err := os.ReadFile(edge.LocalCAPath(filepath.Join(a.dataDir, "certs")))
	if err != nil {
		return nil, 0, herr(model.ErrNotFound, "this site has no local CA (no host with TLS internal)", nil)
	}
	rc := getRC(r)
	a.activity(rc.actorPtr(), "agent", "cert.local-ca-downloaded", "local CA downloaded", map[string]any{"hostname": "", "mode": "internal", "actor": rc.actor})
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.site+"-local-ca.crt"))
	_, _ = w.Write(b)
	return nil, 0, nil
}

// ───────────────────────── housekeeping (hourly)

func (a *Agent) housekeeping(ctx context.Context) {
	// §21 #112 the cheap, local work first: a slow Docker call below never delays log rotation or tmp cleanup
	a.log.Rotate()
	for _, f := range a.log.Files() {
		_, _ = a.db.Exec(`INSERT OR REPLACE INTO debug_index(day, file, sizeBytes, lines) VALUES(?,?,?,0)`, f.Day, f.Path, f.SizeBytes)
	}
	tmp := filepath.Join(a.dataDir, "tmp")
	ents, _ := os.ReadDir(tmp)
	for _, e := range ents {
		if i, err := e.Info(); err == nil && time.Since(i.ModTime()) > 24*time.Hour {
			_ = os.RemoveAll(filepath.Join(tmp, e.Name()))
		}
	}
	a.cleanupSessions()
	a.cleanupStages() // §21 #107 abandoned commit stages (> 24 h)
	a.pruneMemory()   // §21 #112
	a.pruneBlobs()
	a.cleanupFailedContainers(ctx, false) // kept failed containers older than 24 h (§21 #87)
	a.pruneImages(ctx)
	_ = store.JSON
}

// pruneMemory drops in-memory entries nothing needs any more (§21 #112: these maps only grew): update runs, site
// states and coordinator contacts of updates finished more than a day ago, staging details of revisions without an
// open proposal, rate-limit windows and reveal times older than an hour, peer have-lists older than 10 minutes.
func (a *Agent) pruneMemory() {
	done := map[string]bool{}
	if rows, err := a.db.Query(`SELECT updateId FROM updates WHERE finishedAt IS NOT NULL AND finishedAt < ?`, time.Now().Add(-24*time.Hour).Unix()); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				done[id] = true
			}
		}
		rows.Close()
	}
	open := map[string]bool{}
	for _, p := range a.openProposals() {
		open[p.RevID] = true
	}
	hourAgo := time.Now().Add(-time.Hour)
	a.mu.Lock()
	for id := range a.runs {
		if done[id] {
			delete(a.runs, id)
		}
	}
	for id := range a.siteStates {
		if done[id] {
			delete(a.siteStates, id)
		}
	}
	for id, t := range a.coordLast {
		if done[id] || t.Before(time.Now().Add(-24*time.Hour)) {
			delete(a.coordLast, id)
		}
	}
	for id := range a.details {
		if !open[id] {
			delete(a.details, id)
		}
	}
	for k, ts := range a.actorRate {
		if len(ts) == 0 || ts[len(ts)-1].Before(hourAgo) {
			delete(a.actorRate, k)
		}
	}
	for k, t := range a.revealAt {
		if t.Before(hourAgo) {
			delete(a.revealAt, k)
		}
	}
	for k, e := range a.haveCache {
		if e == nil || time.Since(e.at) > 10*time.Minute {
			delete(a.haveCache, k)
		}
	}
	a.mu.Unlock()
	a.volMu.Lock()
	for k, ts := range a.volRate {
		if len(ts) == 0 || ts[len(ts)-1].Before(hourAgo) {
			delete(a.volRate, k)
		}
	}
	a.volMu.Unlock()
}
