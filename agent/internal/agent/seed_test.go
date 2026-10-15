package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opm/agent/internal/canonical"
	"opm/agent/internal/driver"
	"opm/agent/internal/model"
)

type tarEntry struct {
	name, body, link string
	typ              byte
}

func makeTarGz(t *testing.T, p string, entries []tarEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ == tar.TypeDir {
			h.Mode, h.Size = 0o755, 0
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExtractSeedArchiveSafety(t *testing.T) {
	d := t.TempDir()
	ok := filepath.Join(d, "ok.tar.gz")
	makeTarGz(t, ok, []tarEntry{{name: "./", typ: tar.TypeDir}, {name: "lib/", typ: tar.TypeDir}, {name: "lib/a.js", body: "A", typ: tar.TypeReg}, {name: "flows.json", body: "[]", typ: tar.TypeReg}})
	out := filepath.Join(d, "out")
	n, err := extractSeedArchive(ok, out, 1<<20)
	if err != nil || n != 2 {
		t.Fatalf("ok archive: %d %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "lib", "a.js")); string(b) != "A" {
		t.Fatal("content")
	}
	for name, ents := range map[string][]tarEntry{
		"dotdot":   {{name: "../evil", body: "x", typ: tar.TypeReg}},
		"inner":    {{name: "a/../../evil", body: "x", typ: tar.TypeReg}},
		"absolute": {{name: "/etc/evil", body: "x", typ: tar.TypeReg}},
		"symlink":  {{name: "l", link: "/etc/passwd", typ: tar.TypeSymlink}},
		"hardlink": {{name: "h", link: "flows.json", typ: tar.TypeLink}},
		"device":   {{name: "dev", typ: tar.TypeChar}},
		"big":      {{name: "big", body: strings.Repeat("x", 2048), typ: tar.TypeReg}},
	} {
		p := filepath.Join(d, name+".tar.gz")
		makeTarGz(t, p, ents)
		if _, err := extractSeedArchive(p, filepath.Join(d, "x-"+name), 1024); !errors.Is(err, ErrUnsafeArchive) {
			t.Errorf("%s: want ErrUnsafeArchive, got %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "evil")); err == nil {
		t.Fatal("escaped the volume")
	}
}

// Seeding rules: only new / empty volumes, never twice; a changed seed never touches data; reset re-applies;
// a single-file volume; kind change with data fails.
func TestSeedRules(t *testing.T) {
	a := newTestAgent(t)
	put := func(content string) (model.Sha256, int64) {
		h := canonical.Sha256Hex([]byte(content))
		p := driver.FileBlobPath(a.dataDir, h)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
		return model.Sha256("sha256:" + h), int64(len(content))
	}
	fileDef := func(slug, content, render string) model.FileDef {
		s, n := put(content)
		return model.FileDef{FileSlug: slug, Label: slug, Mime: "text/plain", Editor: "text", MaxBytes: 1000, Content: &model.BlobRef{Sha256: s, SizeBytes: n}, Render: model.Some(render)}
	}
	arch := filepath.Join(t.TempDir(), "s.tar.gz")
	makeTarGz(t, arch, []tarEntry{{name: "lib/x.txt", body: "from archive", typ: tar.TypeReg}})
	ab, _ := os.ReadFile(arch)
	as, an := put(string(ab))
	dir, file := "dir", "file"
	cfg := &model.Config{Variables: []model.Variable{}, Secrets: []model.SecretDef{}, Files: []model.FileDef{
		fileDef("flows", `[{"id":"{{ self.slug }}"}]`, "expressions"), fileDef("settings", "plain {{ x }}", "none"), fileDef("one", "single", "none")},
		Sites: []model.Site{{SiteSlug: a.site, Config: model.SiteConfig{Volumes: &[]model.SiteVolume{
			{VolumeSlug: "red", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "flows.json", FileSlug: "flows"}, {Path: "lib/settings.js", FileSlug: "settings"}}})},
			{VolumeSlug: "arc", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "archive", BlobSha256: as, Name: "s.tar.gz", Size: an})},
			{VolumeSlug: "one", Backup: true, Kind: &file, Seed: model.Some(model.VolumeSeed{Kind: "file", FileSlug: "one"})},
			{VolumeSlug: "full", Backup: true, Kind: &dir, Seed: model.Some(model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "a", FileSlug: "one"}}})},
		}}}}}
	cat := emptyCatalog()
	site := &cfg.Sites[0]
	full := driver.VolumePath(a.dataDir, &site.Config, "full")
	_ = os.MkdirAll(full, 0o755)
	_ = os.WriteFile(filepath.Join(full, "keep"), []byte("user data"), 0o644)
	if err := a.seedVolumes(cfg, cat, site, nil); err != nil {
		t.Fatal(err)
	}
	red := driver.VolumePath(a.dataDir, &site.Config, "red")
	if b, _ := os.ReadFile(filepath.Join(red, "flows.json")); string(b) != `[{"id":"`+a.site+`"}]` {
		t.Fatalf("rendered seed: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(red, "lib", "settings.js")); string(b) != "plain {{ x }}" {
		t.Fatalf("raw seed: %s", b)
	}
	if _, err := os.Stat(filepath.Join(red, SeedMarker)); err != nil {
		t.Fatal("marker")
	}
	if b, _ := os.ReadFile(filepath.Join(driver.VolumePath(a.dataDir, &site.Config, "arc"), "lib", "x.txt")); string(b) != "from archive" {
		t.Fatal("archive seed")
	}
	if b, _ := os.ReadFile(driver.VolumePath(a.dataDir, &site.Config, "one")); string(b) != "single" {
		t.Fatal("file seed")
	}
	if _, err := os.Stat(filepath.Join(full, "a")); err == nil || a.seededInfo("full") != nil {
		t.Fatal("a volume with data must not be seeded")
	}
	if s := a.seededInfo("red"); s == nil || s.Source != "files" || s.Entries != 2 {
		t.Fatalf("seeded %+v", s)
	}
	// the container changes the file; a new seed (another revision) never overwrites it
	_ = os.WriteFile(filepath.Join(red, "flows.json"), []byte("edited"), 0o644)
	(*site.Config.Volumes)[0].Seed = model.Some(model.VolumeSeed{Kind: "files", Entries: []model.SeedEntry{{Path: "flows.json", FileSlug: "one"}}})
	if err := a.seedVolumes(cfg, cat, site, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(red, "flows.json")); string(b) != "edited" {
		t.Fatal("re-seeded existing data")
	}
	// even emptied, a seeded volume is never seeded again
	_ = os.RemoveAll(red)
	_ = a.seedVolumes(cfg, cat, site, nil)
	if _, err := os.Stat(red); err == nil {
		t.Fatal("seeded twice")
	}
	// kind change with data → staging error; without data → fine
	next := *cfg
	nsites := []model.Site{*site}
	vols := append([]model.SiteVolume{}, (*site.Config.Volumes)...)
	vols[1].Kind = &file
	nsites[0].Config.Volumes = &vols
	next.Sites = nsites
	head := cfg
	if err := a.checkVolumeKinds(head, &next); err == nil || !strings.Contains(err.Error(), "use a new volume name") {
		t.Fatalf("kind change: %v", err)
	}
	// a single-file volume is archived + restored as one file
	one := driver.VolumePath(a.dataDir, &site.Config, "one")
	_ = os.WriteFile(one, []byte("written by the container"), 0o644)
	dst := filepath.Join(t.TempDir(), "one.tar.zst")
	if _, _, err := archiveDir(one, dst); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(one, []byte("x"), 0o644)
	if err := extractArchive(dst, one); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(one); string(b) != "written by the container" {
		t.Fatalf("file volume restore: %s", b)
	}
}
