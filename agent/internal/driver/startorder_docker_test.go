package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// TestDockerStartOrderDNS (real Docker — Colima locally; also inside docker:28.5.2-dind, see dind28_test.go for the
// commands, run with -test.run 'TestDockerStartOrderDNS'; §21 #93) replays the user's failed update seq 3:
//
//  1. an OLD-style prefixed `opm_<dep>_<site>_mqtt` (alias mqtt) runs → MigrateNames renames it to `mqtt` → its endpoint
//     still carries the slug alias, and a NEW backend container that resolves `mqtt` at its first instruction (exit 1
//     otherwise, like the user's Deno app) is ready at once; EnsureAliases has nothing to repair.
//  2. the same update recreates BOTH containers with no depends, backend sorting BEFORE mqtt: between the stop of mqtt and
//     its create the name does NOT resolve (evidence: the gap); the OLD serial apply (wait for backend before creating
//     mqtt) fails exactly like the user's ("container is restarting: last exit code 1") and the log hint names mqtt;
//     the NEW RunStarts apply (parallel readiness, start order) completes.
//  3. EnsureAliases repairs a running managed container that lost its slug alias (reconnected without aliases).
func TestDockerStartOrderDNS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if out, err := exec.Command("docker", "image", "inspect", "busybox:1.36").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("docker", "pull", "busybox:1.36").CombinedOutput(); err2 != nil {
			t.Skipf("no docker / busybox: %s %s", out, out2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dc := docker.New(docker.DefaultHost(""))
	v, err := dc.Version(ctx)
	if err != nil {
		t.Skip("no docker: ", err)
	}
	api, _ := dc.Negotiate(ctx)
	t.Logf("Docker %s (API %s → %s)", v.Version, v.APIVersion, api)
	base, err := os.MkdirTemp("", "opm-order-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	img, blob := busyboxBlobs(t, base)
	if err := LoadImage(ctx, dc, img, blob); err != nil {
		t.Fatalf("load: %v", err)
	}
	sfx := fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff)
	sfx = strings.Map(func(r rune) rune { return 'a' + (r % 26) }, sfx)
	dep, site := "DepOrder"+sfx, "site-01"
	log := dlog.New(filepath.Join(base, "logs"), "debug", nil)
	drv := func(mode string, complete time.Duration) *Driver {
		d := New(dc, dep, site, filepath.Join(base, "agent"), log, complete)
		d.Names = mode
		d.NoneReadyAfter = 3 * time.Second
		return d
	}
	build := func(mode, slug, restart, cmd string) *Built {
		cfg, cat := fixture("green")
		ctr := &cfg.Sites[0].Config.Containers[0]
		ctr.ContainerSlug = slug
		ctr.ServiceSlug, ctr.Version = "bb", "1.36"
		ctr.Env, ctr.Volumes, ctr.Ports = nil, nil, nil
		ctr.Restart = restart
		ctr.Command = []string{"sh", "-c", cmd}
		ctr.Healthcheck = nil
		spec := cat.Images["web:v1"]
		delete(cat.Images, "web:v1")
		spec.ServiceSlug, spec.Version, spec.ConfigDigest, spec.Layers = "bb", "1.36", img.ConfigDigest, img.Layers
		spec.Ports, spec.Env, spec.Volumes, spec.Healthcheck = nil, nil, nil, nil
		cat.Images["bb:1.36"] = spec
		cat.Services = map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}
		b, err := Build(BuildParams{DataDir: filepath.Join(base, "agent"), DeploymentID: dep, SiteSlug: site, RevID: "R1", RevSeq: 1, Config: cfg, Catalog: cat, Container: ctr, Names: mode})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	t.Cleanup(func() {
		list, _ := dc.ContainerList(context.Background(), true, LDeployment+"="+dep)
		for _, c := range list {
			_ = dc.ContainerRemove(context.Background(), c.ID, true)
		}
		_ = dc.NetworkRemove(context.Background(), NetworkName(dep, site))
	})
	mqttSlug, backSlug := "mqtt-"+sfx, "backend-"+sfx // backend < mqtt in slug order, like northwind-backend-admin < mqtt
	if backSlug > mqttSlug {
		t.Fatal("fixture: backend must sort before mqtt")
	}
	mqttCmd := "sleep 3600"
	backCmd := fmt.Sprintf(`nslookup %s 127.0.0.11 >/dev/null 2>&1 || { echo "MQTT connection error: Error: getaddrinfo EAI_AGAIN %s" >&2; exit 1; }; echo connected; sleep 3600`, mqttSlug, mqttSlug)
	endpoint := func(id string) docker.EndpointSettings {
		cj, err := dc.ContainerInspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return cj.NetworkSettings.Networks[NetworkName(dep, site)]
	}
	resolves := func(fromID, name string) bool {
		code, _, err := dc.Exec(ctx, fromID, []string{"nslookup", name, "127.0.0.11"})
		return err == nil && code == 0
	}

	// 1. prefixed mqtt → MigrateNames → plain; a new backend resolves mqtt at once
	pre, plain := drv(NamesPrefixed, time.Minute), drv(NamesPlain, time.Minute)
	mqttID, err := pre.CreateStart(ctx, build(NamesPrefixed, mqttSlug, "always", mqttCmd), "R1", 1)
	if err != nil {
		t.Fatal(err)
	}
	ep0 := endpoint(mqttID)
	done, skipped, err := plain.MigrateNames(ctx)
	if err != nil || len(done) != 1 || len(skipped) != 0 {
		t.Fatalf("migrate: %+v %v %v", done, skipped, err)
	}
	ep1 := endpoint(mqttID)
	t.Logf("mqtt endpoint before rename: Aliases %v DNSNames %v; after rename to %s: Aliases %v DNSNames %v", ep0.Aliases, ep0.DNSNames, mqttSlug, ep1.Aliases, ep1.DNSNames)
	if !hasName(ep1.Aliases, mqttSlug) && !hasName(ep1.DNSNames, mqttSlug) {
		t.Fatalf("the rename lost the slug alias: %+v", ep1)
	}
	backB := build(NamesPlain, backSlug, "never", backCmd)
	backID, err := plain.CreateStart(ctx, backB, "R1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.WaitCompleted(ctx, backB, backID); err != nil {
		t.Fatalf("a backend created after the migration must resolve %s at once: %v", mqttSlug, err)
	}
	if rep, err := plain.EnsureAliases(ctx); err != nil || len(rep) != 0 {
		t.Fatalf("nothing to repair after a migration: %+v %v", rep, err)
	}
	t.Logf("ok  1. renamed %s → %s keeps the slug alias; %s resolved it at its first instruction", done[0].From, done[0].To, backSlug)

	// 2. both recreated in the same update, no depends
	probeB := build(NamesPlain, "probe-"+sfx, "always", "sleep 3600")
	probeID, err := plain.CreateStart(ctx, probeB, "R1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !resolves(probeID, mqttSlug) {
		t.Fatal("mqtt must resolve before the update")
	}
	ex, _ := plain.Existing(ctx)
	_ = plain.StopRemove(ctx, ex[backSlug].ID, 2) // APPLY step 1: stop + remove (dependents first; no depends → reverse slug order)
	_ = plain.StopRemove(ctx, ex[mqttSlug].ID, 2)
	gap := resolves(probeID, mqttSlug)
	t.Logf("between the stop of %s and its create: resolves = %v (the gap: no container answers on the name)", mqttSlug, gap)
	if gap {
		t.Fatal("expected the name to be unresolvable while the container does not exist")
	}
	// OLD apply: serial in slug order, waiting for each container before creating the next → the user's failure
	serial := drv(NamesPlain, 20*time.Second)
	backB2 := build(NamesPlain, backSlug, "always", backCmd)
	id, err := serial.CreateStart(ctx, backB2, "R1", 1)
	if err != nil {
		t.Fatal(err)
	}
	werr := serial.WaitCompleted(ctx, backB2, id)
	if werr == nil || !strings.Contains(werr.Error(), "last exit code 1") {
		t.Fatalf("the serial apply must reproduce the crash loop: %v", werr)
	}
	lines, _ := dc.ContainerLogs(ctx, id, 50, "")
	var tail []string
	for _, l := range lines {
		tail = append(tail, l.Ts+" "+l.Stream+" "+l.Text)
	}
	h := validate.DNSFailureHintOf(tail, []string{backSlug, mqttSlug, "probe-" + sfx}, backSlug)
	if h == nil || h.ContainerSlug != mqttSlug || h.Code != "EAI_AGAIN" {
		t.Fatalf("hint from %v: %+v", tail, h)
	}
	t.Logf("ok  2a. old serial apply: %v → hint %q", werr, validate.DNSFailureMessage(backSlug, h))
	_ = serial.StopRemove(ctx, id, 2)
	// NEW apply: RunStarts in the start order (no references → declared order: backend, mqtt) with parallel readiness
	mqttB := build(NamesPlain, mqttSlug, "always", mqttCmd)
	steps := []StartStep{}
	for _, b := range []*Built{backB2, mqttB} {
		b := b
		steps = append(steps, StartStep{Slug: b.ContainerSlug,
			Start: func(ctx context.Context) (string, error) { return plain.CreateStart(ctx, b, "R1", 2) },
			Ready: func(ctx context.Context, id string) error { return plain.WaitCompleted(ctx, b, id) }})
	}
	t0 := time.Now()
	for _, r := range RunStarts(ctx, steps, true, nil) {
		if r.Err != nil {
			t.Fatalf("new apply: %s: %v", r.Slug, r.Err)
		}
	}
	t.Logf("ok  2b. new apply (parallel readiness): both ready in %s", time.Since(t0).Round(time.Millisecond))
	ex, _ = plain.Existing(ctx)
	if ep := endpoint(ex[mqttSlug].ID); !hasName(ep.Aliases, mqttSlug) && !hasName(ep.DNSNames, mqttSlug) {
		t.Fatalf("recreated mqtt without its slug alias: %+v", ep)
	}

	// 3. alias self-check repairs a lost slug alias (a prefixed-name container: there the slug is ONLY an alias)
	cacheSlug := "cache-" + sfx
	cacheID, err := pre.CreateStart(ctx, build(NamesPrefixed, cacheSlug, "always", "sleep 3600"), "R1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !resolves(probeID, cacheSlug) {
		t.Fatal("fixture: the alias must resolve first")
	}
	net := NetworkName(dep, site)
	if err := dc.NetworkDisconnect(ctx, net, cacheID, true); err != nil {
		t.Fatal(err)
	}
	if err := dc.NetworkConnect(ctx, net, cacheID, nil); err != nil {
		t.Fatal(err)
	}
	if resolves(probeID, cacheSlug) {
		t.Fatalf("fixture: %s still resolves without its alias: %+v", cacheSlug, endpoint(cacheID))
	}
	rep, err := pre.EnsureAliases(ctx)
	if err != nil || len(rep) != 1 || rep[0].ContainerSlug != cacheSlug {
		t.Fatalf("repair: %+v %v", rep, err)
	}
	if ep := endpoint(cacheID); !hasName(ep.Aliases, cacheSlug) || !resolves(probeID, cacheSlug) {
		t.Fatalf("after the repair: %+v", ep)
	}
	t.Logf("ok  3. EnsureAliases repaired %s (%s)", rep[0].ContainerSlug, rep[0].Reason)
}
