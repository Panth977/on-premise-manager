package validate

import (
	"strings"
	"testing"

	"opm/agent/internal/model"
)

func newVol(slug, kind string, seed *model.VolumeSeed) model.SiteVolume {
	v := NewSiteVolume(slug, slug, "", true)
	v.Kind = &kind
	if seed != nil {
		v.Seed = model.Some(*seed)
	}
	return v
}

// Mirrors the TS test "round E: volume kind + starter content (seed)".
func TestRoundEVolumeSeed(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	d := clone(t, cfg)
	banner := d.Files[0].FileSlug
	app := &site(d, "app").Config
	vs := append(*app.Volumes,
		newVol("nodered-data", "dir", &model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "flows.json", FileSlug: banner}, {Path: "lib/settings.js", FileSlug: banner}}}),
		newVol("one-file", "file", &model.VolumeSeed{Kind: "file", FileSlug: banner}),
		newVol("arch", "dir", &model.VolumeSeed{Kind: "archive", BlobSha256: model.Sha256("sha256:" + strings.Repeat("a", 64)), Name: "start.tar.gz", Size: 1024}))
	app.Volumes = &vs
	var api *model.Container
	for i := range app.Containers {
		if app.Containers[i].ContainerSlug == "hello-api" {
			api = &app.Containers[i]
		}
	}
	api.Volumes = append(api.Volumes,
		model.ContainerMount{MountPath: ptrS("/nodered"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "nodered-data"}},
		model.ContainerMount{MountPath: ptrS("/etc/one.json"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "one-file"}},
		model.ContainerMount{MountPath: ptrS("/seed"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "arch"}})
	for _, x := range ValidateConfig(d, ctx).List {
		if strings.HasPrefix(x.Code, "V_VOLUME") || x.Severity == "error" {
			t.Errorf("valid seeds: %+v", x)
		}
	}
	e := clone(t, d)
	ea := &site(e, "app").Config
	for i := range *ea.Volumes {
		v := &(*ea.Volumes)[i]
		switch v.VolumeSlug {
		case "nodered-data":
			v.Seed = model.Some(model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "../x", FileSlug: banner}, {Path: "a", FileSlug: "nope"}, {Path: "a/b", FileSlug: banner}}})
		case "one-file":
			v.Seed = model.Some(model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "x", FileSlug: banner}}})
		case "arch":
			v.Seed = model.Some(model.VolumeSeed{Kind: "archive", BlobSha256: "x", Name: "start.zip", Size: 300 * 1024 * 1024})
		}
	}
	for i := range ea.Containers {
		for k := range ea.Containers[i].Volumes {
			if m := ea.Containers[i].Volumes[k].MountPath; m != nil && *m == "/etc/one.json" {
				ea.Containers[i].Volumes[k].MountPath = ptrS("/etc/one/")
			}
		}
	}
	el := ValidateConfig(e, ctx).Errors()
	has := func(code, end string) {
		for _, x := range el {
			if x.Code == code && strings.HasSuffix(x.Path, end) {
				return
			}
		}
		t.Errorf("want %s at …%s in %+v", code, end, el)
	}
	has("V_VOLUME_SEED", "volumes[nodered-data].seed.entries[0].path")
	has("V_REF", "volumes[nodered-data].seed.entries[1].fileSlug")
	has("V_VOLUME_SEED", "volumes[nodered-data].seed.entries[1].path")
	has("V_VOLUME_SEED", "volumes[one-file].seed")
	has("V_FORMAT", "volumes[arch].seed.blobSha256")
	has("V_VOLUME_SEED", "volumes[arch].seed.name")
	has("V_VOLUME_SEED", "volumes[arch].seed.size")
	has("V_VOLUME_KIND", ".mountPath")
	if SeedPathError("lib/settings.js") != "" || SeedPathError("/abs") == "" || SeedPathError("a/./b") == "" || SeedPathError("a b") == "" {
		t.Error("seedPathError")
	}
	// legacy volumes normalize to dir / no seed
	n := NormalizeRoundE(&model.Config{Sites: []model.Site{{SiteSlug: "x", Config: model.SiteConfig{Volumes: &[]model.SiteVolume{{VolumeSlug: "x", Label: "x", Backup: true}}}}}})
	v := (*n.Sites[0].Config.Volumes)[0]
	if v.VolumeKind() != "dir" || v.SeedOf() != nil || !v.Seed.Set || v.Kind == nil {
		t.Errorf("normalize %+v", v)
	}
}

func ptrS(s string) *string { return &s }
