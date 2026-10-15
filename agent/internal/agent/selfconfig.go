package agent

// agent.json view + safe-settings editor (SPEC-final §21 #90 a): GET …/agent/config = agent.json with secrets masked
// (keys matching /token|secret|password|key$/i except releasePublicKey → "••••" + last 4) + the editable settings;
// PATCH validates with model.ValidateAgentSettings (= TS validateAgentSettings), keeps unknown keys and the key order,
// writes agent.json.bak + an atomic rename, writes agent.config-changed, and restarts unless restart:false.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"opm/agent/internal/model"
	"opm/agent/internal/sdsock"
	"opm/agent/internal/selfupgrade"
)

var secretKeyRE = regexp.MustCompile(`(?i)(token|secret|password|key$)`)

func maskValue(raw json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		r := []rune(s)
		tail := ""
		if len(r) > 4 {
			tail = string(r[len(r)-4:])
		}
		b, _ := json.Marshal("••••" + tail)
		return b
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return raw
	}
	b, _ := json.Marshal("••••")
	return b
}

// maskObject masks secret-looking keys recursively (objects only; arrays are kept as they are).
func maskObject(o model.OrderedObject) model.OrderedObject {
	out := make(model.OrderedObject, 0, len(o))
	for _, f := range o {
		v := f.Value
		if secretKeyRE.MatchString(f.Key) && f.Key != "releasePublicKey" {
			v = maskValue(v)
		} else if t := bytes.TrimSpace(v); len(t) > 0 && t[0] == '{' {
			var sub model.OrderedObject
			if json.Unmarshal(v, &sub) == nil {
				v, _ = json.Marshal(maskObject(sub))
			}
		}
		out = append(out, model.OrderedField{Key: f.Key, Value: v})
	}
	return out
}

// settingsOf reads the editable settings from agent.json (defaults: tools on, plain names, edge 80, alias 8080).
func settingsOf(o model.OrderedObject) model.AgentEditableSettings {
	ip := func(v int) *int { return &v }
	s := model.AgentEditableSettings{Tools: model.AgentToolsSettings{HostShell: true, ContainerExec: true, DevTools: true}, ContainerNames: "plain", EdgeHTTPPort: ip(80), GuiAliasPort: ip(8080)}
	port := func(key string, dst **int) {
		raw, ok := o.Get(key)
		if !ok {
			return
		}
		var v *int
		if json.Unmarshal(raw, &v) == nil {
			if v != nil && *v <= 0 {
				v = nil
			}
			*dst = v
		}
	}
	port("edgeHttpPort", &s.EdgeHTTPPort)
	port("guiAliasPort", &s.GuiAliasPort)
	if raw, ok := o.Get("containerNames"); ok {
		var n string
		if json.Unmarshal(raw, &n) == nil && n != "" {
			s.ContainerNames = n
		}
	}
	if raw, ok := o.Get("tools"); ok {
		var t struct {
			HostShell     *bool `json:"hostShell"`
			ContainerExec *bool `json:"containerExec"`
			DevTools      *bool `json:"devTools"`
		}
		if json.Unmarshal(raw, &t) == nil {
			if t.HostShell != nil {
				s.Tools.HostShell = *t.HostShell
			}
			if t.ContainerExec != nil {
				s.Tools.ContainerExec = *t.ContainerExec
			}
			if t.DevTools != nil {
				s.Tools.DevTools = *t.DevTools
			}
		}
	}
	return s
}

func (a *Agent) readAgentJSON() (model.OrderedObject, []byte, *httpError) {
	p := a.opt.ConfigPath
	if p == "" {
		return nil, nil, herr(model.ErrUnavailable, "this agent runs without an agent.json path (--config)", nil)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, herr(model.ErrUnavailable, "read "+p+": "+err.Error(), nil)
	}
	var o model.OrderedObject
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, nil, herr(model.ErrUnavailable, p+" is not a JSON object: "+err.Error(), nil)
	}
	return o, raw, nil
}

// configWritable: the file and its directory (atomic rename + agent.json.bak) are writable by this process.
func configWritable(p string) (bool, *string) {
	if err := syscall.Access(p, 2); err != nil {
		r := fmt.Sprintf("%s is not writable by the agent (%v): edit it by hand and restart", p, err)
		return false, &r
	}
	if err := syscall.Access(filepath.Dir(p), 3); err != nil {
		r := fmt.Sprintf("%s is not writable by the agent (%v): edit agent.json by hand and restart", filepath.Dir(p), err)
		return false, &r
	}
	return true, nil
}

func (a *Agent) configView() (*model.AgentConfigView, *httpError) {
	o, raw, e := a.readAgentJSON()
	if e != nil {
		return nil, e
	}
	v := &model.AgentConfigView{Path: a.opt.ConfigPath, Masked: maskObject(o), Settings: settingsOf(o)}
	v.Writable, v.WritableReason = configWritable(a.opt.ConfigPath)
	h, _ := sha256Hex(raw)
	v.RestartPending = a.self.cfgHash != "" && h != a.self.cfgHash
	return v, nil
}

func sha256Hex(b []byte) (string, error) {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (a *Agent) handleAgentConfig(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	v, e := a.configView()
	if e != nil {
		return nil, 0, e
	}
	return v, 200, nil
}

func (a *Agent) handlePatchAgentConfig(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	rc := getRC(r)
	var req model.PatchAgentConfigRequest
	if e := readJSON(r, &req); e != nil {
		return nil, 0, e
	}
	if req.Confirm != a.site {
		return nil, 0, invalid("type the site slug "+a.site+" to confirm", []model.ValidationIssue{{Path: "confirm", Code: "V_CONFIRM", Message: "type " + a.site, Severity: "error"}})
	}
	// §21 #110 never blocked by a running update (the person decides)
	if p := a.upgradeRunning(); p != nil {
		return nil, 0, herr(model.ErrConflict, "an agent upgrade is running ("+p.To+", phase "+p.Phase+")", nil)
	}
	o, raw, e := a.readAgentJSON()
	if e != nil {
		return nil, 0, e
	}
	if ok, why := configWritable(a.opt.ConfigPath); !ok {
		return nil, 0, herr(model.ErrForbidden, *why, nil)
	}
	before := settingsOf(o)
	after := before
	ps := req.Settings
	if ps.Tools != nil {
		if ps.Tools.HostShell != nil {
			after.Tools.HostShell = *ps.Tools.HostShell
		}
		if ps.Tools.ContainerExec != nil {
			after.Tools.ContainerExec = *ps.Tools.ContainerExec
		}
		if ps.Tools.DevTools != nil { // §21 #97
			after.Tools.DevTools = *ps.Tools.DevTools
		}
	}
	if ps.ContainerNames != nil {
		after.ContainerNames = *ps.ContainerNames
	}
	if ps.EdgeHTTPPort.Set {
		after.EdgeHTTPPort = ps.EdgeHTTPPort.Value
	}
	if ps.GuiAliasPort.Set {
		after.GuiAliasPort = ps.GuiAliasPort.Value
	}
	agentPort := a.cfg.AgentPort
	if agentPort == 0 {
		agentPort = 7080
	}
	issues := model.ValidateAgentSettings(after, agentPort, &before)
	var errs []model.ValidationIssue
	for _, i := range issues {
		if i.Severity == "error" {
			errs = append(errs, model.ValidationIssue{Path: "settings." + i.Path, Code: i.Code, Message: i.Message, Severity: i.Severity})
		}
	}
	if len(errs) > 0 {
		return nil, 0, invalid(errs[0].Message, errs)
	}
	ipEq := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	changed := []string{}
	if before.Tools.HostShell != after.Tools.HostShell {
		changed = append(changed, "tools.hostShell")
	}
	if before.Tools.ContainerExec != after.Tools.ContainerExec {
		changed = append(changed, "tools.containerExec")
	}
	if before.Tools.DevTools != after.Tools.DevTools {
		changed = append(changed, "tools.devTools")
	}
	if before.ContainerNames != after.ContainerNames {
		changed = append(changed, "containerNames")
	}
	if !ipEq(before.EdgeHTTPPort, after.EdgeHTTPPort) {
		changed = append(changed, "edgeHttpPort")
	}
	if !ipEq(before.GuiAliasPort, after.GuiAliasPort) {
		changed = append(changed, "guiAliasPort")
	}
	if len(changed) == 0 {
		v, e := a.configView()
		if e != nil {
			return nil, 0, e
		}
		return model.PatchAgentConfigResponse{Changed: changed, Restarting: false, Config: *v}, 200, nil
	}
	// write: unknown keys + order kept; the tools object keeps its other keys
	for _, k := range changed {
		switch k {
		case "tools.hostShell", "tools.containerExec", "tools.devTools":
			var tools model.OrderedObject
			if rawT, ok := o.Get("tools"); ok {
				_ = json.Unmarshal(rawT, &tools)
			}
			tools = tools.SetValue("hostShell", after.Tools.HostShell).SetValue("containerExec", after.Tools.ContainerExec).SetValue("devTools", after.Tools.DevTools)
			o = o.SetValue("tools", tools)
		case "containerNames":
			o = o.SetValue("containerNames", after.ContainerNames)
		case "edgeHttpPort":
			o = o.SetValue("edgeHttpPort", after.EdgeHTTPPort)
		case "guiAliasPort":
			o = o.SetValue("guiAliasPort", after.GuiAliasPort)
		}
	}
	compact, _ := json.Marshal(o)
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, compact, "", "  ")
	pretty.WriteByte('\n')
	perm := os.FileMode(0o644)
	if st, err := os.Stat(a.opt.ConfigPath); err == nil {
		perm = st.Mode().Perm()
	}
	if err := os.WriteFile(a.opt.ConfigPath+".bak", raw, perm); err != nil {
		return nil, 0, herr(model.ErrForbidden, "write agent.json.bak: "+err.Error(), nil)
	}
	if err := selfupgrade.WriteFileAtomic(a.opt.ConfigPath, pretty.Bytes(), perm); err != nil {
		return nil, 0, herr(model.ErrForbidden, "write agent.json: "+err.Error(), nil)
	}
	a.activity(rc.actorPtr(), "agent", "agent.config-changed", "agent.json changed: "+fmt.Sprint(changed),
		actorData(rc, map[string]any{"changed": changed, "before": before, "after": after}))
	restart := req.Restart == nil || *req.Restart
	portsChanged := contains(changed, "edgeHttpPort") || contains(changed, "guiAliasPort")
	if portsChanged && sdsock.Activated() && a.self.sup.Kind() == "systemd" {
		// §21 #91 f: opm-agent.socket holds the edge / alias ports → rewrite it; the socket + the service restart together
		r, err := selfupgrade.InstallUnits(selfupgrade.UnitOptions{ConfigPath: a.opt.ConfigPath, Binary: a.self.binary, DataDir: a.dataDir, Socket: true,
			ScriptPath: a.opt.GuardScript})
		if err != nil {
			a.log.Error("gui", "socket unit rewrite failed", "error", err.Error())
		} else {
			a.log.Info("gui", "socket unit rewritten", "ports", r.SocketPorts)
			restart = true
			a.self.restartCmd = []string{"systemctl", "restart", "--no-block", selfupgrade.SocketFile, selfupgrade.ServiceFile}
		}
	}
	if restart {
		a.restartAgent(rc.actorPtr(), actorData(rc, nil), "agent.json settings changed")
	}
	v, e := a.configView()
	if e != nil {
		return nil, 0, e
	}
	return model.PatchAgentConfigResponse{Changed: changed, Restarting: restart, Config: *v}, 200, nil
}
