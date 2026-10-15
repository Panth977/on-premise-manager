package model

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Same cases as shared/src/agent-self.test.ts (SPEC §21 #90).
func TestCompareAgentVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.9.0-10080438", "0.10.0-10081200", -1},
		{"0.10.0", "0.9.9", 1},
		{"0.10.0-10081200", "0.10.0-10081201", -1},
		{"0.10.0-10081200", "0.10.0-10081200", 0},
		{"0.10.0", "0.10.0-10081200", -1},
		{"1.0.0-dev", "1.0.0-10081200", 1},
		{"garbage", "0.1.0", -1},
		{"v0.7.2-10080232", "0.7.2-10080232", 0},
		{"garbage", "other", 0},
	}
	for _, c := range cases {
		if got := CompareAgentVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSupportsGuiUpgrade(t *testing.T) {
	if GuiUpgradeMinVersion != "0.10.0" || AgentUpgradeHealthSec != 60 || AgentUpgradeRequestTTLSec != 86400 {
		t.Fatal("constants")
	}
	for v, want := range map[string]bool{"0.7.2-10080232": false, "0.9.0-10080438": false, "0.10.0-10081200": true, "1.2.3": true, "": false} {
		if SupportsGuiUpgrade(v) != want {
			t.Errorf("supportsGuiUpgrade(%q) != %v", v, want)
		}
	}
	if !AgentUpgradeAvailable("0.10.0-10081200", "0.10.1-10090000") || AgentUpgradeAvailable("0.10.1-10090000", "0.10.1-10090000") || AgentUpgradeAvailable("0.10.1", "") {
		t.Fatal("agentUpgradeAvailable")
	}
}

func TestValidateAgentSettings(t *testing.T) {
	ip := func(v int) *int { return &v }
	ok := AgentEditableSettings{Tools: AgentToolsSettings{HostShell: true, ContainerExec: true}, ContainerNames: "plain", EdgeHTTPPort: ip(80), GuiAliasPort: ip(8080)}
	if got := ValidateAgentSettings(ok, 7080, nil); len(got) != 0 {
		t.Fatalf("ok: %+v", got)
	}
	codes := func(s AgentEditableSettings) []string {
		out := []string{}
		for _, i := range ValidateAgentSettings(s, 7080, &ok) {
			out = append(out, i.Path+":"+i.Code+":"+i.Severity)
		}
		return out
	}
	with := func(f func(s *AgentEditableSettings)) AgentEditableSettings { s := ok; f(&s); return s }
	cases := []struct {
		s    AgentEditableSettings
		want []string
	}{
		{with(func(s *AgentEditableSettings) { s.EdgeHTTPPort = ip(7080) }), []string{"edgeHttpPort:V_PORT_CLASH:error"}},
		{with(func(s *AgentEditableSettings) { s.GuiAliasPort = ip(80) }), []string{"guiAliasPort:V_PORT_CLASH:error"}},
		{with(func(s *AgentEditableSettings) { s.GuiAliasPort = ip(70000) }), []string{"guiAliasPort:V_PORT:error"}},
		{with(func(s *AgentEditableSettings) { s.GuiAliasPort = nil }), []string{}},
		{with(func(s *AgentEditableSettings) { s.EdgeHTTPPort = nil }), []string{"edgeHttpPort:V_PORT:warning"}},
		{with(func(s *AgentEditableSettings) { s.ContainerNames = "prefixed" }), []string{"containerNames:V_CONTAINER_NAMES:info"}},
		{with(func(s *AgentEditableSettings) { s.ContainerNames = "x" }), []string{"containerNames:V_ENUM:error"}},
	}
	for i, c := range cases {
		if got := codes(c.s); !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: %v want %v", i, got, c.want)
		}
	}
}

func TestUninstallCommands(t *testing.T) {
	l := UninstallCommands("/etc/opm/agent.json", "/var/lib/opm", "/usr/local/bin/opm-agent", true)
	if l[0] != "sudo systemctl disable --now opm-agent.socket opm-agent 2>/dev/null; sudo systemctl disable --now opm-agent" || !strings.Contains(l[1], "opm-agent.socket") || !strings.Contains(l[len(l)-1], "installed-deps.json") || !strings.Contains(strings.Join(l, "\n"), "/usr/local/bin/opm-agent.prev") || !strings.Contains(l[len(l)-1], "/var/lib/opm") {
		t.Fatal(l)
	}
	if l := UninstallCommands("/c", "/d", "/b", false); l[0] != `sudo pkill -f "opm-agent run"` {
		t.Fatal(l)
	}
}

// TestAgentSelfContractMirror: constants + SUPPORT_BUNDLE_SECTIONS equal shared/src/agent-self.ts / types.ts.
func TestAgentSelfContractMirror(t *testing.T) {
	b, err := os.ReadFile("../../../shared/src/agent-self.ts")
	if err != nil {
		t.Skip("shared/src/agent-self.ts not found: ", err)
	}
	src := string(b)
	i := strings.Index(src, "export const SUPPORT_BUNDLE_SECTIONS = [")
	if i < 0 {
		t.Fatal("SUPPORT_BUNDLE_SECTIONS not found")
	}
	block := src[i:]
	block = block[:strings.Index(block, "] as const")]
	var ts []string
	for _, m := range regexp.MustCompile(`(?m)^\s*'([^']+)'`).FindAllStringSubmatch(block, -1) {
		ts = append(ts, m[1])
	}
	if !reflect.DeepEqual(ts, SupportBundleSections) {
		t.Fatalf("SUPPORT_BUNDLE_SECTIONS\nTS: %v\nGo: %v", ts, SupportBundleSections)
	}
	tb, _ := os.ReadFile("../../../shared/src/types.ts")
	for _, want := range []string{"export const GUI_UPGRADE_MIN_VERSION = '0.10.0'", "export const AGENT_UPGRADE_HEALTH_SEC = 60", "export const AGENT_UPGRADE_REQUEST_TTL_SEC = 24 * 3600"} {
		if !strings.Contains(string(tb), want) {
			t.Errorf("types.ts lacks %q", want)
		}
	}
	// every AgentUpgradeProgress key of the TS interface is a JSON key of the Go struct
	j, _ := json.Marshal(AgentUpgradeProgress{})
	for _, k := range []string{"upgradeId", "requestId", "runId", "via", "siteSlug", "from", "to", "phase", "progress", "bytesDone", "bytesTotal", "verification", "error", "actor", "startedAt", "updatedAt", "finishedAt"} {
		if !strings.Contains(string(j), `"`+k+`"`) {
			t.Errorf("AgentUpgradeProgress lacks %s", k)
		}
	}
}

func TestOrderedObject(t *testing.T) {
	var o OrderedObject
	if err := json.Unmarshal([]byte(`{"b":1,"a":{"x":true},"c":null}`), &o); err != nil {
		t.Fatal(err)
	}
	o = o.SetValue("a", map[string]int{"y": 2}).SetValue("d", "new")
	b, _ := json.Marshal(o)
	if string(b) != `{"b":1,"a":{"y":2},"c":null,"d":"new"}` {
		t.Fatal(string(b))
	}
}

// Same cases as the §21 #91 block of shared (agentInstallAction / agentSchemaCompatible).
func TestInstallActionAndSchema(t *testing.T) {
	if AgentInstallAction("", "0.10.0") != "fresh" || AgentInstallAction("0.9.0", "0.10.0") != "upgrade" || AgentInstallAction("0.10.1", "0.10.0") != "downgrade" || AgentInstallAction("0.10.0-1", "0.10.0-1") != "same" {
		t.Fatal("install action")
	}
	ip := func(v int) *int { return &v }
	if !AgentSchemaCompatible(nil, nil, nil) || !AgentSchemaCompatible(ip(1), ip(1), ip(0)) || AgentSchemaCompatible(ip(2), ip(1), ip(0)) || AgentSchemaCompatible(ip(0), ip(2), ip(1)) || !AgentSchemaCompatible(nil, ip(2), nil) {
		t.Fatal("schema compatible")
	}
	// the shared agent-self.test.ts vectors
	if AgentInstallAction("0.7.2-10080232", "0.10.0-10081200") != "upgrade" || AgentInstallAction("0.10.1-10090000", "0.10.0-10081200") != "downgrade" ||
		!AgentSchemaCompatible(ip(1), nil, nil) || AgentSchemaCompatible(ip(2), nil, nil) || !AgentSchemaCompatible(ip(2), ip(3), ip(1)) ||
		!AgentSchemaCompatible(nil, ip(2), ip(0)) || AgentSchemaCompatible(ip(1), ip(3), ip(2)) {
		t.Fatal("shared vectors")
	}
	if AgentLegacySchema != 1 {
		t.Fatal("legacy")
	}
}
