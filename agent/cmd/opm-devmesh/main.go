// Command opm-devmesh starts an in-memory fake cloud and two agents (sites app + db) on the §20 ports for local GUI
// work and smoke tests (no Firebase needed):
//
//	go run ./cmd/opm-devmesh [-dir ~/.opm-devmesh] [-key ABCDEFGHIJKLMNOPQRSTUVWX] [-approve-wait 2] [-devtools] [-volume-content] [-branches]
//
// app: agentPort 7080, GUI alias 8090; db: agentPort 7081, GUI alias 8091. Ctrl-C stops everything.
//
// The fake cloud lives in memory, so every start is a NEW cloud: the agent data dirs are wiped at start (an agent
// enrolled with a previous run's cloud is unknown to the new one and would never receive a proposal). -keep-data keeps
// them (GUI state only; such agents cannot talk to the new cloud).
//
// -devtools (§21 #97) also starts postgres / redis / mosquitto / httpbin containers and publishes a revision with
// dev-tool presets for them on site db (approve it in the GUI; the containers are removed on exit).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"opm/agent/internal/agent"
	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
)

// meshOptions configure one devmesh (flags in main, free ports in tests).
type meshOptions struct {
	Dir         string
	Key         string
	Deployment  string
	ApproveWait time.Duration
	DevTools    bool
	KeepData    bool
	// VolumeContent (§21 #101): publish a persist-to-cloud volume on app + a demo cloud edit / send after its first snapshot
	VolumeContent bool
	// Branches (§21 #104): seed the deployment branch with 2 commits (no proposals)
	Branches bool
	// ports: app agent / GUI alias, db agent / GUI alias, edge HTTP of app (db = +1)
	AppPort, AppAlias, DBPort, DBAlias, EdgePort int
	Out                                          io.Writer
}

type mesh struct {
	fc      *fakecloud.Cloud
	done    chan struct{}
	cleanup func()
	revID   string
}

func defaultOptions() meshOptions {
	home, _ := os.UserHomeDir()
	return meshOptions{Dir: filepath.Join(home, ".opm-devmesh"), Key: "ABCDEFGHIJKLMNOPQRSTUVWX", Deployment: "DepDevMesh0000000001",
		ApproveWait: 2 * time.Second, AppPort: 7080, AppAlias: 8090, DBPort: 7081, DBAlias: 8091, EdgePort: 8181, Out: os.Stdout}
}

// startMesh starts the fake cloud + both agents (+ the dev tools services and preset revision). Stop with ctx.
func startMesh(ctx context.Context, o meshOptions) (*mesh, error) {
	if !o.KeepData {
		for _, s := range []string{"app", "db"} {
			if err := os.RemoveAll(filepath.Join(o.Dir, s)); err != nil {
				return nil, fmt.Errorf("reset %s: %w", s, err)
			}
		}
	}
	fc := fakecloud.Start(o.Deployment)
	fc.SetAccessKey(o.Key)
	m := &mesh{fc: fc, done: make(chan struct{}, 2), cleanup: func() {}}
	for _, s := range []struct {
		slug        string
		port, alias int
	}{{"app", o.AppPort, o.AppAlias}, {"db", o.DBPort, o.DBAlias}} {
		d := filepath.Join(o.Dir, s.slug)
		_ = os.MkdirAll(d, 0o700)
		tok := filepath.Join(d, "install-token")
		if _, err := os.Stat(filepath.Join(d, "data", "opm.db")); err != nil {
			_ = os.WriteFile(tok, []byte(fc.CreateInstallToken(s.slug)), 0o600)
		} else {
			fmt.Fprintf(o.Out, "WARNING: %s keeps the data of a previous run (-keep-data): it is not enrolled with this fake cloud and will not see proposals\n", s.slug)
		}
		cfg := model.AgentConfigFile{APIBase: fc.URL, DeploymentID: o.Deployment, SiteSlug: s.slug, DataDir: filepath.Join(d, "data"), AgentPort: s.port,
			GuiAliasPort: model.Some(s.alias), AdvertiseHost: "127.0.0.1", InstallTokenFile: tok,
			EdgeHTTPPort: model.Null[int](), ContainerNames: "prefixed"} // several sites on one Docker host (§21 #88)
		a, err := agent.New(agent.Options{Config: cfg, ApproveWait: o.ApproveWait, LogLevel: "info", LogEcho: os.Stderr})
		if err != nil {
			fc.Close()
			return nil, err
		}
		go func() { _ = a.Run(ctx); m.done <- struct{}{} }()
	}
	if o.DevTools {
		svcs, cleanup, err := startDevServices(o.Dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "devtools:", err)
		} else {
			m.cleanup = cleanup
			if err := waitEnrolled(fc, 60*time.Second); err != nil {
				fmt.Fprintln(os.Stderr, "devtools:", err)
			}
			m.revID = publishDevToolsRevision(fc, svcs, o)
		}
	}
	if o.Branches { // §21 #104 (branches.go)
		if err := waitEnrolled(fc, 60*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "branches:", err)
		}
		if err := seedBranchCommits(fc, o); err != nil {
			fmt.Fprintln(os.Stderr, "branches:", err)
		}
		prev := m.cleanup
		m.cleanup = func() { prev(); removeDeploymentContainers(o.Deployment) } // the -branches containers go on exit
	}
	if o.VolumeContent { // §21 #101 (volcontent.go)
		if err := waitEnrolled(fc, 60*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "volume content:", err)
		}
		m.revID = publishVolumeContentRevision(fc, o)
		go demoCloudEdit(ctx, fc, o, m.revID)
	}
	return m, nil
}

// waitEnrolled waits until both agents enrolled with the fake cloud (a proposal published earlier is still fetched,
// but the agents only report staging once they are known).
func waitEnrolled(fc *fakecloud.Cloud, max time.Duration) error {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		_, a := fc.Agent("app")
		_, d := fc.Agent("db")
		if a && d {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the agents did not enroll within %s", max)
}

func (m *mesh) stop() {
	m.cleanup()
	<-m.done
	<-m.done
	m.fc.Close()
}

func main() {
	o := defaultOptions()
	flag.StringVar(&o.Dir, "dir", o.Dir, "data root (must be under $HOME for Colima bind mounts); app/ and db/ are reset at start")
	flag.StringVar(&o.Key, "key", o.Key, "access key (24 chars A–Z 2–7)")
	flag.StringVar(&o.Deployment, "deployment", o.Deployment, "deployment id")
	wait := flag.Int("approve-wait", 2, "approve wait seconds")
	flag.BoolVar(&o.DevTools, "devtools", false, "start service containers + publish dev-tool presets on site db (§21 #97)")
	flag.BoolVar(&o.VolumeContent, "volume-content", false, "publish a persist-to-cloud volume (app/flows, own starter content) + send a demo cloud edit after its first snapshot (§21 #101)")
	flag.BoolVar(&o.Branches, "branches", false, "seed the deployment branch with 2 commits (§21 #104; no proposals: merge on the Branch page)")
	flag.BoolVar(&o.KeepData, "keep-data", false, "keep the agent data of a previous run (those agents are NOT enrolled with the new fake cloud)")
	flag.Parse()
	o.ApproveWait = time.Duration(*wait) * time.Second
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m, err := startMesh(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("fake cloud %s\napp GUI http://127.0.0.1:%d/_opm/  db GUI http://127.0.0.1:%d/_opm/\naccess key %s\n", m.fc.URL, o.AppAlias, o.DBAlias, o.Key)
	<-ctx.Done()
	m.stop()
}
