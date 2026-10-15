package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

// §21 #103: config views mask static preset values; a local edit that sends them back masked keeps the head's preset
// (raw bytes: the revision hash and the V_LOCAL_DEVTOOLS comparison stay exact).
func TestPresetStaticValueMaskRoundTrip(t *testing.T) {
	raw := `{"presetSlug":"broker","name":"Broker","description":"","kind":"mqtt","target":{"kind":"host","host":"mqtt","port":1883},` +
		`"auth":{"username":"northwind","password":{"kind":"literal","value":"hunter2-static"},"database":null,"clientId":null,"httpScheme":"none","token":{"kind":"none"},"apiKeyHeader":null},` +
		`"options":{"tls":false,"tlsInsecure":false,"tlsServerName":null,"sslMode":null,"timeoutSec":null,"maxRows":null,"consistency":null,"scheme":null,"basePath":null,"followRedirects":null,"defaultHeaders":[],"cleanSession":null,"keepAliveSec":null},"readOnly":false}`
	var p model.DevToolPreset
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	list := []model.DevToolPreset{p}
	head := &model.Config{Sites: []model.Site{{SiteSlug: "app", Config: model.SiteConfig{DevTools: &list}}}}
	v := maskConfig(head)
	js, _ := json.Marshal(v)
	if strings.Contains(string(js), "hunter2-static") || !strings.Contains(string(js), model.Mask) {
		t.Fatalf("config view leaks the static value: %s", js)
	}
	if b, _ := json.Marshal(head.Sites[0].Config.DevToolList()[0]); string(b) != raw {
		t.Fatal("maskConfig changed the revision")
	}
	// the GUI sends the view back (masked) → the head's preset, byte for byte
	var back []model.DevToolPreset
	b, _ := json.Marshal(v.Sites[0].Config.DevToolList())
	_ = json.Unmarshal(b, &back)
	got := unmaskPresetLiterals(back, head.Sites[0].Config.DevToolList())
	if g, _ := json.Marshal((*got)[0]); string(g) != raw {
		t.Fatalf("unmask: %s", g)
	}
	// a changed preset is kept as sent (checkLocalEdit refuses it)
	back[0] = back[0].Detached()
	back[0].Name = "Other"
	got = unmaskPresetLiterals(back, head.Sites[0].Config.DevToolList())
	if (*got)[0].Name != "Other" {
		t.Fatal("a changed preset must not be replaced")
	}
}
