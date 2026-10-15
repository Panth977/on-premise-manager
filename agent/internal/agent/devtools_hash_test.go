package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// §21 #97 a: a revision WITH dev-tool presets hashes like the TS canonical hash (expected values computed with
// shared/dist/canonical.js + sha256 in node), a revision without presets keeps its hash (no devTools key), and a
// preset key this agent does not know survives the round trip (a newer cloud never makes this agent refuse).
func TestDevToolsRevisionHash(t *testing.T) {
	const tsBefore = "sha256:bc705adac999181fcbcd9899174a150370e004d8b0e78573104e041778baf119"
	const tsAfter = "sha256:b326f59f914068ba79f9f78a320d284a8d818d41485a3fd307bd1f0c679e08da"
	b, err := os.ReadFile("../../../shared/testdata/hello-config.json")
	if err != nil {
		t.Skip("shared testdata missing: ", err)
	}
	var doc struct {
		Config json.RawMessage `json:"config"`
	}
	_ = json.Unmarshal(b, &doc)
	check := func(name string, raw []byte, want string) model.Config {
		t.Helper()
		var cfg model.Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h, err := canonical.HashCanonical(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if h != want {
			t.Fatalf("%s: Go hash %s, TS hash %s", name, h, want)
		}
		rev := model.Revision{RevID: "01JA2Y0000000000000000000A", Config: cfg, Hash: model.Sha256(want)}
		normalizeCatalog(&rev.Catalog)
		ch, _ := canonical.HashCanonical(rev.Catalog)
		rev.CatalogHash = model.Sha256(ch)
		if err := verifyRevision(&rev); err != nil {
			t.Fatalf("%s: verifyRevision: %v", name, err)
		}
		// stored (DB) round trip keeps the hash
		stored, _ := json.Marshal(cfg)
		var again model.Config
		_ = json.Unmarshal(stored, &again)
		if h2, _ := canonical.HashCanonical(again); h2 != want {
			t.Fatalf("%s: hash changed after a store round trip", name)
		}
		return cfg
	}
	plain := check("without presets", doc.Config, tsBefore)
	for _, s := range plain.Sites {
		if s.Config.DevTools != nil || len(s.Config.DevToolList()) != 0 {
			t.Fatal("absent devTools must stay nil")
		}
	}
	if out, _ := json.Marshal(plain); strings.Contains(string(out), "devTools") {
		t.Fatal("devTools key written for a config without presets")
	}
	presets, err := os.ReadFile("testdata/devtools-presets.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(doc.Config, &m)
	var pv any
	_ = json.Unmarshal(presets, &pv)
	m["sites"].([]any)[1].(map[string]any)["config"].(map[string]any)["devTools"] = pv
	withRaw, _ := json.Marshal(m)
	cfg := check("with presets", withRaw, tsAfter)
	list := cfg.Sites[1].Config.DevToolList()
	if len(list) != 2 || list[0].Kind != "redis" || list[0].Auth.Password.Kind != "expr" || *list[0].Auth.Password.Expr != "{{ secret.api-token }}" ||
		list[1].Target.Kind != "url" || len(list[1].Options.DefaultHeaders) != 1 || !list[0].ReadOnly {
		t.Fatalf("decoded presets: %+v", list)
	}
	// an empty list is never written (the normalizer drops it)
	empty := []model.DevToolPreset{}
	sc := cfg.Sites[0].Config
	sc.DevTools = &empty
	sc.NormalizeDevTools()
	if sc.DevTools != nil {
		t.Fatal("empty devTools must be dropped")
	}
}
