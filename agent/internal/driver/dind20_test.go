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

// TestDocker2010 (opt-in, OPM_TEST_DIND20=1) runs the driver against a real Docker 20.10 (Engine API 1.41, the floor):
//
//	docker run -d --privileged --name dind20 -e DOCKER_TLS_CERTDIR= -p 23750:2375 -v "$HOME:$HOME" \
//	  docker:20.10-dind --host=tcp://0.0.0.0:2375 --tls=false
//	OPM_TEST_DIND20=1 go test ./internal/driver -run TestDocker2010 -v
//	docker rm -f dind20
//
// The data dir is under $HOME (bind-mounted into the dind container at the same path, so binds resolve). Steps: negotiate
// (1.41), load busybox through LoadImage (docker-archive from blobs) + readable tag, network, create with ports / a data
// volume / a bind-mounted file / a secret file, start, exec health check, logs, inspect ports, stats, stop, remove.
func TestDocker2010(t *testing.T) {
	if os.Getenv("OPM_TEST_DIND20") != "1" {
		t.Skip("set OPM_TEST_DIND20=1 (and run the dind20 container) to run against Docker 20.10")
	}
	host := os.Getenv("OPM_DIND20_HOST")
	if host == "" {
		host = "tcp://127.0.0.1:23750"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dc := docker.New(host)
	step := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("ok  %s", name)
	}

	// 1. negotiate
	v, err := dc.Version(ctx)
	step("version", err)
	api, err := dc.Negotiate(ctx)
	step("negotiate", err)
	t.Logf("daemon Docker %s, API %s (min %s) → negotiated %s", v.Version, v.APIVersion, v.MinAPIVersion, api)
	if api != "1.41" {
		t.Fatalf("negotiated %s, want 1.41", api)
	}

	// 2. image blobs from the local (default) docker: docker save busybox:1.36
	home, _ := os.UserHomeDir()
	base, err := os.MkdirTemp(home, ".opm-dind20-test-")
	step("data dir under $HOME", err)
	defer os.RemoveAll(base)
	blobDir := filepath.Join(base, "blobs", "sha256")
	_ = os.MkdirAll(blobDir, 0o755)
	tarPath := filepath.Join(base, "busybox.tar")
	if out, err := exec.Command("docker", "save", "busybox:1.36", "-o", tarPath).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("docker", "pull", "busybox:1.36").CombinedOutput(); err2 != nil {
			t.Fatalf("docker pull busybox:1.36: %v %s", err2, out2)
		}
		if out, err = exec.Command("docker", "save", "busybox:1.36", "-o", tarPath).CombinedOutput(); err != nil {
			t.Fatalf("docker save: %v %s", err, out)
		}
	}
	files := map[string]string{} // tar name → blob hex
	f, err := os.Open(tarPath)
	step("open archive", err)
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
	f.Close()
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
	blob := func(hx string) string { return filepath.Join(blobDir, hx) }

	// 3. load (docker-archive assembled from blobs) + verify + readable tag
	step("image load + verify + tag bb:1.36", LoadImage(ctx, dc, img, blob))
	ok, err := ImagePresent(ctx, dc, img, blob)
	if err != nil || !ok {
		t.Fatalf("image present %v %v", ok, err)
	}
	if _, err := dc.ImageList(ctx, "opm/cfg"); err != nil {
		t.Fatalf("image list: %v", err)
	}

	// 4. build a container: port 8080 (published 0.0.0.0:<free> + loopback ephemeral), a data volume, a file mount
	//    (bind of the one site copy), a secret file, exec health check
	cfg, cat := fixture("green")
	content := []byte("hello from docker 20.10\n")
	sum := sha256.Sum256(content)
	fileHex := hex.EncodeToString(sum[:])
	dataDir := filepath.Join(base, "agent")
	_ = os.MkdirAll(filepath.Dir(FileBlobPath(dataDir, fileHex)), 0o755)
	_ = os.WriteFile(FileBlobPath(dataDir, fileHex), content, 0o444)
	cfg.Files[0].Content = &model.BlobRef{Sha256: "sha256:" + fileHex, SizeBytes: int64(len(content))}
	cfg.Variables[1].Value = model.VariableValue{V: float64(freeTCPPort(t))}
	ctr := &cfg.Sites[0].Config.Containers[0]
	ctr.ServiceSlug, ctr.Version = "bb", "1.36"
	ctr.Command = []string{"sh", "-c", "mkdir -p /www && cat /etc/x > /www/index.html && echo dind20-started && echo persisted > /data/marker && exec httpd -f -p 8080 -h /www"}
	ctr.Healthcheck = &model.HealthCheck{Type: "exec", Command: []string{"sh", "-c", "wget -qO- http://127.0.0.1:8080/ | grep -q 'docker 20.10' && test -s /run/tok"}, TimeoutSec: 5}
	spec := cat.Images["web:v1"]
	delete(cat.Images, "web:v1")
	spec.ServiceSlug, spec.Version, spec.ConfigDigest, spec.Layers = "bb", "1.36", img.ConfigDigest, img.Layers
	cat.Images["bb:1.36"] = spec
	cat.Services = map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}
	b, err := Build(BuildParams{DataDir: dataDir, DeploymentID: "DepDind20", SiteSlug: "app", RevID: "R1", RevSeq: 1, Config: cfg, Catalog: cat, Container: ctr})
	step("build spec", err)

	d := New(dc, "DepDind20", "app", dataDir, dlog.New(filepath.Join(base, "logs"), "info", nil), time.Minute)
	if ex, err := d.Existing(ctx); err == nil {
		for _, e := range ex {
			_ = d.StopRemove(ctx, e.ID, 1)
		}
	}
	id, err := d.CreateStart(ctx, b, "R1", 1)
	step("network + create + start (ports, data volume, file bind, secret bind)", err)
	defer func() {
		_ = d.StopRemove(context.Background(), id, 2)
		_ = dc.NetworkRemove(context.Background(), b.Spec.Network)
		_ = dc.ImageRemove(context.Background(), "bb:1.36")
		_ = dc.ImageRemove(context.Background(), ImageRef(img.ConfigDigest))
	}()

	// 5. ports published (loopback ephemeral + the configured host port)
	loop, cj, err := d.LoopPorts(ctx, id, b)
	step("inspect + loopback ports", err)
	if loop["http"] == 0 || len(cj.NetworkSettings.Ports["8080/tcp"]) < 2 {
		t.Fatalf("ports %+v", cj.NetworkSettings.Ports)
	}
	t.Logf("    ports %+v", cj.NetworkSettings.Ports["8080/tcp"])

	// 6. exec health check passes (http server inside, file + secret mounts readable)
	var hcErr error
	for i := 0; i < 30; i++ {
		if hcErr = d.CheckOnce(ctx, b, id, loop, time.Now().Add(-time.Minute)); hcErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	step("exec health check (file bind + secret bind + httpd)", hcErr)

	// 7. logs
	lines, err := dc.ContainerLogs(ctx, id, 50, "")
	step("logs", err)
	found := false
	for _, l := range lines {
		found = found || strings.Contains(l.Text, "dind20-started")
	}
	if !found {
		t.Fatalf("logs %+v", lines)
	}

	// 8. data volume is a bind of the host data dir
	var marker []byte
	for i := 0; i < 20; i++ {
		if marker, err = os.ReadFile(filepath.Join(b.DataDirs[0], "marker")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if strings.TrimSpace(string(marker)) != "persisted" {
		t.Fatalf("data volume marker %q (%v) in %v", marker, err, b.DataDirs)
	}
	t.Logf("ok  data volume written through the bind (%s)", b.DataDirs[0])

	// 9. stats, list by label, stop (own timeout), start again, stop + remove
	_, err = dc.ContainerStats(ctx, id)
	step("stats", err)
	ex, err := d.Existing(ctx)
	step("list by labels", err)
	if _, ok := ex["web"]; !ok {
		t.Fatalf("existing %+v", ex)
	}
	step("stop", d.Stop(ctx, id, 2))
	running, _, err := d.IsRunning(ctx, id)
	if err != nil || running {
		t.Fatalf("running after stop: %v %v", running, err)
	}
	step("start again", dc.ContainerStart(ctx, id))
	step("stop + remove", d.StopRemove(ctx, id, 2))
	if _, err := dc.ContainerInspect(ctx, id); err == nil {
		t.Fatal("container still exists")
	}
	step("network remove", dc.NetworkRemove(ctx, b.Spec.Network))
}

func freeTCPPort(t *testing.T) int {
	for p := 21000; p < 21100; p++ {
		if PortFree(p, "tcp") {
			return p
		}
	}
	t.Fatal("no free port")
	return 0
}
