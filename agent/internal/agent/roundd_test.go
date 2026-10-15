package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

func names(v []volRef) string {
	var out []string
	for _, x := range v {
		out = append(out, x.Name+"["+strings.Join(x.Containers, "+")+"]")
	}
	return strings.Join(out, ",")
}

// Round D backup rules: manual / scheduled = the declared backup = true volumes whose directory exists; pre-update =
// backup = true head volumes of changed containers + those the new config mounts in created / recreated containers
// (+ rollback-restored ones). Host paths, files, secrets and backup = false volumes are never selected.
func TestRoundDVolumeSelection(t *testing.T) {
	dir := t.TempDir()
	vm := func(c, slug string) model.ContainerMount {
		return model.ContainerMount{MountPath: ptr("/" + slug), Source: model.VolumeSource{Kind: "volume", VolumeSlug: slug}}
	}
	sc := model.SiteConfig{Volumes: &[]model.SiteVolume{
		{VolumeSlug: "shared", Label: "Shared", Backup: true},
		{VolumeSlug: "scratch", Label: "Scratch", Backup: false},
		{VolumeSlug: "unused", Label: "Unused", Backup: true},
		{VolumeSlug: "nodir", Label: "No dir", Backup: true},
		{VolumeSlug: "solo", Label: "Solo", Backup: true},
	}, Containers: []model.Container{
		{ContainerSlug: "web", Volumes: []model.ContainerMount{vm("web", "shared"), vm("web", "scratch"), {MountPath: ptr("/h"), Source: model.VolumeSource{Kind: "host", Path: "/srv"}}}},
		{ContainerSlug: "api", Volumes: []model.ContainerMount{vm("api", "shared"), vm("api", "nodir"), {MountPath: ptr("/f"), Source: model.VolumeSource{Kind: "file", FileSlug: "f"}, ReadOnly: true}}},
		{ContainerSlug: "worker", Volumes: []model.ContainerMount{vm("worker", "solo")}},
	}}
	for _, v := range []string{"shared", "scratch", "unused", "solo"} {
		if err := os.MkdirAll(driver.VolumeDir(dir, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	all := siteVolumes(dir, &sc)
	if got := names(all); got != "shared[api+web],scratch[web],unused[],nodir[api],solo[worker]" {
		t.Fatalf("siteVolumes %s", got)
	}
	if all[0].Dir != filepath.Join(dir, "volumes", "shared") {
		t.Fatal(all[0].Dir)
	}
	if got := names(backupVolumes(dir, &sc)); got != "shared[api+web],unused[],solo[worker]" {
		t.Fatalf("manual / scheduled %s", got)
	}
	none := map[string]bool{}
	if got := names(preUpdateVolumes(all, map[string]bool{"web": true}, none, none)); got != "shared[api+web]" {
		t.Fatalf("pre-update web changed: %s", got)
	}
	if got := names(preUpdateVolumes(all, none, map[string]bool{driver.VolumeDir(dir, "solo"): true}, none)); got != "solo[worker]" {
		t.Fatalf("pre-update new mount: %s", got)
	}
	if got := names(preUpdateVolumes(all, none, map[string]bool{driver.VolumeDir(dir, "scratch"): true}, map[string]bool{"unused": true})); got != "unused[]" {
		t.Fatalf("pre-update scratch never, restored unused: %s", got)
	}
	if got := names(preUpdateVolumes(all, map[string]bool{"api": true}, none, none)); got != "shared[api+web]" {
		t.Fatalf("pre-update api (nodir has no directory): %s", got)
	}
	// a legacy (pre-round-D) site config: data sources count as declared volumes (backup on)
	legacy := model.SiteConfig{Containers: []model.Container{{ContainerSlug: "kv", Volumes: []model.ContainerMount{{ImageVolume: ptr("data"), Source: model.VolumeSource{Kind: "data", Name: "kv-data"}, ReadOnly: true}}}}}
	if got := names(siteVolumes(dir, &legacy)); got != "kv-data[kv]" {
		t.Fatalf("legacy %s", got)
	}
}

// Round D part 2: file blobs move to blobs/files (DB paths updated), vol/<slug> moves to the volume's location, the
// location changes carry the data, old per-container copies go away, agentHome precedence.
func TestRoundDPart2Layout(t *testing.T) {
	a := newTestAgent(t)
	h := strings.Repeat("ab", 32)
	old := filepath.Join(a.dataDir, "files", h)
	if err := os.WriteFile(old, []byte("blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = a.db.Exec(`INSERT INTO blobs(sha256, sizeBytes, kind, source, haveSince, path, lastUsedAt) VALUES(?,?,?,?,?,?,?)`, h, 4, "file", "cloud", 1, old, 1)
	_ = os.WriteFile(filepath.Join(a.dataDir, "files", "banner"), []byte("copy"), 0o444)
	a.moveFileBlobs()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("blob still in files/")
	}
	if p, ok := a.blobPath(h); !ok || p != driver.FileBlobPath(a.dataDir, h) {
		t.Fatalf("blob path %q %v", p, ok)
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "files", "banner")); err != nil {
		t.Fatal("a per-site copy must stay")
	}
	// vol/<slug> → auto location; a custom "./" location; a location change moves the data from the head location
	_ = os.MkdirAll(driver.LegacyVolumeDir(a.dataDir, "kv"), 0o755)
	_ = os.WriteFile(filepath.Join(driver.LegacyVolumeDir(a.dataDir, "kv"), "n"), []byte("1"), 0o644)
	_ = os.MkdirAll(driver.LegacyVolumeDir(a.dataDir, "pg"), 0o755)
	sc := model.SiteConfig{Volumes: &[]model.SiteVolume{{VolumeSlug: "kv", Backup: true}, {VolumeSlug: "pg", Backup: true, Location: &model.StorageLocation{Kind: "path", Path: "./db/pg"}}}}
	if err := a.moveLegacyVolumes(&sc); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.dataDir, "volumes", "kv", "n")); string(b) != "1" {
		t.Fatal("vol/kv not moved to volumes/kv")
	}
	if !dirExists(filepath.Join(a.dataDir, "db", "pg")) || dirExists(driver.LegacyVolumeDir(a.dataDir, "pg")) {
		t.Fatal("vol/pg not moved to ./db/pg")
	}
	if err := a.moveLegacyVolumes(&sc); err != nil { // idempotent
		t.Fatal(err)
	}
	// old per-container copies
	_ = os.MkdirAll(filepath.Join(a.dataDir, "rendered", "web"), 0o700)
	_ = os.MkdirAll(filepath.Join(a.dataDir, "secrets", "web"), 0o700)
	_ = os.WriteFile(filepath.Join(a.dataDir, "secrets", "tok"), []byte("x"), 0o400)
	a.removeOldPerContainerCopies()
	if dirExists(filepath.Join(a.dataDir, "rendered")) || dirExists(filepath.Join(a.dataDir, "secrets", "web")) {
		t.Fatal("old copies not removed")
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "secrets", "tok")); err != nil {
		t.Fatal("a per-site secret copy must stay")
	}
	t.Setenv("OPM_DATA_DIR", "/env/home")
	if ResolveDataDir("/flag", "/cfg") != "/flag" || ResolveDataDir("", "/cfg") != "/env/home" {
		t.Error("flag > env")
	}
	t.Setenv("OPM_DATA_DIR", "")
	if ResolveDataDir("", "/cfg") != "/cfg" || ResolveDataDir("", "") != "/var/lib/opm" {
		t.Error("config > default")
	}
	// siteVolumes response: agentHome + path + location (no head: orphans only, here volumes/kv and ./db/pg is not under volumes/)
	r := a.siteVolumesResponse()
	if r.AgentHome != a.dataDir || len(r.Volumes) != 1 || r.Volumes[0].VolumeSlug != "kv" || r.Volumes[0].Declared || r.Volumes[0].Path != filepath.Join(a.dataDir, "volumes", "kv") || r.Volumes[0].Location.Kind != "auto" {
		t.Fatalf("siteVolumes %+v", r)
	}
}
