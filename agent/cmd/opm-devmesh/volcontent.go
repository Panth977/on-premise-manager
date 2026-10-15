package main

import (
	"context"
	"fmt"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// -volume-content (§21 #101): publishes sites app + db (no containers) where app declares the folder volume "flows"
// with its own starter content (a tree: flows.json, a rendered settings.js, lib/util.js, an empty folder) persisted to
// the cloud (auto snapshots). Approve it in the GUI; once the first snapshot of app/flows reaches the fake cloud, the
// "cloud console" saves an edit and sends it (restart off) so Docker › Volumes shows "Cloud ahead" + Apply cloud version.
func publishVolumeContentRevision(fc *fakecloud.Cloud, o meshOptions) string {
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
		cfg.Sites = append(cfg.Sites, model.Site{SiteSlug: s.slug, Label: s.slug, Config: sc})
	}
	ent := func(path, body, mode, render string) model.VolumeTreeEntry {
		h := fc.PutBlob([]byte(body))
		return model.VolumeTreeEntry{Path: path, Sha256: "sha256:" + h, Size: int64(len(body)), Mode: mode, Render: render}
	}
	dir := "dir"
	vols := []model.SiteVolume{{VolumeSlug: "flows", Label: "Node-RED flows", Backup: true, Location: validate.AutoLocation(), Kind: &dir,
		Seed: model.Some(model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{
			ent("flows.json", "[{\"id\":\"starter\",\"type\":\"tab\",\"label\":\"Flow 1\"}]\n", "0644", "none"),
			ent("settings.js", "module.exports = {\n  uiPort: 1880,\n  site: '{{ self.slug }}',\n}\n", "0644", "expressions"),
			ent("lib/util.js", "module.exports = (x) => x * 2;\n", "0644", "none"),
		}, Dirs: []string{"context"}}),
		Persist: &model.VolumePersist{Exclude: append([]string{}, model.VolumePersistDefaultExclude...), MaxBytes: model.VolumePersistDefaultBytes, Auto: true}}}
	cfg.Sites[0].Config.Volumes = &vols
	cat := model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
	rev, _ := fc.Publish(cfg, cat, "volume content + persist to cloud (opm-devmesh -volume-content)")
	fmt.Fprintf(o.Out, "volume content: published revision %s (seq %d): site app, volume flows (tree starter content, persist to cloud) — approve it in the agent GUI (Updates)\n", rev.RevID, rev.Seq)
	return rev.RevID
}

// demoCloudEdit waits for the first snapshot of app/flows, then saves a cloud edit and sends it (once).
func demoCloudEdit(ctx context.Context, fc *fakecloud.Cloud, o meshOptions) {
	for ctx.Err() == nil {
		if len(fc.VolumeVersions("app", "flows")) > 0 {
			v, err := fc.CreateVolumeEdit("app", "flows", map[string][]byte{
				"lib/util.js":   []byte("module.exports = (x) => x * 3; // edited in the cloud\n"),
				"cloud-note.md": []byte("# Added in the cloud console\n"),
			}, nil, "editor@example.com")
			if err == nil {
				_, err = fc.SendVolumeContent("app", "flows", v.VersionID, false, "demo edit from opm-devmesh")
			}
			if err != nil {
				fmt.Fprintln(o.Out, "volume content demo:", err)
			} else {
				fmt.Fprintf(o.Out, "volume content: the cloud sent version %d of app/flows — Docker › Volumes › flows › Apply cloud version\n", v.Seq)
			}
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
