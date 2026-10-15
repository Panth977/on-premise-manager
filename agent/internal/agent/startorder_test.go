package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

// startPlan (§21 #93): the user's site (15 containers, depends: [] everywhere) — mqtt starts before
// northwind-backend-admin when the backend mentions it; waits = explicit + implicit only.
func TestStartPlanUserSite(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/depends-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Infer []struct {
			Site  model.SiteConfig `json:"site"`
			Order []string         `json:"order"`
		} `json:"infer"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	a := newTestAgent(t)
	site := &model.Site{SiteSlug: "app", Config: v.Infer[0].Site}
	cfg := &model.Config{Sites: []model.Site{*site}}
	order, waits := a.startPlan(cfg, &model.RevisionCatalog{}, site, nil)
	if !reflect.DeepEqual(order, v.Infer[0].Order) {
		t.Fatalf("order %v", order)
	}
	if w := waits["northwind-backend-worker"]; !reflect.DeepEqual(w, []string{"redis"}) {
		t.Fatalf("worker waits for its implicit depend: %v", w)
	}
	if w := waits["northwind-backend-admin"]; len(w) != 0 {
		t.Fatalf("a suggested depend only orders the start: %v", w)
	}
	if o, _ := a.startPlan(cfg, &model.RevisionCatalog{}, nil, nil); o != nil {
		t.Fatal("no site → no order")
	}
}
