package driver

import (
	"reflect"
	"strings"
	"testing"

	"opm/agent/internal/docker"
)

// TestContainerNames (§21 #88): plain (default) = the containerSlug; prefixed = opm_<dep>_<site>_<slug>.
func TestContainerNames(t *testing.T) {
	if got := ContainerName("", "Dep", "app", "mqtt"); got != "mqtt" {
		t.Fatalf("default %q", got)
	}
	if got := ContainerName(NamesPlain, "Dep", "app", "mqtt"); got != "mqtt" {
		t.Fatalf("plain %q", got)
	}
	if got := ContainerName(NamesPrefixed, "Dep", "app", "mqtt"); got != "opm_Dep_app_mqtt" {
		t.Fatalf("prefixed %q", got)
	}
	if !ValidNames("") || !ValidNames("plain") || !ValidNames("prefixed") || ValidNames("short") {
		t.Fatal("ValidNames")
	}
	if NetworkName("Dep", "app") != "opm_Dep_app" {
		t.Fatal("network name")
	}
	d := &Driver{Dep: "Dep", Site: "app"}
	if d.NameFor("web") != "web" {
		t.Fatal("driver default must be plain")
	}
	d.Names = NamesPrefixed
	if d.NameFor("web") != "opm_Dep_app_web" {
		t.Fatal("driver prefixed")
	}
}

// TestNameNotHashed: the naming mode changes the docker name, never the spec hash (a mode switch / the rename
// migration recreates nothing); both modes get the aliases [slug, prefixed name].
func TestNameNotHashed(t *testing.T) {
	var bs []*Built
	for _, mode := range []string{"", NamesPlain, NamesPrefixed} {
		cfg, cat := fixture("green")
		b, err := Build(BuildParams{DataDir: "/d", DeploymentID: "Dep", SiteSlug: "app", RevID: "R", Config: cfg, Catalog: cat, Container: &cfg.Sites[0].Config.Containers[0], Names: mode})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, b)
	}
	slug := bs[0].ContainerSlug
	if bs[0].Name != slug || bs[1].Name != slug || bs[2].Name != "opm_Dep_app_"+slug {
		t.Fatalf("names %q %q %q", bs[0].Name, bs[1].Name, bs[2].Name)
	}
	if bs[0].Hash != bs[1].Hash || bs[1].Hash != bs[2].Hash {
		t.Fatalf("hash depends on the name: %s %s %s", bs[0].Hash, bs[1].Hash, bs[2].Hash)
	}
	want := []string{slug, "opm_Dep_app_" + slug}
	for _, b := range bs {
		got := b.CreateConfig("R", 1).NetworkingConfig.EndpointsConfig["opm_Dep_app"].Aliases
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("aliases %v want %v", got, want)
		}
	}
}

// TestFindNamed: exact names only (never an id prefix, never a substring).
func TestFindNamed(t *testing.T) {
	list := []docker.ContainerSummary{{ID: "cafe0001", Names: []string{"/opm_Dep_app_mqtt"}}, {ID: "beef0002", Names: []string{"/mqtt2"}}}
	if docker.FindNamed(list, "mqtt") != nil {
		t.Fatal("substring matched")
	}
	if docker.FindNamed(list, "cafe") != nil {
		t.Fatal("id prefix matched")
	}
	if c := docker.FindNamed(list, "mqtt2"); c == nil || c.ID != "beef0002" {
		t.Fatal("exact name not found")
	}
}

// TestNameClashError: the message the staging view shows.
func TestNameClashError(t *testing.T) {
	err := NameClashError("mqtt", &docker.ContainerSummary{ID: "0123456789abcdef", Image: "busybox", Labels: map[string]string{}})
	want := "Docker container name `mqtt` is already used by a container not managed by this site (id 0123456789ab, image busybox) — remove or rename it, or set containerNames: prefixed in agent.json"
	if err.Error() != want {
		t.Fatalf("%s", err)
	}
	err = NameClashError("mqtt", &docker.ContainerSummary{ID: "0123456789abcdef", Image: "eclipse-mosquitto:2", Labels: map[string]string{LDeployment: "Other", LSite: "s2"}})
	if want := "managed by OPM deployment Other site s2"; !strings.Contains(err.Error(), want) {
		t.Fatalf("%s", err)
	}
}
