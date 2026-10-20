package validate

import (
	"strings"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/model"
)

// §21 #101: tree / single seeds, persist, legacy File seeds (agent mode only), V_FILE_IN_WRITABLE_VOLUME (warning in
// agent mode), V_FILE_RENDER of 'expressions' entries (BlobContents), V_LOCAL_VOLUME_CONTENT.
func TestVolumeContentRules(t *testing.T) {
	cfg, ctx := load(t, ModeAgent)
	d := clone(t, cfg)
	sha := func(s string) string { return canonical.Sha256Tagged([]byte(s)) }
	good := `host {{ site.app.host }}`
	bad := `x {{ nope.y }}`
	app := &site(d, "app").Config
	vs := append(*app.Volumes,
		newVol("nodered-data", "dir", &model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{
			{Path: "settings.js", Sha256: sha(good), Size: int64(len(good)), Mode: "0644", Render: "expressions"},
			{Path: "lib/bad.js", Sha256: sha(bad), Size: int64(len(bad)), Mode: "0755", Render: "expressions"},
			{Path: "flows.json", Sha256: sha("[]"), Size: 2, Mode: "0644", Render: "none"},
		}, Dirs: []string{"empty"}}),
		newVol("one", "file", &model.VolumeSeed{Kind: "single", Sha256: sha("x"), Size: 1, Mode: "0600", Render: "none"}),
		newVol("legacy", "dir", &model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "a.json", FileSlug: d.Files[0].FileSlug}}}),
		// §21 #117 a persist volume's seed is committed content: literal, nothing under the excludes
		newVol("flows", "dir", &model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{
			{Path: "flows.json", Sha256: sha("[]"), Size: 2, Mode: "0644", Render: "none"},
			{Path: "tpl.js", Sha256: sha(good), Size: int64(len(good)), Mode: "0644", Render: "expressions"},
			{Path: "debug.log", Sha256: sha("l"), Size: 1, Mode: "0644", Render: "none"},
		}, Dirs: []string{}}))
	vs[len(vs)-1].Persist = &model.VolumePersist{Exclude: []string{"node_modules/", "*.log"}, MaxBytes: 50 << 20, Auto: true}
	app.Volumes = &vs
	var api *model.Container
	for i := range app.Containers {
		if app.Containers[i].ContainerSlug == "hello-api" {
			api = &app.Containers[i]
		}
	}
	api.Volumes = append(api.Volumes,
		model.ContainerMount{MountPath: ptrS("/nodered"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "nodered-data"}},
		model.ContainerMount{MountPath: ptrS("/nodered/settings.js"), ReadOnly: true, Source: model.VolumeSource{Kind: "file", FileSlug: d.Files[0].FileSlug}},
		model.ContainerMount{MountPath: ptrS("/etc/one"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "one"}},
		model.ContainerMount{MountPath: ptrS("/legacy"), Source: model.VolumeSource{Kind: "volume", VolumeSlug: "legacy"}})
	ctx.BlobContents = map[string]string{sha(good): good, sha(bad): bad}
	list := ValidateConfig(d, ctx).List
	find := func(l []model.ValidationIssue, code, end, sev string) bool {
		for _, x := range l {
			if x.Code == code && strings.HasSuffix(x.Path, end) && x.Severity == sev {
				return true
			}
		}
		return false
	}
	if !find(list, "V_FILE_IN_WRITABLE_VOLUME", ".source.fileSlug", "warning") {
		t.Errorf("agent mode: V_FILE_IN_WRITABLE_VOLUME warning missing: %+v", list)
	}
	if !find(list, "V_FILE_RENDER", "volumes[nodered-data].seed.entries[lib/bad.js]", "error") {
		t.Errorf("V_FILE_RENDER missing: %+v", list)
	}
	if !find(list, "V_VOLUME_SEED", "volumes[flows].seed.entries[tpl.js]", "error") || !find(list, "V_VOLUME_CONTENT_EXCLUDED", "volumes[flows].seed.entries[debug.log]", "error") {
		t.Errorf("§21 #117 persist content rules missing: %+v", list)
	}
	for _, x := range list {
		if strings.Contains(x.Path, "volumes[flows]") {
			continue
		}
		if x.Severity == "error" && x.Code != "V_FILE_RENDER" {
			t.Errorf("unexpected error %+v", x)
		}
		if strings.Contains(x.Path, "settings.js") && x.Code == "V_FILE_RENDER" {
			t.Errorf("good template flagged: %+v", x)
		}
	}
	// publish mode: legacy seed + file in writable volume are errors
	ctxP := ctx
	ctxP.Mode = ModePublish
	pl := ValidateConfig(d, ctxP).List
	if !find(pl, "V_VOLUME_SEED_FILE_REF", "volumes[legacy].seed", "error") || !find(pl, "V_FILE_IN_WRITABLE_VOLUME", ".source.fileSlug", "error") {
		t.Errorf("publish mode: %+v", pl)
	}
	// bad shapes
	e := clone(t, d)
	ea := &site(e, "app").Config
	for i := range *ea.Volumes {
		v := &(*ea.Volumes)[i]
		switch v.VolumeSlug {
		case "nodered-data":
			v.Seed = model.Some(model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{{Path: "../x", Sha256: "x", Mode: "644", Render: "none"}, {Path: "a", Sha256: "sha256:1", Size: 1, Mode: "0644", Render: "none"}}, Dirs: []string{"a"}})
			v.Persist = &model.VolumePersist{Exclude: []string{"!x", "a", "a"}, MaxBytes: 1, Auto: true}
		case "one":
			v.Seed = model.Some(model.VolumeSeed{Kind: "tree", Tree: []model.VolumeTreeEntry{}, Dirs: []string{}})
			v.Persist = &model.VolumePersist{Exclude: []string{}, MaxBytes: 50 << 20, Auto: true}
		}
	}
	el := ValidateConfig(e, ctx).Errors()
	for _, w := range [][2]string{
		{"V_VOLUME_CONTENT_PATH", "volumes[nodered-data].seed.entries[../x]"},
		{"V_FORMAT", "volumes[nodered-data].seed.entries[a]"},
		{"V_VOLUME_SEED", "volumes[nodered-data].seed.entries[a]"},
		{"V_VOLUME_PERSIST", "volumes[nodered-data].persist.exclude[0]"},
		{"V_DUPLICATE", "volumes[nodered-data].persist.exclude"},
		{"V_RANGE", "volumes[nodered-data].persist.maxBytes"},
		{"V_VOLUME_SEED", "volumes[one].seed"},
		{"V_VOLUME_PERSIST", "volumes[one].persist"},
	} {
		if !find(el, w[0], w[1], "error") {
			t.Errorf("want %s at …%s in %+v", w[0], w[1], el)
		}
	}
	// local edit: content / persist are cloud configuration
	f := clone(t, d)
	fa := &site(f, "app").Config
	for i := range *fa.Volumes {
		if (*fa.Volumes)[i].VolumeSlug == "flows" {
			(*fa.Volumes)[i].Persist = nil
		}
	}
	got := CheckLocalEdit(d, f)
	ok := false
	for _, x := range got {
		if x.Code == "V_LOCAL_VOLUME_CONTENT" && strings.HasSuffix(x.Path, "volumes[flows]") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("V_LOCAL_VOLUME_CONTENT missing: %+v", got)
	}
	for _, x := range CheckLocalEdit(d, clone(t, d)) {
		if x.Code == "V_LOCAL_VOLUME_CONTENT" {
			t.Errorf("unchanged config flagged: %+v", x)
		}
	}
}
