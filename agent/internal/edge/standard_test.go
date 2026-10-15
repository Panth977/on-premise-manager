package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/requestbody" // in the official image, not in the agent

	"opm/agent/internal/edgecfg"
	"opm/agent/internal/model"
)

// vectorEdges loads the edges + container ports of shared/testdata/edge-vectors.json.
func vectorEdges(t *testing.T) (map[string]model.Edge, map[string]map[string]map[string]int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "testdata", "edge-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Edges map[string]model.Edge                `json:"edges"`
		Ports map[string]map[string]map[string]int `json:"ports"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for k, e := range v.Edges {
		v.Edges[k] = e.Normalized()
	}
	return v.Edges, v.Ports
}

// StandardInput is the edge Input of a vector edge for BuildStandard (upstream ports = container ports).
func standardInput(e model.Edge, ports map[string]map[string]int) *edgecfg.Input {
	return &edgecfg.Input{SiteSlug: "s", HTTPPort: e.HTTPPort, HTTPSPort: e.HTTPSPort, MaxBodyBytes: e.MaxBodyBytes, Hosts: e.Hosts, Routes: e.Routes,
		Upstreams: ports, FilesPEM: map[string]edgecfg.PEMPair{}, DNSConfigs: map[string]map[string]any{}}
}

// modulesOf lists every "handler" / "module" / "protocol" name of a Caddy JSON document.
func modulesOf(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if s, ok := vv.(string); ok && (k == "handler" || k == "module" || k == "protocol") {
				out[s] = true
			}
			modulesOf(vv, out)
		}
	case []any:
		for _, vv := range x {
			modulesOf(vv, out)
		}
	}
}

func TestBuildStandardValidates(t *testing.T) {
	edges, ports := vectorEdges(t)
	names := make([]string, 0, len(edges))
	for k := range edges {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		tlsDir := t.TempDir()
		certPEM, keyPEM, _ := selfSigned(t, clientHost)
		_ = os.WriteFile(filepath.Join(tlsDir, clientHost+".crt"), []byte(certPEM), 0o644)
		_ = os.WriteFile(filepath.Join(tlsDir, clientHost+".key"), []byte(keyPEM), 0o600)
		cfg, notes := edgecfg.BuildStandard(standardInput(edges[name], ports[name]), edgecfg.StandardOptions{StorageRoot: t.TempDir(), TLSDir: tlsDir})
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "opm_agent") || strings.Contains(string(raw), "/_opm") {
			t.Fatalf("%s: the standard config must not route /_opm", name)
		}
		var generic any
		_ = json.Unmarshal(raw, &generic)
		mods := map[string]bool{}
		modulesOf(generic, mods)
		for m := range mods {
			if strings.HasPrefix(m, "opm_") {
				t.Fatalf("%s: non-standard module %s", name, m)
			}
		}
		var c caddy.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		if err := caddy.Validate(&c); err != nil {
			t.Fatalf("%s: caddy.Validate: %v", name, err)
		}
		t.Logf("%s: %d bytes, modules %v, notes %v", name, len(raw), keys(mods), notes)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
