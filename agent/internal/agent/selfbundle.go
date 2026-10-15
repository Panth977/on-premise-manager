package agent

// Support bundle (SPEC-final §21 #90 a): POST …/agent/support-bundle issues a single-use 5-min link (the tools ticket
// store); GET …/agent/support-bundle/download?ticket=… builds opm-support-<site>-<yyyymmdd-hhmmss>.tar.gz with
// SUPPORT_BUNDLE_SECTIONS and writes agent.support-bundle-downloaded when the download starts.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opm/agent/internal/model"
)

func (a *Agent) handleSupportBundleTicket(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	c := a.toolsCaller(r)
	svc := a.Tools()
	if err := svc.Allow("bundle", c); err != nil {
		return nil, 0, toolsErr(err)
	}
	tk, exp := svc.IssueTicket("support-bundle", a.site, c, 5*time.Minute)
	return map[string]any{"url": "/_opm/api/sites/" + a.site + "/agent/support-bundle/download?ticket=" + url.QueryEscape(tk), "expiresAt": tsOf(exp)}, 200, nil
}

func (a *Agent) handleSupportBundleDownload(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	c := a.toolsCaller(r)
	_, actor, err := a.Tools().TakeTicket(r.URL.Query().Get("ticket"), "support-bundle", c)
	if err != nil {
		return nil, 0, toolsErr(err)
	}
	tmp, err := os.CreateTemp(filepath.Join(a.dataDir, "tmp"), "support-*.tar.gz")
	if err != nil {
		return nil, 0, herr(model.ErrInternal, err.Error(), nil)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sections, err := a.writeSupportBundle(ctx, tmp)
	if err != nil {
		return nil, 0, herr(model.ErrInternal, "support bundle: "+err.Error(), nil)
	}
	st, _ := tmp.Stat()
	_, _ = tmp.Seek(0, 0)
	name := fmt.Sprintf("opm-support-%s-%s.tar.gz", a.site, time.Now().UTC().Format("20060102-150405"))
	var ap *string
	if actor != "" {
		ap = &actor
	}
	data := map[string]any{"sizeBytes": st.Size(), "sections": sections}
	if c.SessionID != "" {
		data["sessionId"] = c.SessionID
	}
	if c.Via != "" {
		data["via"] = c.Via
	}
	a.activity(ap, "agent", "agent.support-bundle-downloaded", "support bundle downloaded ("+name+")", data)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = io.Copy(w, tmp)
	return nil, 0, nil
}

// stripAuths removes registry credentials ("auths" / "Auths" keys) anywhere in a JSON value.
func stripAuths(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if strings.EqualFold(k, "auths") || strings.EqualFold(k, "auth") || strings.EqualFold(k, "IdentityToken") {
				delete(x, k)
				continue
			}
			x[k] = stripAuths(vv)
		}
	case []any:
		for i := range x {
			x[i] = stripAuths(x[i])
		}
	}
	return v
}

// writeSupportBundle writes the tar.gz and returns the sections written.
func (a *Agent) writeSupportBundle(ctx context.Context, w io.Writer) ([]string, error) {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()
	sections := []string{}
	add := func(name string, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	addJSON := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return add(name, append(b, '\n'))
	}
	section := func(s string) { sections = append(sections, s) }
	// version.json: AgentSelfInfo without keys
	info := a.selfInfo(ctx)
	info.Enrollment.PublicKey = nil
	if err := addJSON("version.json", info); err != nil {
		return nil, err
	}
	section("version.json")
	// agent.json masked
	if o, _, e := a.readAgentJSON(); e == nil {
		if err := addJSON("agent.json", maskObject(o)); err != nil {
			return nil, err
		}
	} else if err := addJSON("agent.json", map[string]string{"error": e.msg}); err != nil {
		return nil, err
	}
	section("agent.json")
	// debug/: the last 24 h (JSONL, already redacted)
	var dbg bytes.Buffer
	cut := now.Add(-24 * time.Hour)
	var all bytes.Buffer
	_, _ = a.log.Export(&all, cut, time.Time{})
	for _, line := range bytes.Split(all.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var l struct {
			Ts string `json:"ts"`
		}
		if json.Unmarshal(line, &l) == nil && l.Ts != "" && parseTs(l.Ts).Before(cut) {
			continue
		}
		dbg.Write(line)
		dbg.WriteByte('\n')
	}
	if err := add("debug/agent-24h.jsonl", dbg.Bytes()); err != nil {
		return nil, err
	}
	section("debug/")
	// docker-info.json (no registry auths)
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	dinfo := map[string]any{}
	if raw, err := a.dc.GetRaw(dctx, "/info", nil); err == nil {
		var v any
		_ = json.Unmarshal(raw, &v)
		dinfo["info"] = stripAuths(v)
	} else {
		dinfo["infoError"] = err.Error()
	}
	if v, err := a.dc.Version(dctx); err == nil {
		dinfo["version"] = v
		dinfo["negotiatedApi"] = a.dc.APIVersion()
	} else {
		dinfo["versionError"] = err.Error()
	}
	if err := addJSON("docker-info.json", dinfo); err != nil {
		cancel()
		return nil, err
	}
	section("docker-info.json")
	// containers.json: OPM containers of this deployment (list output: labels, state, ports; no env)
	filters, _ := json.Marshal(map[string][]string{"label": {"opm.deployment=" + a.dep}})
	var containers any = []any{}
	if raw, err := a.dc.GetRaw(dctx, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}); err == nil {
		_ = json.Unmarshal(raw, &containers)
	} else {
		containers = map[string]string{"error": err.Error()}
	}
	cancel()
	if err := addJSON("containers.json", containers); err != nil {
		return nil, err
	}
	section("containers.json")
	// activities.jsonl: the last 500
	items, _ := a.activitiesPage(0, 500, "", "")
	var act bytes.Buffer
	for i := len(items) - 1; i >= 0; i-- {
		b, _ := json.Marshal(items[i])
		act.Write(b)
		act.WriteByte('\n')
	}
	if err := add("activities.jsonl", act.Bytes()); err != nil {
		return nil, err
	}
	section("activities.jsonl")
	// status.json
	sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
	st := a.siteStatus(sctx)
	scancel()
	if err := addJSON("status.json", st); err != nil {
		return nil, err
	}
	section("status.json")
	// upgrade/
	for _, n := range []string{"state.json", "history.json", "pending.json", "rolled-back.json", "run.json", "watch.log"} {
		if b, err := os.ReadFile(filepath.Join(a.self.paths.Dir, n)); err == nil {
			if len(b) > 4<<20 {
				b = b[len(b)-4<<20:]
			}
			if err := add("upgrade/"+n, b); err != nil {
				return nil, err
			}
		}
	}
	if err := addJSON("upgrade/current.json", model.AgentUpgradeStateResponse{Current: a.upgradeCurrent(), History: a.upgradeHistory()}); err != nil {
		return nil, err
	}
	section("upgrade/")
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return sections, gz.Close()
}
