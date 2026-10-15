package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

func siteCfg(c *model.Config, slug string) *model.SiteConfig {
	for i := range c.Sites {
		if c.Sites[i].SiteSlug == slug {
			return &c.Sites[i].Config
		}
	}
	return nil
}

// §21 #113 a site is standalone: an edit that adds / removes containers, variables, secrets and files and redefines
// them is a valid commit of the agent branch (no V_LOCAL_* refusal); the result carries exactly the edited items.
func TestEditRevisionStandalone(t *testing.T) {
	a, r1 := branchUnitAgent(t)
	ed := clone(t, r1.Config)
	app := siteCfg(&ed, "app")
	// add a container (a second hello-api), remove hello-web
	extra := clone(t, app.Containers[0])
	extra.ContainerSlug = "hello-api-b"
	for i := range extra.Ports {
		extra.Ports[i].HostPort = model.NumOrExpr{} // not published
	}
	var keep []model.Container
	for _, c := range app.Containers {
		if c.ContainerSlug != "hello-web" {
			keep = append(keep, c)
		}
	}
	app.Containers = append(keep, extra)
	// edge routes / depends that pointed at hello-web would be validation errors: drop them for the test
	app.Edge.Routes = nil
	for i := range app.Containers {
		app.Containers[i].Depends = []model.ContainerRef{}
	}
	// a new variable, a redefined one, a new secret, a new file
	ed.Variables = append(ed.Variables, model.Variable{VariableSlug: "shift", Label: "Shift", Type: "string", Value: model.VariableValue{V: "night"}})
	ed.Variables[0].Label = "Theme (site label)"
	ed.Files = append(ed.Files, model.FileDef{FileSlug: "notes", Label: "Notes", Mime: "text/plain", Editor: "text", MaxBytes: 1024, Render: model.Some("none")})
	in := inputOf(ed)
	in.Secrets = append(in.Secrets, model.SecretDefInput{SecretSlug: "plant-key", Label: "Plant key", PlainValue: model.Some("k-1")})
	rev, _, he := a.editRevision(a.headRevision(), in, "ops@example.com", "standalone edit")
	if he != nil {
		t.Fatalf("refused: %s %v", he.msg, he.details)
	}
	got := siteCfg(&rev.Config, "app")
	names := map[string]bool{}
	for _, c := range got.Containers {
		names[c.ContainerSlug] = true
	}
	if !names["hello-api-b"] || names["hello-web"] {
		t.Fatalf("containers %v", names)
	}
	if len(rev.Config.Variables) != len(r1.Config.Variables)+1 || rev.Config.Variables[0].Label != "Theme (site label)" {
		t.Fatalf("variables %+v", rev.Config.Variables)
	}
	var sk *model.SecretDef
	for i := range rev.Config.Secrets {
		if rev.Config.Secrets[i].SecretSlug == "plant-key" {
			sk = &rev.Config.Secrets[i]
		}
	}
	if sk == nil || sk.Value == nil || *sk.Value != "k-1" {
		t.Fatalf("secret %+v", sk)
	}
	if len(rev.Config.Files) != len(r1.Config.Files)+1 {
		t.Fatalf("files %d", len(rev.Config.Files))
	}
	// the site list stays the deployment's
	bad := clone(t, r1.Config)
	bad.Sites = bad.Sites[:1]
	if _, _, he := a.editRevision(a.headRevision(), inputOf(bad), "ops@example.com", "x"); he == nil {
		t.Fatal("removing a site must be refused")
	}
}

// §21 #113 a site image (resolved docker reference, kv site-images) is a valid image of an edit; the commit carries it
// in its catalog (digest "", reference kept) so another site / the cloud can use it.
func TestEditRevisionSiteImage(t *testing.T) {
	a, r1 := branchUnitAgent(t)
	ref := "nginx:1.27@sha256:" + "ab" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	li := &liveImage{ID: "sha256:" + "c0ffee0123456789abcdef0123456789abcdef0123456789abcdef0123456789", RepoTags: []string{"nginx:1.27"}}
	li.Config.ExposedPorts = map[string]struct{}{"80/tcp": {}}
	sp, label := localImageSpec(li, li.ID, "nginx:1.27")
	sp.Reference = &ref
	key := sp.ServiceSlug + ":" + sp.Version
	if key != "nginx:local-c0ffee012345" || label != "nginx" {
		t.Fatalf("spec key %s label %s", key, label)
	}
	_ = a.db.SetKV(siteImagesKVKey, map[string]siteImageEntry{key: {Image: sp, Service: model.CatalogService{ServiceSlug: "nginx", Label: label}}})
	if _, ok := a.knownCatalog(a.headRevision()).Images[key]; !ok {
		t.Fatal("knownCatalog without the site image")
	}
	ed := clone(t, r1.Config)
	app := siteCfg(&ed, "app")
	app.Containers = append(app.Containers, model.Container{ContainerSlug: "web2", ServiceSlug: "nginx", Version: sp.Version, Enabled: true, Env: []model.ContainerEnv{},
		Ports: []model.ContainerPort{{PortName: "p80", HostIP: "0.0.0.0", Edge: true}}, Volumes: []model.ContainerMount{}, Restart: "always", Depends: []model.ContainerRef{}})
	rev, _, he := a.editRevision(a.headRevision(), inputOf(ed), "ops@example.com", "nginx from docker hub")
	if he != nil {
		t.Fatalf("refused: %s %v", he.msg, he.details)
	}
	im, ok := rev.Catalog.Images[key]
	if !ok || im.Digest != "" || im.Reference == nil || *im.Reference != ref || !im.IsSiteImage() {
		t.Fatalf("catalog image %+v", im)
	}
	if _, ok := rev.Catalog.Services["nginx"]; !ok {
		t.Fatal("catalog service missing")
	}
}

func TestSiteImageReferences(t *testing.T) {
	for ref, ok := range map[string]bool{
		"nginx": true, "nginx:1.27": true, "registry.example.com:5000/team/app:2": true,
		"redis@sha256:" + strings.Repeat("a", 64): true, "redis:7@sha256:" + strings.Repeat("b", 64): true,
		"": false, "has space:1": false, "/abs:1": false, "trail/": false, "x:": false, "y@sha256:short": false,
	} {
		if validReference(ref) != ok {
			t.Errorf("validReference(%q) != %v", ref, ok)
		}
	}
	for ref, want := range map[string][2]string{
		"nginx": {"nginx", "latest"}, "nginx:1.27": {"nginx", "1.27"}, "host:5000/a/b:3": {"host:5000/a/b", "3"},
		"redis@sha256:abc": {"redis", "sha256:abc"}, "host:5000/a": {"host:5000/a", "latest"},
	} {
		if im, tag := docker.SplitReference(ref); im != want[0] || tag != want[1] {
			t.Errorf("SplitReference(%q) = %s %s", ref, im, tag)
		}
	}
}

// §21 #113 drift policy auto-commit: stable Docker changes are committed by the agent (author kind unknown) once they
// are stable; ask (default) leaves them uncommitted.
func TestAutoCommitDrift(t *testing.T) {
	a, r1 := branchUnitAgent(t)
	id := r1.RevID
	live := clone(t, r1.Config)
	for i := range siteCfg(&live, "app").Containers {
		c := &siteCfg(&live, "app").Containers[i]
		if c.ContainerSlug == "hello-api" {
			c.Restart = "never"
		}
	}
	scan := &driftScan{DriftReport: model.DriftReport{ScannedAt: nowTs(), HeadCommitID: &id, Notes: []model.DriftNote{}, LocalImages: []string{}}, LiveConfig: live}
	scan.Items = []model.DriftItem{{MergeItem: model.MergeItem{Key: "sites/app/containers/hello-api/restart", Section: "containers", Kind: "change", Effect: "change"}, Origin: "docker", Detail: "changed by hand"}}
	a.saveDrift(scan)
	t0 := time.Now()
	// ask: nothing happens
	if c := a.maybeAutoCommitDrift(context.Background(), t0.Add(time.Hour)); c != nil {
		t.Fatal("ask must not commit")
	}
	_ = a.db.SetKV(driftPolicyKVKey, DriftPolicy{Mode: "auto-commit"})
	if c := a.maybeAutoCommitDrift(context.Background(), t0); c != nil {
		t.Fatal("first sighting must wait")
	}
	if c := a.maybeAutoCommitDrift(context.Background(), t0.Add(10*time.Second)); c != nil {
		t.Fatal("not stable yet")
	}
	c := a.maybeAutoCommitDrift(context.Background(), t0.Add(autoCommitStable+time.Second))
	if c == nil {
		t.Fatal("stable drift not auto-committed")
	}
	if c.Author.Kind != "unknown" || c.Author.Name == nil || *c.Author.Name != UnknownAuthorName || c.Kind != "drift" {
		t.Fatalf("author %+v kind %s", c.Author, c.Kind)
	}
	if h := a.head(); h == nil || h.RevID != c.CommitID {
		t.Fatal("head did not move to the auto-commit")
	}
	for _, x := range siteCfg(&c.Config, "app").Containers {
		if x.ContainerSlug == "hello-api" && x.Restart != "never" {
			t.Fatalf("the commit does not hold the Docker change: %s", x.Restart)
		}
	}
	if u := a.uncommittedNow(); u.blocking() != 0 {
		t.Fatalf("still uncommitted: %+v", u.items)
	}
}
