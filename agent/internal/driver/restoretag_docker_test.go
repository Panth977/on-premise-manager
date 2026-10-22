package driver

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// §21 #120: opm/cfg:<config hex> removed (docker image prune -a) while a container created from it runs → ImagePresent
// puts the tag back on the container's image (no reload); without such a container it stays absent.
func TestRestoreIntegrityTagDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dc := docker.New(docker.DefaultHost(""))
	ctx := context.Background()
	if err := dc.Ping(ctx); err != nil {
		t.Skip("no docker: ", err)
	}
	run := func(args ...string) string {
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	_ = exec.Command("docker", "pull", "-q", "busybox:1.36").Run()
	hex := "00000000000000000000000000000000000000000000000000000000c0ffee20"
	img := model.ImageSpec{ConfigDigest: "sha256:" + hex}
	ref := ImageRef(img.ConfigDigest)
	run("tag", "busybox:1.36", ref)
	name := "opm-restoretag-test"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	run("create", "--name", name, ref, "sleep", "1")
	defer func() { _ = exec.Command("docker", "rm", "-f", name).Run(); _ = exec.Command("docker", "rmi", ref).Run() }()
	run("rmi", ref) // untag only (busybox:1.36 keeps the image)
	noBlob := func(string) string { return "/nonexistent" }
	ok, err := ImagePresent(ctx, dc, img, noBlob)
	if err != nil || !ok {
		t.Fatalf("restored: %v %v", ok, err)
	}
	if _, err := dc.ImageInspect(ctx, ref); err != nil {
		t.Fatal("the tag is back")
	}
	other := model.ImageSpec{ConfigDigest: "sha256:" + strings.Repeat("1", 64)}
	if ok, _ := ImagePresent(ctx, dc, other, noBlob); ok {
		t.Fatal("no container from it: absent")
	}
}
