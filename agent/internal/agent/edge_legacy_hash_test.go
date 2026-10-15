package agent

import (
	"encoding/json"
	"os"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// Round E: a revision published before round E (legacy edge: match.pathPrefix, proxy timeoutSec, no responseHeaders /
// maxBodyBytes) still verifies, and the agent runs it in the round E shape.
func TestLegacyEdgeRevisionHash(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/hello-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	cfg := doc["config"].(map[string]any)
	legacyEdge := `{"httpPort":8081,"httpsPort":null,"hosts":[{"hostname":"*","tls":{"mode":"none"},"redirectHttpToHttps":false}],
	  "routes":[{"routeId":"api","hosts":[],"match":{"pathPrefix":"/api","methods":null},"action":{"type":"proxy","containerSlug":"hello-api","portName":"http","stripPrefix":true,"addPrefix":null,"setHeaders":{"X-Opm-Site":"{{ self.slug }}"},"removeHeaders":[],"websocket":false,"timeoutSec":30},"order":1},
	            {"routeId":"old","hosts":[],"match":{"pathPrefix":"/old","methods":null},"action":{"type":"redirect","to":"/","status":308,"keepPath":false},"order":2}]}`
	var le any
	_ = json.Unmarshal([]byte(legacyEdge), &le)
	for _, s := range cfg["sites"].([]any) {
		s.(map[string]any)["config"].(map[string]any)["edge"] = le
	}
	hash, err := canonical.HashCanonical(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	var rev model.Revision
	rev.RevID = "01JA2Y0000000000000000000A"
	if err := json.Unmarshal(raw, &rev.Config); err != nil {
		t.Fatal(err)
	}
	rev.Hash = model.Sha256(hash)
	rev.CatalogHash, _ = func() (model.Sha256, error) {
		normalizeCatalog(&rev.Catalog)
		h, e := canonical.HashCanonical(rev.Catalog)
		return model.Sha256(h), e
	}()
	if err := verifyRevision(&rev); err != nil {
		t.Fatalf("legacy revision does not verify: %v", err)
	}
	r := rev.Config.Sites[0].Config.Edge.Routes[0]
	if r.Match.Kind != "prefix" || r.Match.Path != "/api" || r.Action.ReadTimeoutSec != 30 || r.Action.ConnectTimeoutSec != 60 {
		t.Fatalf("not normalized: %+v", r)
	}
	// a stored (DB) round trip keeps the hash too
	stored, _ := json.Marshal(rev.Config)
	var again model.Config
	_ = json.Unmarshal(stored, &again)
	if h2, _ := canonical.HashCanonical(again); h2 != hash {
		t.Fatal("hash changed after a store round trip")
	}
}
