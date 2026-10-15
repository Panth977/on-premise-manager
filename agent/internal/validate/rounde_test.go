package validate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

// Mirrors the round E part of the TS "edge rules" test.
func TestRoundEEdgeRules(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	if is := ValidateConfig(cfg, ctx); !is.Ok() {
		t.Fatalf("hello config: %+v", is.List)
	}
	f := clone(t, cfg)
	fr := &site(f, "app").Config.Edge.Routes
	dup := (*fr)[0]
	dup.RouteID = "dup"
	*fr = append(*fr, dup)
	empty := model.EmptyResponseHeaders()
	*fr = append(*fr, model.EdgeRoute{RouteID: "re", Hosts: []string{}, Match: model.EdgeMatch{Kind: "regex", Path: "(?=x)", StopRegex: true},
		Action: model.EdgeRouteAction{Type: "rewrite", Regex: "^/(", Replacement: ""}, Order: 9, ResponseHeaders: &empty})
	pa := &(*fr)[0].Action
	pa.ReadTimeoutSec = 0
	m1 := int64(-1)
	pa.MaxBodyBytes = &m1
	pa.Rewrite = &model.EdgeRewrite{Regex: "a", Replacement: "/b"}
	pa.SetHeaders = model.MapOf("Connection", "Upgrade")
	site(f, "app").Config.Edge.Hosts[0].ResponseHeaders = model.EdgeResponseHeaders{Set: model.MapOf("X-A", "1"), Add: model.MapOf("x-a", "2"), Remove: []string{"bad name"}, Always: []string{"X-B"}}
	is := ValidateConfig(f, ctx)
	codes := []string{}
	for _, x := range is.List {
		codes = append(codes, x.Code)
	}
	for _, k := range []string{"V_DUPLICATE", "V_EDGE_REGEX", "V_EDGE_ROUTE", "V_RANGE", "V_EDGE_BODY", "V_EDGE_HEADER", "V_EDGE_HOP", "V_REQUIRED", "V_FORMAT"} {
		if !contains(codes, k) {
			t.Errorf("want %s in %v", k, codes)
		}
	}
	found := false
	for _, x := range is.List {
		if x.Code == "V_EDGE_HEADER" && strings.HasSuffix(x.Path, "routes[re].responseHeaders") {
			found = true
		}
	}
	if !found {
		t.Error("rewrite routes have no response headers")
	}
	// redirect 303 is valid; keepPath only for prefix routes
	g := clone(t, cfg)
	gr := &site(g, "app").Config.Edge.Routes
	*gr = append(*gr, model.EdgeRoute{RouteID: "r303", Hosts: []string{}, Match: model.EdgeMatch{Kind: "exact", Path: "/x"}, Action: model.EdgeRouteAction{Type: "redirect", To: "/y", Status: 303, KeepPath: true}, Order: 5})
	is = ValidateConfig(g, ctx)
	if hasIssue(is, "V_ENUM", "error") || !hasIssue(is, "V_EDGE_ROUTE", "error") {
		t.Errorf("303 / keepPath: %+v", is.List)
	}
}

func TestResponseHeaderAsRequest(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	r := &site(cfg, "app").Config.Edge.Routes[0]
	r.Action.SetHeaders = model.MapOf("Cache-Control", "public, max-age=3600, must-revalidate", "X-Script-Name", "/x")
	host := &site(cfg, "app").Config.Edge.Hosts[0]
	host.ResponseHeaders = model.EdgeResponseHeaders{Set: model.HeaderMap{}, Add: model.MapOf("Pragma", "no-cache", "Cache-Control", "no-store"), Remove: []string{}, Always: []string{}}
	n := 0
	for _, x := range ValidateConfig(cfg, ctx).List {
		if x.Code == "W_RESPONSE_HEADER_AS_REQUEST" {
			n++
			if x.Severity != "warning" {
				t.Fatal(x)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want 1 warning, got %d", n)
	}
	if !IsResponseOnlyHeader("Access-Control-Allow-Origin", "") || IsResponseOnlyHeader("Cache-Control", "no-transform") || IsResponseOnlyHeader("X-Script-Name", "/x") {
		t.Fatal("isResponseOnlyHeader")
	}
	m := MoveToResponseHeaders(*r, "cache-control", host)
	got, _ := json.Marshal(map[string]any{"set": m.Action.SetHeaders, "rh": m.ResponseHeaders})
	var a, b any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal([]byte(`{"set":{"X-Script-Name":"/x"},"rh":{"set":{"Cache-Control":"public, max-age=3600, must-revalidate"},"add":{"Pragma":"no-cache"},"remove":[],"always":[]}}`), &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("move: %s", got)
	}
}
