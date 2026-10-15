package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// BranchImage is the offline image the -branches containers run (pulled once when missing).
const BranchImage = "busybox:1.36"

// -branches (§21 #104): seeds the deployment branch with 3 commits (no proposals) where every site runs containers
// (busybox, loaded from the local Docker into the fake cloud, so no registry is needed):
//
//	1 initial: app {web, legacy}, db {cache}, variable theme, volume data       ← merged on both sites at start
//	2 theme yellow + motd + web GEN=2 + db volume archive
//	3 worker on app, legacy removed (a removal), cache GEN=3
//
// Each Branch page then shows the cloud head 2 commits ahead of the local branch. A container started by hand on a
// site network (docker run -d --name stray --network opm_<dep>_app busybox:1.36 sleep 3600) shows as uncommitted
// drift; after committing it, merging the cloud head offers "remove locally" for it.
func seedBranchCommits(fc *fakecloud.Cloud, o meshOptions) error {
	spec, err := loadImageIntoCloud(fc, BranchImage)
	if err != nil {
		return err
	}
	spec.ServiceSlug, spec.Version = "bb", "1"
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{"bb": {ServiceSlug: "bb", Label: "busybox"}}, Images: map[string]model.ImageSpec{"bb:1": spec}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	ctr := func(slug, gen string) model.Container {
		return model.Container{ContainerSlug: slug, ServiceSlug: "bb", Version: "1", Enabled: true, Env: []model.ContainerEnv{{Name: "GEN", Value: &gen}},
			Ports: []model.ContainerPort{}, Volumes: []model.ContainerMount{}, Command: []string{"sh", "-c", "sleep 2147483647"}, Restart: "always", Depends: []model.ContainerRef{}}
	}
	mk := func(step int) model.Config {
		cfg := validate.EmptyConfig()
		host := "127.0.0.1"
		for i, s := range []struct {
			slug        string
			port, alias int
		}{{"app", o.AppPort, o.AppAlias}, {"db", o.DBPort, o.DBAlias}} {
			sc := validate.DefaultSiteConfig(&host)
			alias := s.alias
			sc.AgentPort, sc.AgentGuiAliasPort = s.port, &alias
			sc.Edge.HTTPPort, sc.Edge.HTTPSPort = o.EdgePort+i, nil
			dir := "dir"
			vols := []model.SiteVolume{{VolumeSlug: "data", Label: "Data", Backup: true, Location: validate.AutoLocation(), Kind: &dir, Seed: model.Null[model.VolumeSeed]()}}
			if step >= 2 && s.slug == "db" {
				vols = append(vols, model.SiteVolume{VolumeSlug: "archive", Label: "Archive", Backup: false, Location: validate.AutoLocation(), Kind: &dir, Seed: model.Null[model.VolumeSeed]()})
			}
			sc.Volumes = &vols
			if s.slug == "app" {
				gen := "1"
				if step >= 2 {
					gen = "2"
				}
				sc.Containers = []model.Container{ctr("web", gen)}
				if step < 3 {
					sc.Containers = append(sc.Containers, ctr("legacy", "1"))
				} else {
					sc.Containers = append(sc.Containers, ctr("worker", "3"))
				}
			} else {
				gen := "1"
				if step >= 3 {
					gen = "3"
				}
				sc.Containers = []model.Container{ctr("cache", gen)}
			}
			cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s.slug, Label: s.slug, Config: sc})
		}
		theme := "green"
		if step >= 2 {
			theme = "yellow"
		}
		cfg.Variables = []model.Variable{{VariableSlug: "theme", Label: "Theme", Type: "enum", Value: model.VariableValue{V: theme}, Required: true, Options: []string{"green", "yellow", "blue"}}}
		if step >= 2 {
			cfg.Variables = append(cfg.Variables, model.Variable{VariableSlug: "motd", Label: "Message of the day", Type: "string", Value: model.VariableValue{V: "hello from the cloud"}})
		}
		return cfg
	}
	c1 := fc.Commit(mk(1), cat, "initial release (opm-devmesh -branches)")
	// both sites merge commit 1 through their own agent API (a person on each site)
	for _, s := range []struct {
		slug  string
		alias int
	}{{"app", o.AppAlias}, {"db", o.DBAlias}} {
		// the two in-process agents share Caddy's local CA on first start: a staging dry run can race its
		// provisioning ("loading root / intermediate key") — retry those for up to 60 s
		var err error
		for try := 0; try < 30; try++ {
			if err = mergeOverAPI(o, s.alias, c1.CommitID); err == nil || !strings.Contains(err.Error(), "provisioning CA") {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			return fmt.Errorf("merge commit 1 on %s: %w", s.slug, err)
		}
	}
	c2 := fc.Commit(mk(2), cat, "theme yellow + motd + web GEN=2 + db archive volume")
	c3 := fc.Commit(mk(3), cat, "worker on app, legacy removed, cache GEN=3")
	fmt.Fprintf(o.Out, "branches: commit 1 (%s) merged on app + db; cloud head = commit 3 (%s), 2 commits ahead (commit 2 %s)\n", c1.CommitID, c3.CommitID, c2.CommitID)
	fmt.Fprintf(o.Out, "branches: drift demo: docker run -d --name stray --network opm_%s_app %s sleep 3600\n", o.Deployment, BranchImage)
	return nil
}

// mergeOverAPI logs in to a site GUI API, declares an actor and merges a cloud commit with every change (waits).
func mergeOverAPI(o meshOptions, alias int, commitID string) error {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 10 * time.Minute}
	base := fmt.Sprintf("http://127.0.0.1:%d", alias)
	actor := ""
	call := func(method, path string, body, out any) (int, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		if actor != "" && method != "GET" {
			req.Header.Set("Authorization", "Actor "+actor)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if out != nil && resp.StatusCode < 300 {
			_ = json.Unmarshal(b, out)
		}
		if resp.StatusCode >= 300 {
			return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return resp.StatusCode, nil
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := call("POST", "/_opm/api/login", map[string]string{"accessKey": o.Key}, nil); err == nil {
			break
		} else if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
	var ar model.ActorResponse
	if _, err := call("POST", "/_opm/api/actor", map[string]string{"email": "devmesh@opm.test"}, &ar); err != nil {
		return err
	}
	actor = ar.ActorToken
	var plan model.MergePlan
	for {
		_, err := call("POST", "/_opm/api/branch/compare", map[string]any{"source": map[string]string{"cloudCommitId": commitID}}, &plan)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
	var ch []model.MergeChoice
	for _, it := range plan.Items {
		ch = append(ch, model.MergeChoice{Key: it.Key, Take: "source"})
	}
	var head *string
	if plan.Target.Head != nil {
		head = &plan.Target.Head.CommitID
	}
	var run model.UpdateRun
	if _, err := call("POST", "/_opm/api/branch/merge", model.LocalMergeRequest{SourceCommitID: commitID, TargetHead: head, Choices: ch, Message: "take the initial release (devmesh)"}, &run); err != nil {
		return err
	}
	for {
		var r model.UpdateRun
		if _, err := call("GET", "/_opm/api/updates/"+run.UpdateID, nil, &r); err == nil && r.FinishedAt != nil {
			if r.Result == nil || !r.Result.Ok {
				return fmt.Errorf("merge failed: %+v", r.Result)
			}
			return nil
		}
		time.Sleep(time.Second)
	}
}

// loadImageIntoCloud exports a local image (`docker save`, pulled once when missing) into the fake cloud blobs and
// returns its ImageSpec (digests + layers).
func loadImageIntoCloud(fc *fakecloud.Cloud, ref string) (model.ImageSpec, error) {
	if exec.Command("docker", "image", "inspect", ref).Run() != nil {
		if out, err := exec.Command("docker", "pull", ref).CombinedOutput(); err != nil {
			return model.ImageSpec{}, fmt.Errorf("docker pull %s: %v %s", ref, err, out)
		}
	}
	dir, _ := os.MkdirTemp("", "opm-devmesh-img")
	defer os.RemoveAll(dir)
	tarPath := filepath.Join(dir, "img.tar")
	if out, err := exec.Command("docker", "save", ref, "-o", tarPath).CombinedOutput(); err != nil {
		return model.ImageSpec{}, fmt.Errorf("docker save %s: %v %s", ref, err, out)
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return model.ImageSpec{}, err
	}
	defer f.Close()
	files := map[string][]byte{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = b
	}
	var man []struct {
		Config string
		Layers []string
	}
	if err := json.Unmarshal(files["manifest.json"], &man); err != nil || len(man) == 0 {
		return model.ImageSpec{}, fmt.Errorf("docker save %s: no manifest.json", ref)
	}
	cfgHex := strings.TrimPrefix(man[0].Config, "blobs/sha256/")
	spec := model.ImageSpec{ConfigDigest: "sha256:" + cfgHex, Platform: "linux/" + runtime.GOARCH, Kind: "service", ManifestMediaType: "application/vnd.oci.image.manifest.v1+json",
		Ports: []model.ImagePort{}, Env: []model.ImageEnv{}, Volumes: []model.ImageVolume{}, Command: []string{"sh"}, StopTimeoutSec: 1}
	manHex := ""
	for name, b := range files {
		if !strings.HasPrefix(name, "blobs/sha256/") {
			continue
		}
		var m struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest    string `json:"digest"`
				Size      int64  `json:"size"`
				MediaType string `json:"mediaType"`
			} `json:"layers"`
		}
		if json.Unmarshal(b, &m) == nil && m.Config.Digest == spec.ConfigDigest {
			manHex = strings.TrimPrefix(name, "blobs/sha256/")
			for _, l := range m.Layers {
				spec.Layers = append(spec.Layers, model.ImageLayer{Digest: l.Digest, SizeBytes: l.Size, MediaType: l.MediaType})
			}
		}
	}
	if manHex == "" {
		for _, l := range man[0].Layers {
			h := strings.TrimPrefix(l, "blobs/sha256/")
			spec.Layers = append(spec.Layers, model.ImageLayer{Digest: "sha256:" + h, SizeBytes: int64(len(files[l])), MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"})
		}
		mb, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": spec.ConfigDigest}})
		manHex = fc.PutBlob(mb)
	} else {
		fc.PutBlob(files["blobs/sha256/"+manHex])
	}
	spec.Digest = "sha256:" + manHex
	fc.PutBlob(files["blobs/sha256/"+cfgHex])
	for _, l := range spec.Layers {
		fc.PutBlob(files["blobs/sha256/"+strings.TrimPrefix(l.Digest, "sha256:")])
		spec.SizeBytes += l.SizeBytes
	}
	return spec, nil
}

// removeDeploymentContainers removes every container of the devmesh deployment (labelled opm.deployment) and the
// hand-made demo container "stray".
func removeDeploymentContainers(dep string) {
	out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=opm.deployment="+dep).Output()
	ids := strings.Fields(string(out))
	if len(ids) > 0 {
		_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
	}
	_ = exec.Command("docker", "rm", "-f", "stray").Run()
	for _, s := range []string{"app", "db"} {
		_ = exec.Command("docker", "network", "rm", "opm_"+dep+"_"+s).Run()
	}
}
