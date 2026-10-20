package main

import (
	"context"
	"fmt"
	"time"

	"opm/agent/internal/fakecloud"
	"opm/agent/internal/model"
	"opm/agent/internal/validate"
)

// -volume-content (§21 #101 / #117): publishes sites app + db (no containers) where app declares the folder volume
// "flows" persisted to the cloud with its committed content (a tree: flows.json, settings.js, lib/util.js, an empty
// folder). Approve it in the GUI; once app runs it, the "cloud console" releases an edit of the content as a second
// revision (lib/util.js changed, cloud-note.md added) — approving it writes the files into the live volume (§21 #117:
// fetch on merge, no Send / Apply).
func volumeContentConfig(fc *fakecloud.Cloud, o meshOptions, edited bool) model.Config {
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
	ent := func(path, body string) model.VolumeTreeEntry {
		h := fc.PutBlob([]byte(body))
		return model.VolumeTreeEntry{Path: path, Sha256: "sha256:" + h, Size: int64(len(body)), Mode: "0644", Render: "none"}
	}
	tree := []model.VolumeTreeEntry{
		ent("flows.json", "[{\"id\":\"starter\",\"type\":\"tab\",\"label\":\"Flow 1\"}]\n"),
		ent("settings.js", "module.exports = {\n  uiPort: 1880,\n}\n"),
		ent("lib/util.js", "module.exports = (x) => x * 2;\n"),
	}
	if edited {
		tree[2] = ent("lib/util.js", "module.exports = (x) => x * 3; // edited in the cloud\n")
		tree = append(tree, ent("cloud-note.md", "# Added in the cloud console\n"))
	}
	dir := "dir"
	vols := []model.SiteVolume{{VolumeSlug: "flows", Label: "Node-RED flows", Backup: true, Location: validate.AutoLocation(), Kind: &dir,
		Seed:    model.Some(model.VolumeSeed{Kind: "tree", Tree: tree, Dirs: []string{"context"}}),
		Persist: &model.VolumePersist{Exclude: append([]string{}, model.VolumePersistDefaultExclude...), MaxBytes: model.VolumePersistDefaultBytes, Auto: true}}}
	cfg.Sites[0].Config.Volumes = &vols
	return cfg
}

func emptyCatalog() model.RevisionCatalog {
	return model.RevisionCatalog{Services: map[string]model.CatalogService{}, Images: map[string]model.ImageSpec{}, NotFoundPages: map[string]model.CatalogNotFoundPage{}}
}

func publishVolumeContentRevision(fc *fakecloud.Cloud, o meshOptions) string {
	rev, _ := fc.Publish(volumeContentConfig(fc, o, false), emptyCatalog(), "volume content + persist to cloud (opm-devmesh -volume-content)")
	fmt.Fprintf(o.Out, "volume content: published revision %s (seq %d): site app, volume flows (committed content, persist to cloud) — approve it in the agent GUI (Updates)\n", rev.RevID, rev.Seq)
	return rev.RevID
}

// demoCloudEdit waits until app runs the first revision, then releases an edit of the content (once).
func demoCloudEdit(ctx context.Context, fc *fakecloud.Cloud, o meshOptions, first string) {
	for ctx.Err() == nil {
		if ad, ok := fc.Agent("app"); ok && ad.HeadRevID != nil && *ad.HeadRevID == first {
			rev, _ := fc.Publish(volumeContentConfig(fc, o, true), emptyCatalog(), "cloud edit of app/flows (lib/util.js, cloud-note.md)")
			fmt.Fprintf(o.Out, "volume content: the cloud released revision %d with an edit of app/flows — approve it to write the files\n", rev.Seq)
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
