package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

// TestCaptureFailureKeepsContainer (real Docker): diagnostics of a crash-looping container (logs with a secret
// redacted, exit code, restarts, port map, health history), the container kept as <name>-failed-<unix> outside every
// plan, the capped activity data, and the cleanup after the next successful update (§21 #87).
func TestCaptureFailureKeepsContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if out, err := exec.Command("docker", "image", "inspect", "busybox:1.36").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("docker", "pull", "busybox:1.36").CombinedOutput(); err2 != nil {
			t.Skipf("no docker / busybox: %s %s", out, out2)
		}
	}
	a := newTestAgent(t)
	ctx := context.Background()
	if _, err := a.dc.Version(ctx); err != nil {
		t.Skip("no docker: ", err)
	}
	a.log.AddSecrets("s3cr3t-value-123")
	name := a.drv.NameFor("admin")
	_ = exec.Command("docker", "rm", "-f", name).Run()
	cc := &docker.ContainerConfig{Image: "busybox:1.36", Cmd: []string{"sh", "-c", `echo "fatal: cannot connect with password s3cr3t-value-123" >&2; sleep 0.5; exit 3`},
		Labels: map[string]string{driver.LDeployment: a.dep, driver.LSite: a.site, driver.LContainer: "admin"}, ExposedPorts: map[string]struct{}{"8080/tcp": {}},
		HostConfig: docker.HostConfig{RestartPolicy: docker.RestartPolicy{Name: "always"}, PortBindings: map[string][]docker.PortBinding{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: ""}}}}}
	id, err := a.dc.ContainerCreate(ctx, name, cc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.dc.ContainerRemove(context.Background(), id, true) })
	if err := a.dc.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	// capture while Docker is between restarts (a just-restarted container reports ExitCode 0 while it runs)
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if cj, err := a.dc.ContainerInspect(ctx, id); err == nil && (cj.State.Restarting || !cj.State.Running) && cj.State.ExitCode == 3 {
			break
		}
	}
	b := &driver.Built{ContainerSlug: "admin", Slug: "admin", Name: name}
	f := a.captureFailure("admin", b, id, errStr("admin: unhealthy: container is restarting"))
	if f.State == nil || f.State.ExitCode != 3 || f.State.RestartCount < 1 {
		t.Fatalf("state %+v", f.State)
	}
	logs := strings.Join(f.LogTail, "\n")
	if !strings.Contains(logs, "fatal: cannot connect") || strings.Contains(logs, "s3cr3t-value-123") || !strings.Contains(logs, "[redacted]") {
		t.Fatalf("log tail (secret must be redacted): %s", logs)
	}
	if f.KeptAs == nil || !driver.IsFailedName(*f.KeptAs) {
		t.Fatalf("keptAs %v", f.KeptAs)
	}
	cj, err := a.dc.ContainerInspect(ctx, id)
	if err != nil || cj.State.Running || strings.TrimPrefix(cj.Name, "/") != *f.KeptAs {
		t.Fatalf("kept container must be stopped and renamed: %v %+v %s", err, cj.State, cj.Name)
	}
	ex, _ := a.drv.Existing(ctx)
	if _, ok := ex["admin"]; ok {
		t.Fatal("a kept failed container must not be part of a plan")
	}
	// activity data: capped, includes the log tail
	st := &model.UpdateSiteState{Containers: []model.UpdateContainerState{{ContainerSlug: "admin", State: "failed", Failure: f}}}
	fs := failureActivityData(st)
	if len(fs) != 1 || fs[0]["exitCode"] != 3 || len(fs[0]["logTail"].([]string)) == 0 || jsonLen(fs) > activityFailBytes {
		t.Fatalf("activity data %+v", fs)
	}
	big := *f
	for i := 0; i < 500; i++ {
		big.LogTail = append(big.LogTail, strings.Repeat("x", 1900))
	}
	st.Containers = append(st.Containers, model.UpdateContainerState{ContainerSlug: "b2", Failure: &big}, model.UpdateContainerState{ContainerSlug: "b3", Failure: &big})
	if n := jsonLen(failureActivityData(st)); n > activityFailBytes {
		t.Fatalf("activity data not capped: %d", n)
	}
	// 24 h rule keeps it; a successful update removes it
	a.cleanupFailedContainers(ctx, false)
	if _, err := a.dc.ContainerInspect(ctx, id); err != nil {
		t.Fatal("a fresh kept container must survive the 24 h housekeeping")
	}
	a.cleanupFailedContainers(ctx, true)
	if _, err := a.dc.ContainerInspect(ctx, id); err == nil {
		t.Fatal("the next successful update must remove kept containers")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
