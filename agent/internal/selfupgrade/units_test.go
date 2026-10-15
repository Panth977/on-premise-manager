package selfupgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func TestServiceUnitIsSpec192(t *testing.T) {
	defer func(f func() int64) { hostMemBytes = f }(hostMemBytes)
	hostMemBytes = func() int64 { return 0 }
	// §21 #112: Wants (not Requires) docker, NotifyAccess=main (the agent turns the watchdog on itself)
	want := "[Unit]\nDescription=On-Prem Manager agent\nAfter=network-online.target docker.service\nWants=network-online.target docker.service\n[Service]\nType=simple\nExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json\nRestart=always\nRestartSec=5\nLimitNOFILE=65536\nKillMode=process\nNotifyAccess=main\n[Install]\nWantedBy=multi-user.target\n"
	if got := ServiceUnit("", "", false); got != want {
		t.Fatalf("unit differs from §19.2 + §21 #112:\n%s", got)
	}
	if s := ServiceUnit("", "", true); !strings.Contains(s, "After=network-online.target docker.service opm-agent.socket\n") || !strings.Contains(s, "Requires=opm-agent.socket\n") || strings.Contains(s, "Requires=docker") {
		t.Fatal(s)
	}
	// MemoryHigh from the host RAM: 4 GiB → 2 GiB (2 × the 1 GiB Go limit); 1 GiB → 512 MiB (half the RAM)
	hostMemBytes = func() int64 { return 4 << 30 }
	if s := ServiceUnit("", "", false); !strings.Contains(s, "MemoryHigh=2048M\n") {
		t.Fatal(s)
	}
	if h := MemoryHighFor(1 << 30); h != 512<<20 {
		t.Fatal(h)
	}
}

func TestSocketPorts(t *testing.T) {
	var c model.AgentConfigFile
	_ = json.Unmarshal([]byte(`{"apiBase":"x","deploymentId":"d","siteSlug":"s"}`), &c)
	if got := SocketPorts(c); !reflect.DeepEqual(got, []int{80, 7080, 8080}) {
		t.Fatal(got)
	}
	_ = json.Unmarshal([]byte(`{"apiBase":"x","deploymentId":"d","siteSlug":"s","edgeHttpPort":null,"agentPort":7090,"guiAliasPort":null}`), &c)
	if got := SocketPorts(c); !reflect.DeepEqual(got, []int{7090}) {
		t.Fatal(got)
	}
}

func TestInstallUnitsSocketOnOff(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agent.json")
	_ = os.WriteFile(cfg, []byte(`{"apiBase":"x","deploymentId":"d","siteSlug":"s","edgeHttpPort":8081,"agentPort":7080}`), 0o644)
	units := filepath.Join(dir, "units")
	o := UnitOptions{ConfigPath: cfg, DataDir: "/var/lib/opm", Socket: true, UnitDir: units, ScriptPath: filepath.Join(dir, "guard.sh"), Force: true}
	r, err := InstallUnits(o)
	if err != nil || !r.Socket || len(r.Written) != 3 || !reflect.DeepEqual(r.SocketPorts, []int{7080, 8080, 8081}) {
		t.Fatalf("%+v %v", r, err)
	}
	sock, _ := os.ReadFile(filepath.Join(units, SocketFile))
	for _, w := range []string{"ListenStream=7080\n", "ListenStream=8080\n", "ListenStream=8081\n", "FileDescriptorName=opm\n", "Service=opm-agent.service\n", "WantedBy=sockets.target"} {
		if !strings.Contains(string(sock), w) {
			t.Fatalf("socket unit lacks %q:\n%s", w, sock)
		}
	}
	if _, err := os.Stat(filepath.Join(units, "opm-agent.service.d", DropInName)); err != nil {
		t.Fatal("guard drop-in")
	}
	if r, _ := InstallUnits(o); len(r.Written) != 0 {
		t.Fatalf("idempotent: %+v", r)
	}
	o.Socket = false
	r, err = InstallUnits(o)
	if err != nil || r.Socket || len(r.Removed) != 1 || !r.SocketDisabled || SocketInstalled(units) {
		t.Fatalf("no-socket %+v %v", r, err)
	}
	svc, _ := os.ReadFile(filepath.Join(units, ServiceFile))
	if strings.Contains(string(svc), "opm-agent.socket") {
		t.Fatal("service still requires the socket")
	}
}
