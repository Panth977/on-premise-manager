package export

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"opm/agent/internal/driver"
	"opm/agent/internal/model"
	"opm/agent/internal/volcontent"
)

// Runtime mode (opm-render init on the VM): the same passes A + B, plus one with the real values; the outputs that
// differ between A and B are written from the real pass.

// Missing lists the .env names this site needs that have no value.
func Missing(req Request, site string, values map[string]string) ([]string, error) {
	x, err := newExporter(&req)
	if err != nil {
		return nil, err
	}
	st := x.classify(site)
	var out []string
	for _, d := range st.inputs {
		if st.used[d.Name] && values[d.Name] == "" {
			out = append(out, d.Name)
		}
	}
	return out, nil
}

func (x *exporter) realPass(site string, values map[string]string) (*siteState, *pass, error) {
	if siteOf(x.cfg, site) == nil {
		return nil, nil, fmt.Errorf("site %s is not in this export", site)
	}
	st := x.classify(site)
	var missing []string
	vals := map[string]string{}
	for _, d := range st.inputs {
		vals[d.Name] = values[d.Name]
		if st.used[d.Name] && values[d.Name] == "" {
			missing = append(missing, d.Name)
		}
	}
	if len(missing) > 0 {
		return nil, nil, &MissingError{Names: missing}
	}
	return st, x.run(site, vals), nil
}

// MissingError: required .env values without a value.
type MissingError struct{ Names []string }

func (e *MissingError) Error() string {
	return "these values are not set in .env: " + strings.Join(e.Names, ", ")
}

func rtFile(path string, b []byte, perm uint32) File {
	s := string(b)
	return File{Path: path, Mode: "100644", Text: &s, Size: int64(len(b)), Perm: perm}
}

// runtimeFiles: paths relative to the site directory (.opm-runtime/…), Perm = the file mode to write.
func (x *exporter) runtimeFiles(site string, values map[string]string) ([]File, error) {
	st, r, err := x.realPass(site, values)
	if err != nil {
		return nil, err
	}
	for c, msg := range r.buildErr {
		return nil, fmt.Errorf("container %s: %s", c, msg)
	}
	var out []File
	// every File is mounted from .opm-runtime/files with the agent's mode (0444, or 0400 when a secret is in clear):
	// rendered ones from the real pass, the others copied from git (files/<slug>)
	for slug, f := range r.files {
		if st.fileDef[slug] {
			out = append(out, rtFile(".opm-runtime/files/"+slug, f.Bytes, uint32(f.Mode)))
			continue
		}
		out = append(out, File{Path: ".opm-runtime/files/" + slug, Mode: "100644", From: "files/" + slug, Perm: uint32(f.Mode)})
	}
	secrets := map[string]bool{}
	for _, b := range r.builts {
		if b == nil {
			continue
		}
		for _, m := range b.Spec.Mounts {
			if m.Origin.Kind == "secret" {
				secrets[m.Origin.Slug] = true
			}
		}
	}
	for slug := range secrets {
		v := ""
		for _, s := range r.cfg.Secrets {
			if s.SecretSlug == slug && s.Value != nil {
				v = *s.Value
			}
		}
		out = append(out, rtFile(".opm-runtime/secrets/"+slug, []byte(v), 0o400))
	}
	for _, b := range r.builts {
		if b == nil || len(st.derived[b.ContainerSlug]) == 0 {
			continue
		}
		em := envMap(b.Spec.Env)
		m := map[string]string{}
		for _, k := range st.derived[b.ContainerSlug] {
			m[k] = em[k]
		}
		j, _ := json.Marshal(m)
		out = append(out, rtFile(".opm-runtime/env/"+b.ContainerSlug+".env.json", j, 0o444))
	}
	if st.edgeDef && r.edge != nil {
		s, err := prettyJSON(r.edge)
		if err != nil {
			return nil, err
		}
		out = append(out, rtFile(".opm-runtime/edge/caddy.json", []byte(s), 0o444))
	}
	for h, pem := range r.tls {
		out = append(out, rtFile(".opm-runtime/tls/"+h+".crt", []byte(pem[0]), 0o444), rtFile(".opm-runtime/tls/"+h+".key", []byte(pem[1]), 0o400))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// SeedFile is one starter file opm-init writes: from git (GitPath, relative to the site dir) or rendered (Data).
type SeedFile struct {
	Path    string // inside the volume ("" = a single-file volume)
	GitPath string
	Data    []byte
	Mode    uint32
}

// SeedPlan is the starter content of one declared volume of the site.
type SeedPlan struct {
	Volume   string
	FileKind bool
	DirPath  string // where opm-init sees a folder volume
	FilePath string // where opm-init sees a single-file volume
	Source   string // tree | single | snapshot | archive | "" (no content: only the file of a single-file volume is created)
	Files    []SeedFile
	Dirs     []string
	Archive  string // git path of the .tar.gz (archive seeds)
	Skipped  []string
}

func parseMode(m string) uint32 {
	var v uint32
	if _, err := fmt.Sscanf(m, "%o", &v); err != nil || !volcontent.ReFileMode.MatchString(m) {
		return 0o644
	}
	return v
}

// Seeds plans the starter content of every declared volume of a site with the real values.
func Seeds(req Request, site string, values map[string]string) ([]SeedPlan, error) {
	x, err := newExporter(&req)
	if err != nil {
		return nil, err
	}
	st, r, err := x.realPass(site, values)
	if err != nil {
		return nil, err
	}
	sc := siteOf(r.cfg, site)
	var out []SeedPlan
	for _, v := range sc.Config.VolumeList() {
		v := v
		dirT, fileT := InitVolumeTarget(&v)
		kind, entries, dirs, arch := x.seedSource(site, &v)
		p := SeedPlan{Volume: v.VolumeSlug, FileKind: v.VolumeKind() == "file", DirPath: dirT, FilePath: fileT, Source: kind, Dirs: dirs}
		gitDir := "volumes/" + v.VolumeSlug
		for _, e := range entries {
			if e.size > MaxFileBytes {
				p.Skipped = append(p.Skipped, e.path)
				continue
			}
			sf := SeedFile{Path: e.path, Mode: parseMode(e.mode)}
			gp := gitDir
			if e.path != "" {
				gp += "/" + e.path
			}
			if e.render == "expressions" {
				if st.seedDef[v.VolumeSlug][e.path] {
					sf.Data = r.seeds[v.VolumeSlug][e.path]
					if sf.Data == nil {
						sf.Data = []byte{}
					}
				} else {
					sf.GitPath = gp
				}
			} else {
				sf.GitPath = gp
			}
			p.Files = append(p.Files, sf)
		}
		if arch != nil {
			p.Archive = gitDir + ".opm-seed.tar.gz"
		}
		out = append(out, p)
	}
	return out, nil
}

// Value is the value of one env NAME of a container rendered with the real values (`opm-render value`).
func Value(req Request, site, container, name string, values map[string]string) (string, error) {
	x, err := newExporter(&req)
	if err != nil {
		return "", err
	}
	_, r, err := x.realPass(site, values)
	if err != nil {
		return "", err
	}
	for _, b := range r.builts {
		if b != nil && b.ContainerSlug == container {
			if v, ok := envMap(b.Spec.Env)[name]; ok {
				return v, nil
			}
			return "", fmt.Errorf("container %s has no env %s", container, name)
		}
	}
	return "", fmt.Errorf("container %s is not on site %s", container, site)
}

var _ = driver.NamesPlain
var _ = model.Mask
