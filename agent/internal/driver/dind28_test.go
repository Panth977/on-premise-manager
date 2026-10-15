package driver

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// TestDocker28HealthReplay (opt-in, OPM_TEST_DIND28=1) replays the user's failed first update (§21 #87) against a real
// Docker 28.5.2: an http health check {portName, path /health, interval, retries 3, startPeriod} on a port that ALSO has a
// fixed host port on 0.0.0.0. The test binary must run INSIDE the dind container (the loopback publications live in its
// network namespace):
//
//	docker run -d --privileged --name dind28 -e DOCKER_TLS_CERTDIR= -v "$HOME:$HOME" docker:28.5.2-dind --tls=false
//	GOOS=linux GOARCH=$(go env GOARCH) CGO_ENABLED=0 go test -c -o "$HOME/.cache/opm-driver.test" ./internal/driver
//	docker exec -e OPM_TEST_DIND28=1 dind28 sh -c 'docker pull -q busybox:1.36 && '"$HOME"'/.cache/opm-driver.test -test.run TestDocker28HealthReplay -test.v'
//
// The same binary runs TestDockerStartOrderDNS (§21 #93: rename keeps the slug alias, the stop→create gap, the old serial
// apply vs RunStarts, alias repair) on Docker 28.5.2: -test.run 'TestDockerStartOrderDNS|TestDocker28HealthReplay'.
//
// Cases: (1) healthy app (/health → 200 on 8080 after a 5 s delay) → completed; (2) /health → 500 → fails after the start
// period with "GET /health → 500", the logs hold the app's own error line; (3) crash loop (exit 3, restart always) →
// fails with "container is restarting: last exit code 3" — the case that showed up as "port server is not published on
// loopback" before the fix (Docker reports Running + Restarting with an EMPTY port map during the restart backoff).
func TestDocker28HealthReplay(t *testing.T) {
	if os.Getenv("OPM_TEST_DIND28") != "1" {
		t.Skip("set OPM_TEST_DIND28=1 and run inside a docker:28.5.2-dind container (see the comment)")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dc := docker.New(host)
	v, err := dc.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	api, err := dc.Negotiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Docker %s (API %s) → negotiated %s", v.Version, v.APIVersion, api)
	base, err := os.MkdirTemp("", "opm-dind28-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	img, blob := busyboxBlobs(t, base)
	if err := LoadImage(ctx, dc, img, blob); err != nil {
		t.Fatalf("load: %v", err)
	}

	run := func(name, cmd string, hc *model.HealthCheck) (*Driver, *Built, string, error) {
		cfg, cat := fixture("green")
		cfg.Variables[1].Value = model.VariableValue{V: float64(16000 + time.Now().Nanosecond()%1000)}
		ctr := &cfg.Sites[0].Config.Containers[0]
		ctr.ContainerSlug = name
		ctr.ServiceSlug, ctr.Version = "bb", "1.36"
		ctr.Env = []model.ContainerEnv{{Name: "THEME", Value: ptrS("{{ var.theme }}")}}
		ctr.Volumes = nil
		ctr.Restart = "always"
		ctr.Command = []string{"sh", "-c", cmd}
		ctr.Healthcheck = hc
		spec := cat.Images["web:v1"]
		delete(cat.Images, "web:v1")
		spec.ServiceSlug, spec.Version, spec.ConfigDigest, spec.Layers = "bb", "1.36", img.ConfigDigest, img.Layers
		cat.Images["bb:1.36"] = spec
		cat.Services = map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}
		b, err := Build(BuildParams{DataDir: filepath.Join(base, "agent"), DeploymentID: "DepDind28", SiteSlug: "site-01", RevID: "R1", RevSeq: 1, Config: cfg, Catalog: cat, Container: ctr})
		if err != nil {
			t.Fatal(err)
		}
		d := New(dc, "DepDind28", "site-01", filepath.Join(base, "agent"), dlog.New(filepath.Join(base, "logs"), "info", nil), 2*time.Minute)
		id, err := d.CreateStart(ctx, b, "R1", 1)
		if err != nil {
			t.Fatalf("%s: create: %v", name, err)
		}
		t.Cleanup(func() { _ = d.StopRemove(context.Background(), id, 1) })
		_, cj, _ := d.LoopPorts(ctx, id, b)
		t.Logf("%s: bindings %s", name, PortMapText(cj))
		return d, b, id, d.WaitCompleted(ctx, b, id)
	}
	hc := func(path string) *model.HealthCheck {
		return &model.HealthCheck{Type: "http", PortName: "http", Path: path, ExpectStatus: 200, IntervalSec: 2, TimeoutSec: 2, Retries: 3, StartPeriodSec: 10}
	}

	// 1. healthy after 5 s
	_, _, _, err = run("ok", "echo starting; sleep 5; mkdir -p /www && echo ok > /www/health && echo serving && exec httpd -f -p 8080 -h /www", hc("/health"))
	if err != nil {
		t.Fatalf("healthy app must complete: %v", err)
	}
	t.Log("ok  healthy app completed")

	// 2. /health → 500: clear failure + the app's own log line
	cgi := `mkdir -p /www/cgi-bin && printf '#!/bin/sh\nprintf "Status: 500 Internal Server Error\\r\\nContent-Type: text/plain\\r\\n\\r\\nboom"\n' > /www/cgi-bin/health && chmod +x /www/cgi-bin/health && echo "db unreachable: connection refused" >&2 && exec httpd -f -p 8080 -h /www`
	d, _, id, err := run("bad", cgi, hc("/cgi-bin/health"))
	if err == nil || !strings.Contains(err.Error(), "GET /cgi-bin/health → 500") || !strings.Contains(err.Error(), "3 checks failed in a row") {
		t.Fatalf("500: %v", err)
	}
	t.Logf("ok  500 → %v", err)
	lines, _ := dc.ContainerLogs(ctx, id, 200, "")
	found := false
	for _, l := range lines {
		found = found || strings.Contains(l.Text, "db unreachable")
	}
	if !found {
		t.Fatalf("logs lack the app error: %+v", lines)
	}
	if h := d.History("bad"); len(h) < 4 {
		t.Fatalf("history %+v", h)
	}

	// 3. crash loop (the user's "port server is not published on loopback")
	_, _, _, err = run("crash", `echo "fatal: DB_URL is not set" >&2; sleep 1; exit 3`, hc("/health"))
	if err == nil || !strings.Contains(err.Error(), "container is restarting: last exit code 3") || strings.Contains(err.Error(), "not published on loopback") {
		t.Fatalf("crash loop: %v", err)
	}
	t.Logf("ok  crash loop → %v", err)
}

func ptrS(s string) *string { return &s }

// busyboxBlobs: docker save busybox:1.36 (from the daemon the docker CLI talks to) → blobs + ImageSpec.
func busyboxBlobs(t *testing.T, base string) (model.ImageSpec, func(string) string) {
	t.Helper()
	blobDir := filepath.Join(base, "blobs", "sha256")
	_ = os.MkdirAll(blobDir, 0o755)
	tarPath := filepath.Join(base, "busybox.tar")
	if out, err := exec.Command("docker", "save", "busybox:1.36", "-o", tarPath).CombinedOutput(); err != nil {
		t.Fatalf("docker save busybox:1.36 (pull it first): %v %s", err, out)
	}
	files := map[string]string{}
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var manifest []byte
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, _ := io.ReadAll(tr)
		if h.Name == "manifest.json" {
			manifest = b
			continue
		}
		sum := sha256.Sum256(b)
		hx := hex.EncodeToString(sum[:])
		files[h.Name] = hx
		_ = os.WriteFile(filepath.Join(blobDir, hx), b, 0o444)
	}
	var man []struct {
		Config string
		Layers []string
	}
	if json.Unmarshal(manifest, &man) != nil || len(man) == 0 {
		t.Fatal("bad docker save output")
	}
	img := model.ImageSpec{ServiceSlug: "bb", Version: "1.36", ConfigDigest: "sha256:" + files[man[0].Config], Kind: "service"}
	for _, l := range man[0].Layers {
		img.Layers = append(img.Layers, model.ImageLayer{Digest: "sha256:" + files[l]})
	}
	return img, func(hx string) string { return filepath.Join(blobDir, hx) }
}
