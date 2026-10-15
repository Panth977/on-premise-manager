package docker

import (
	"context"
	"testing"
	"time"
)

func TestParseLogs(t *testing.T) {
	raw := []byte{1, 0, 0, 0, 0, 0, 0, 14}
	raw = append(raw, []byte("2024-01-01 hi\n")...)
	raw = append(raw, 2, 0, 0, 0, 0, 0, 0, 10)
	raw = append(raw, []byte("2024 oops\n")...)
	l := parseLogs(raw)
	if len(l) != 2 || l[0].Stream != "stdout" || l[0].Text != "hi" || l[1].Stream != "stderr" || l[1].Text != "oops" {
		t.Fatalf("%+v", l)
	}
}

// TestDaemon runs only when a Docker daemon is reachable.
func TestDaemon(t *testing.T) {
	c := New(DefaultHost(""))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skip("no docker:", err)
	}
	v, err := c.Version(ctx)
	if err != nil || v.APIVersion == "" {
		t.Fatal(err)
	}
	t.Logf("docker %s, server API %s (min %s), negotiated %s", v.Version, v.APIVersion, v.MinAPIVersion, c.APIVersion())
	if want, _ := chooseAPI(v); c.APIVersion() != want {
		t.Fatalf("negotiated %q want %q", c.APIVersion(), want)
	}
	if _, err := c.ImageInspect(ctx, "opm-does-not-exist:none"); err == nil {
		t.Fatal("expected not found")
	}
	if err := c.EnsureNetwork(ctx, "opm_unit_test_net", map[string]string{"opm.test": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureNetwork(ctx, "opm_unit_test_net", nil); err != nil {
		t.Fatal(err)
	}
	_ = c.NetworkRemove(ctx, "opm_unit_test_net")
}
