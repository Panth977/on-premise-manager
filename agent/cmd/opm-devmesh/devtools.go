package main

// -devtools (§21 #97): start postgres / redis / mosquitto / go-httpbin containers on the local Docker (published on
// 127.0.0.1, removed on exit) and publish a revision whose site "db" carries dev-tool presets for them (passwords
// as {{ secret.x }}). Approve the proposal in the agent GUI (Updates) to make the presets the head.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

type devService struct {
	name, image string
	port        int
	env, cmd    []string
	hostPort    int
}

// startDevServices starts the containers of one devmesh (label opm.devmesh.dir=<data root>, names unique per run;
// leftovers of a previous run with the same data root are removed first).
func startDevServices(dir string) ([]*devService, func(), error) {
	devLabel := "opm.devmesh.dir=" + dir
	run := fmt.Sprintf("%d", time.Now().UnixNano()%1e8)
	svcs := []*devService{
		{name: "postgres", image: "postgres:17-alpine", port: 5432, env: []string{"POSTGRES_PASSWORD=devmesh-pg-pass"}},
		{name: "redis", image: "redis:7.4-alpine", port: 6379, cmd: []string{"redis-server", "--requirepass", "devmesh-redis-pass"}},
		{name: "mqtt", image: "eclipse-mosquitto:2", port: 1883, cmd: []string{"mosquitto", "-c", "/mosquitto-no-auth.conf"}},
		{name: "httpbin", image: "mccutchen/go-httpbin", port: 8080},
	}
	cleanup := func() {
		ids, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+devLabel).Output()
		if f := strings.Fields(string(ids)); len(f) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f"}, f...)...).Run()
		}
	}
	cleanup()
	for _, s := range svcs {
		args := []string{"run", "-d", "--name", "opm-devmesh-" + s.name + "-" + run, "--label", devLabel, "-p", fmt.Sprintf("127.0.0.1::%d", s.port)}
		for _, e := range s.env {
			args = append(args, "-e", e)
		}
		args = append(append(args, s.image), s.cmd...)
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("docker run %s: %v: %s", s.name, err, out)
		}
		out, err := exec.Command("docker", "port", "opm-devmesh-"+s.name+"-"+run, fmt.Sprintf("%d/tcp", s.port)).Output()
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("docker port %s: %v", s.name, err)
		}
		line := strings.TrimSpace(strings.Split(string(out), "\n")[0])
		s.hostPort, _ = strconv.Atoi(line[strings.LastIndex(line, ":")+1:])
	}
	return svcs, cleanup, nil
}

// publishDevToolsRevision publishes sites app + db (no containers) with secrets and the presets on db.
func publishDevToolsRevision(fc *fakecloud.Cloud, svcs []*devService, o meshOptions) string {
	cfg := validate.EmptyConfig()
	pg, rp := "devmesh-pg-pass", "devmesh-redis-pass"
	cfg.Secrets = []model.SecretDef{{SecretSlug: "pg-pass", Label: "PostgreSQL password", Value: &pg}, {SecretSlug: "redis-pass", Label: "Redis password", Value: &rp}}
	host := "127.0.0.1"
	for i, s := range []struct {
		slug        string
		port, alias int
	}{{"app", o.AppPort, o.AppAlias}, {"db", o.DBPort, o.DBAlias}} {
		sc := validate.DefaultSiteConfig(&host)
		alias := s.alias
		sc.AgentPort, sc.AgentGuiAliasPort = s.port, &alias
		https := (*int)(nil)
		sc.Edge.HTTPPort, sc.Edge.HTTPSPort = o.EdgePort+i, https
		cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s.slug, Label: s.slug, Config: sc})
	}
	hp := func(name string) model.DevToolTarget {
		for _, s := range svcs {
			if s.name == name {
				h, p := "127.0.0.1", float64(s.hostPort)
				return model.DevToolTarget{Kind: "host", Host: &h, Port: &p}
			}
		}
		return model.DevToolTarget{Kind: "host"}
	}
	ex := func(s string) model.DevToolCredential { return model.DevToolCredential{Kind: "expr", Expr: &s} }
	pgP := model.NewDevToolPreset("postgres", "main-db")
	pgP.Name, pgP.Target, pgP.Auth.Password = "Main DB", hp("postgres"), ex("{{ secret.pg-pass }}")
	pgRO := pgP
	pgRO.PresetSlug, pgRO.Name, pgRO.ReadOnly = "main-db-ro", "Main DB (read-only)", true
	rd := model.NewDevToolPreset("redis", "cache")
	rd.Name, rd.Target, rd.Auth.Password = "Cache", hp("redis"), ex("{{ secret.redis-pass }}")
	mq := model.NewDevToolPreset("mqtt", "broker")
	mq.Name, mq.Target = "Broker", hp("mqtt")
	web := model.NewDevToolPreset("http", "httpbin")
	web.Name, web.Target = "httpbin", hp("httpbin")
	presets := []model.DevToolPreset{pgP, pgRO, rd, mq, web}
	cfg.Sites[1].Config.DevTools = &presets
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	rev, _ := fc.Publish(cfg, cat, "dev tools presets (opm-devmesh -devtools)")
	fmt.Fprintf(o.Out, "dev tools: published revision %s (seq %d) with %d presets on site db — approve it in the agent GUI (Updates)\n", rev.RevID, rev.Seq, len(presets))
	for _, s := range svcs {
		fmt.Fprintf(o.Out, "  %-8s 127.0.0.1:%d\n", s.name, s.hostPort)
	}
	return rev.RevID
}
