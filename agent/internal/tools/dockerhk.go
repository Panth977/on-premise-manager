package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/metrics"
)

// ───────────────────────── info

func (s *Service) handleInfo(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	user, shell := s.cfg.shellFor()
	var dv *string
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if v, err := s.h.Docker().Version(ctx); err == nil {
		dv = &v.Version
	}
	hsReason, ceReason := s.cfg.hostShellReason(), s.cfg.containerExecReason()
	var cp *string
	if s.cfg.Path != "" {
		p := s.cfg.Path
		cp = &p
	}
	return map[string]any{"siteSlug": s.h.Site(), "os": runtime.GOOS, "arch": runtime.GOARCH, "agentUser": agentUser(), "dockerVersion": dv,
		"hostShell":      map[string]any{"enabled": hsReason == "", "reason": nilIfEmpty(hsReason), "user": user, "shell": shell},
		"containerExec":  map[string]any{"enabled": ceReason == "", "reason": nilIfEmpty(ceReason)},
		"limits":         map[string]any{"maxSessions": maxSessions, "idleTimeoutSec": int(s.term.idle.Seconds()), "transcriptDays": transcriptDays, "viewMaxBytes": viewMax},
		"activeSessions": s.ActiveSessions(),
		"metrics":        map[string]any{"intervalSec": int(s.Metrics.Interval.Seconds()), "rawSec": 3600, "minuteSec": 86400, "quarterSec": 7 * 86400},
		"configPath":     cp}, 200, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ───────────────────────── monitoring

func (s *Service) handleMetricsSummary(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var order []string
	if ts, err := s.h.Targets(r.Context()); err == nil {
		for _, t := range ts {
			order = append(order, t.Ref)
		}
	}
	return s.Metrics.Summary(s.h.Site(), order), 200, nil
}

func (s *Service) handleMetricsSeries(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	q := r.URL.Query()
	subj := q.Get("subject")
	if subj == "" {
		subj = "host"
	}
	res, err := s.Metrics.Series(s.h.Site(), subj, q.Get("range"), q.Get("metrics"))
	if err != nil {
		var br *metrics.ErrBadRange
		if errors.As(err, &br) {
			return nil, 0, invalid("%s", br.Msg)
		}
		return nil, 0, unavailable(err)
	}
	return res, 200, nil
}

// ───────────────────────── docker housekeeping

func (s *Service) diskUsage(ctx context.Context) *docker.DiskUsage {
	s.mu.Lock()
	if s.dfCache.val != nil && time.Since(s.dfCache.at) < 30*time.Second {
		v := s.dfCache.val
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	df, err := s.h.Docker().SystemDF(cctx)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	s.dfCache.at, s.dfCache.val = time.Now(), df
	s.mu.Unlock()
	return df
}

type imageRow struct {
	ID         string           `json:"id"`
	Tags       []string         `json:"tags"`
	SizeBytes  int64            `json:"sizeBytes"`
	Created    string           `json:"created"`
	Containers []map[string]any `json:"containers"`
	InUse      bool             `json:"inUse"`
	OPM        bool             `json:"opm"`
	Protected  bool             `json:"protected"`
}

func (s *Service) imageRows(ctx context.Context) ([]imageRow, error) {
	dc := s.h.Docker()
	imgs, err := dc.Images(ctx)
	if err != nil {
		return nil, err
	}
	conts, err := dc.ContainerList(ctx, true)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{} // container id → ref
	if ts, err := s.h.Targets(ctx); err == nil {
		for _, t := range ts {
			if t.ID != "" {
				refs[t.ID] = t.Ref
			}
		}
	}
	byImage := map[string][]map[string]any{}
	for _, c := range conts {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var ref *string
		if r, ok := refs[c.ID]; ok {
			ref = &r
		}
		byImage[c.ImageID] = append(byImage[c.ImageID], map[string]any{"name": name, "ref": ref, "state": c.State})
	}
	head := s.h.HeadImages()
	out := []imageRow{}
	for _, im := range imgs {
		tags := []string{}
		for _, t := range im.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		row := imageRow{ID: im.ID, Tags: tags, SizeBytes: im.Size, Created: ts(time.Unix(im.Created, 0)), Containers: byImage[im.ID]}
		if row.Containers == nil {
			row.Containers = []map[string]any{}
		}
		row.InUse = len(row.Containers) > 0 || im.Containers > 0
		for _, t := range tags {
			if strings.HasPrefix(t, "opm/") || head[t] {
				row.OPM = true
			}
			if head[t] {
				row.Protected = true
			}
		}
		if im.Labels["opm.deployment"] != "" {
			row.OPM = true
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InUse != out[j].InUse {
			return out[i].InUse
		}
		return out[i].SizeBytes > out[j].SizeBytes
	})
	return out, nil
}

func (s *Service) handleImages(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	rows, err := s.imageRows(r.Context())
	if err != nil {
		return nil, 0, unavailable(err)
	}
	var dv *string
	if v, err := s.h.Docker().Version(r.Context()); err == nil {
		dv = &v.Version
	}
	var df any
	if u := s.diskUsage(r.Context()); u != nil {
		df = dfView(u)
	}
	return map[string]any{"siteSlug": s.h.Site(), "dockerVersion": dv, "items": rows, "df": df}, 200, nil
}

func dfView(u *docker.DiskUsage) map[string]any {
	type agg struct{ count, size, recl int64 }
	var im, co, vo, bc agg
	for _, i := range u.Images {
		im.count++
		im.size += i.Size - i.SharedSize
		if i.Containers == 0 {
			im.recl += i.Size - i.SharedSize
		}
	}
	im.size += 0
	if u.LayersSize > 0 {
		im.size = u.LayersSize
	}
	for _, c := range u.Containers {
		co.count++
		co.size += c.SizeRw
		if c.State != "running" {
			co.recl += c.SizeRw
		}
	}
	for _, v := range u.Volumes {
		vo.count++
		if v.UsageData != nil && v.UsageData.Size > 0 {
			vo.size += v.UsageData.Size
			if v.UsageData.RefCount == 0 {
				vo.recl += v.UsageData.Size
			}
		}
	}
	for _, b := range u.BuildCache {
		bc.count++
		bc.size += b.Size
		if !b.InUse && !b.Shared {
			bc.recl += b.Size
		}
	}
	if im.recl > im.size {
		im.recl = im.size
	}
	v := func(a agg) map[string]any {
		return map[string]any{"count": a.count, "sizeBytes": a.size, "reclaimableBytes": a.recl}
	}
	return map[string]any{"images": v(im), "containers": v(co), "volumes": v(vo), "buildCache": v(bc)}
}

type pruneReq struct {
	Typed string `json:"typed"`
}

func (s *Service) handlePrune(w http.ResponseWriter, r *http.Request, c Caller) (any, int, error) {
	var req pruneReq
	if e := readBody(r, &req); e != nil {
		return nil, 0, e
	}
	if strings.TrimSpace(req.Typed) != "PRUNE" {
		return nil, 0, &Error{Status: 422, Code: "invalid", Msg: `type PRUNE to remove the unused images`,
			Details: map[string]any{"errors": []map[string]string{{"path": "typed", "code": "V_CONFIRM", "message": "type PRUNE", "severity": "error"}}}}
	}
	if e := s.rl.allow("prune", c); e != nil {
		return nil, 0, e
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rows, err := s.imageRows(ctx)
	if err != nil {
		return nil, 0, unavailable(err)
	}
	deleted := []map[string]any{}
	skipped := []map[string]any{}
	var reclaimed int64
	for _, im := range rows {
		if im.InUse {
			continue
		}
		if im.Protected {
			skipped = append(skipped, map[string]any{"id": im.ID, "tags": im.Tags, "reason": "used by the head revision of this site"})
			continue
		}
		if err := s.h.Docker().ImageDelete(ctx, im.ID); err != nil {
			reason := err.Error()
			var ae *docker.APIError
			if errors.As(err, &ae) && ae.Status == 409 {
				reason = "in use (" + ae.Message + ")"
			}
			skipped = append(skipped, map[string]any{"id": im.ID, "tags": im.Tags, "reason": reason})
			continue
		}
		reclaimed += im.SizeBytes
		deleted = append(deleted, map[string]any{"id": im.ID, "tags": im.Tags, "sizeBytes": im.SizeBytes})
	}
	s.mu.Lock()
	s.dfCache.val = nil
	s.mu.Unlock()
	s.activity(c, "docker.images-pruned", fmt.Sprintf("%d unused images removed on %s (%s)", len(deleted), s.h.Site(), humanBytes(reclaimed)),
		map[string]any{"deleted": len(deleted), "skipped": len(skipped), "reclaimedBytes": reclaimed, "siteSlug": s.h.Site()})
	s.h.Log().Info("tools", "unused images pruned", "deleted", len(deleted), "reclaimedBytes", reclaimed, "actor", c.Actor)
	return map[string]any{"deleted": deleted, "skipped": skipped, "reclaimedBytes": reclaimed}, 200, nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
