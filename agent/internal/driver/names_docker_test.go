package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// TestDockerNamesIntegration (real Docker, e.g. Colima; §21 #88):
//  1. plain names: the docker name = the containerSlug; from another container of the site network the slug AND the
//     legacy prefixed name resolve (network aliases) to the container;
//  2. rename-in-place migration from prefixed: same container id, still running, same StartedAt, Plan keeps it;
//  3. clash with an unmanaged `docker run --name <slug> busybox` container: staging (CheckNames) and CreateStart fail with
//     the clear error and the unmanaged container is untouched (also not renamed by MigrateNames);
//  4. a managed container of ANOTHER site holding the name: the same error.
func TestDockerNamesIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if out, err := exec.Command("docker", "image", "inspect", "busybox:1.36").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("docker", "pull", "busybox:1.36").CombinedOutput(); err2 != nil {
			t.Skipf("no docker / busybox: %s %s", out, out2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dc := docker.New(docker.DefaultHost(""))
	if _, err := dc.Version(ctx); err != nil {
		t.Skip("no docker: ", err)
	}
	base, err := os.MkdirTemp("", "opm-names-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	img, blob := busyboxBlobs(t, base)
	if err := LoadImage(ctx, dc, img, blob); err != nil {
		t.Fatalf("load: %v", err)
	}
	sfx := fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff)
	sfx = strings.Map(func(r rune) rune { return 'a' + (r % 26) }, sfx) // slug-safe letters
	dep, site := "DepNames"+sfx, "site-01"
	log := dlog.New(filepath.Join(base, "logs"), "debug", nil)
	drv := func(s, mode string) *Driver {
		d := New(dc, dep, s, filepath.Join(base, "agent-"+s), log, time.Minute)
		d.Names = mode
		d.NoneReadyAfter = time.Second
		return d
	}
	build := func(s, mode, slug string) *Built {
		cfg, cat := fixture("green")
		cfg.Sites[0].SiteSlug = s
		ctr := &cfg.Sites[0].Config.Containers[0]
		ctr.ContainerSlug = slug
		ctr.ServiceSlug, ctr.Version = "bb", "1.36"
		ctr.Env = nil
		ctr.Volumes = nil
		ctr.Ports = nil
		ctr.Restart = "always"
		ctr.Command = []string{"sh", "-c", "sleep 3600"}
		spec := cat.Images["web:v1"]
		delete(cat.Images, "web:v1")
		spec.ServiceSlug, spec.Version, spec.ConfigDigest, spec.Layers = "bb", "1.36", img.ConfigDigest, img.Layers
		spec.Ports, spec.Env, spec.Volumes = nil, nil, nil
		cat.Images["bb:1.36"] = spec
		cat.Services = map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}
		b, err := Build(BuildParams{DataDir: filepath.Join(base, "agent-"+s), DeploymentID: dep, SiteSlug: s, RevID: "R1", RevSeq: 1, Config: cfg, Catalog: cat, Container: ctr, Names: mode})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			_ = dc.ContainerRemove(context.Background(), id, true)
		}
		_ = dc.NetworkRemove(context.Background(), NetworkName(dep, site))
		_ = dc.NetworkRemove(context.Background(), NetworkName(dep, "site-02"))
	})
	start := func(d *Driver, b *Built) string {
		id, err := d.CreateStart(ctx, b, "R1", 1)
		if id != "" {
			ids = append(ids, id)
		}
		if err != nil {
			t.Fatalf("create %s: %v", b.Slug, err)
		}
		return id
	}
	nameOf := func(id string) string {
		cj, err := dc.ContainerInspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimPrefix(cj.Name, "/")
	}
	ipRe := regexp.MustCompile(`Address(?: \d+)?:\s+(\d+\.\d+\.\d+\.\d+)`)
	resolve := func(fromID, name string) string {
		code, out, err := dc.Exec(ctx, fromID, []string{"nslookup", name, "127.0.0.11"})
		if err != nil {
			t.Fatal(err)
		}
		var last string
		for _, m := range ipRe.FindAllStringSubmatch(out, -1) {
			if m[1] != "127.0.0.11" {
				last = m[1]
			}
		}
		if code != 0 {
			return ""
		}
		return last
	}
	ipOf := func(id string) string {
		raw, err := dc.InspectRaw(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`"IPAddress":"(\d+\.\d+\.\d+\.\d+)"`).FindStringSubmatch(string(raw))
		if m == nil {
			t.Fatalf("no ip: %s", raw)
		}
		return m[1]
	}

	// 1. plain names + DNS by the plain name (and the legacy prefixed alias) from another container
	plain := drv(site, NamesPlain)
	mqttSlug, backSlug := "mqtt-"+sfx, "backend-"+sfx
	mqtt := start(plain, build(site, NamesPlain, mqttSlug))
	back := start(plain, build(site, NamesPlain, backSlug))
	if n := nameOf(mqtt); n != mqttSlug {
		t.Fatalf("plain name: %q", n)
	}
	want := ipOf(mqtt)
	if got := resolve(back, mqttSlug); got != want {
		t.Fatalf("%s from the backend resolves to %q, want %s", mqttSlug, got, want)
	}
	if got := resolve(back, PrefixedName(dep, site, mqttSlug)); got != want {
		t.Fatalf("legacy prefixed alias resolves to %q, want %s", got, want)
	}
	t.Logf("ok  plain names; %s and %s resolve to %s from %s", mqttSlug, PrefixedName(dep, site, mqttSlug), want, backSlug)

	// 2. migration from prefixed: rename in place, no recreate
	pre := drv(site, NamesPrefixed)
	webSlug := "web-" + sfx
	webB := build(site, NamesPrefixed, webSlug)
	web := start(pre, webB)
	if n := nameOf(web); n != PrefixedName(dep, site, webSlug) {
		t.Fatalf("prefixed name: %q", n)
	}
	// the user's question: with the OLD prefixed docker names the slug already resolved (network alias = containerSlug)
	if got, w := resolve(back, webSlug), ipOf(web); got != w {
		t.Fatalf("alias %s of a prefixed-name container resolves to %q, want %s", webSlug, got, w)
	}
	t.Logf("ok  prefixed docker name %s: the alias %s resolves from %s", PrefixedName(dep, site, webSlug), webSlug, backSlug)
	cj0, _ := dc.ContainerInspect(ctx, web)
	done, skipped, err := plain.MigrateNames(ctx)
	if err != nil || len(skipped) != 0 || len(done) != 1 || done[0].ID != web || done[0].To != webSlug {
		t.Fatalf("migrate: %+v %v %v", done, skipped, err)
	}
	cj1, err := dc.ContainerInspect(ctx, web)
	if err != nil || cj1.ID != cj0.ID || strings.TrimPrefix(cj1.Name, "/") != webSlug || !cj1.State.Running || cj1.State.StartedAt != cj0.State.StartedAt {
		t.Fatalf("rename must keep the container: %v %+v", err, cj1)
	}
	if got, w := resolve(back, webSlug), ipOf(web); got != w {
		t.Fatalf("after the rename %s resolves to %q, want %s", webSlug, got, w)
	}
	ex, _ := plain.Existing(ctx)
	acts := Plan([]*Built{build(site, NamesPlain, mqttSlug), build(site, NamesPlain, backSlug), build(site, NamesPlain, webSlug)}, ex)
	for _, a := range acts {
		if a.Kind != ActKeep {
			t.Fatalf("after the rename every container is kept, got %s %s", a.Kind, a.ContainerSlug)
		}
	}
	if done, _, _ := plain.MigrateNames(ctx); len(done) != 0 {
		t.Fatalf("second migration must be a no-op: %+v", done)
	}
	t.Logf("ok  rename in place %s → %s (id %s unchanged, StartedAt unchanged, plan keep)", PrefixedName(dep, site, webSlug), webSlug, web[:12])

	// 3. clash with an unmanaged container
	clashSlug := "clash-" + sfx
	out, err := exec.Command("docker", "run", "-d", "--name", clashSlug, "busybox:1.36", "sleep", "3600").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	unmanaged := strings.TrimSpace(string(out))
	ids = append(ids, unmanaged)
	clashB := build(site, NamesPlain, clashSlug)
	err = plain.CheckNames(ctx, []*Built{build(site, NamesPlain, mqttSlug), clashB})
	wantMsg := "Docker container name `" + clashSlug + "` is already used by a container not managed by this site (id " + unmanaged[:12] + ", image busybox:1.36) — remove or rename it, or set containerNames: prefixed"
	if err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("staging clash error: %v", err)
	}
	if _, err := plain.CreateStart(ctx, clashB, "R1", 1); err == nil || !strings.Contains(err.Error(), "not managed by this site") {
		t.Fatalf("create over an unmanaged container must fail: %v", err)
	}
	if cj, err := dc.ContainerInspect(ctx, unmanaged); err != nil || !cj.State.Running || strings.TrimPrefix(cj.Name, "/") != clashSlug {
		t.Fatalf("the unmanaged container must be untouched: %v", err)
	}
	t.Logf("ok  clash: %v", plain.CheckNames(ctx, []*Built{clashB}))

	// 4. a managed container of another site with the same name
	other := drv("site-02", NamesPlain)
	sharedSlug := "shared-" + sfx
	otherID := start(other, build("site-02", NamesPlain, sharedSlug))
	err = plain.CheckNames(ctx, []*Built{build(site, NamesPlain, sharedSlug)})
	if err == nil || !strings.Contains(err.Error(), "not managed by this site (id "+otherID[:12]) || !strings.Contains(err.Error(), "site site-02") {
		t.Fatalf("other-site clash: %v", err)
	}
	// and the other site's containers are never renamed by this site's migration
	if done, _, _ := pre.MigrateNames(ctx); len(done) != 3 {
		t.Fatalf("prefixed migration renames this site's 3 containers only: %+v", done)
	}
	if n := nameOf(otherID); n != sharedSlug {
		t.Fatalf("other site's container renamed: %q", n)
	}
	t.Logf("ok  other-site clash: %v", err)
}
