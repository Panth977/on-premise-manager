package cloud

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Release + install-dependency uploads (server/release.ts).
const (
	DockerVersion = "28.5.2"
	MinDockerAPI  = "1.41"
	AgentNotesMax = 8000
)

var upstreamArch = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

// SignHex signs a hex digest with an Ed25519 seed (base64): base64url.
func SignHex(hexStr, seedB64 string) string {
	key := ed25519.NewKeyFromSeed(doc.B64Decode(seedB64))
	return doc.B64URL(ed25519.Sign(key, []byte(hexStr)))
}

// ReadReleaseMeta: <dir>/release.json → the metadata to store (nil when absent); an error on a bad / mismatching file.
func ReadReleaseMeta(dir, version string) (*doc.Map, error) {
	f := filepath.Join(dir, "release.json")
	raw, err := os.ReadFile(f)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j, err := doc.ParseMap(raw)
	if err != nil {
		return nil, err
	}
	if j.Val("version") != version {
		return nil, fmt.Errorf("%s: version %s != --version %s", f, jsStringU(j, "version"), version)
	}
	isInt := func(k string) bool {
		n, ok := j.NumOK(k)
		return ok && n == float64(int64(n)) && n >= 0
	}
	if !isInt("schema") || !isInt("minSchema") || j.Num("minSchema") > j.Num("schema") {
		return nil, fmt.Errorf("%s: schema / minSchema must be integers with minSchema <= schema", f)
	}
	s := func(k string) any {
		if v, ok := j.StrOK(k); ok {
			return v
		}
		return nil
	}
	return doc.Obj("version", version, "schema", j.Val("schema"), "minSchema", j.Val("minSchema"), "commit", s("commit"), "builtAt", s("builtAt")), nil
}

func jsStringU(m *doc.Map, k string) string {
	if v, ok := m.Get(k); ok {
		return jsString(v)
	}
	return "undefined"
}

type agentBin struct{ os, arch, file string }

var (
	binRe = regexp.MustCompile(`^opm-agent[-_](linux|darwin)[-_](amd64|arm64)$`)
	dirRe = regexp.MustCompile(`^(linux|darwin)-(amd64|arm64)$`)
)

// FindBinaries lists opm-agent-<os>-<arch> and <os>-<arch>/opm-agent.
func FindBinaries(dir string) ([]agentBin, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []agentBin
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if m := binRe.FindStringSubmatch(e.Name()); m != nil && st.Mode().IsRegular() {
			out = append(out, agentBin{m[1], m[2], p})
		}
		if m := dirRe.FindStringSubmatch(e.Name()); m != nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "opm-agent")); err == nil {
				out = append(out, agentBin{m[1], m[2], filepath.Join(p, "opm-agent")})
			}
		}
	}
	return out, nil
}

// UploadAgentDir uploads agent/<v>/<os>-<arch>/opm-agent + .sha256 + .sig (+ release.json, + agent/latest).
func UploadAgentDir(b *driver.BlobStore, dir, version string, latest bool, seedB64 string, log func(string)) error {
	bins, err := FindBinaries(dir)
	if err != nil {
		return err
	}
	if len(bins) == 0 {
		return fmt.Errorf("no binaries found in %s (opm-agent-<os>-<arch> or <os>-<arch>/opm-agent)", dir)
	}
	meta, err := ReadReleaseMeta(dir, version)
	if err != nil {
		return err
	}
	text := driver.PutOptions{ContentType: "text/plain"}
	for _, x := range bins {
		data, err := os.ReadFile(x.file)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		hx := hex.EncodeToString(sum[:])
		base := "agent/" + version + "/" + x.os + "-" + x.arch + "/opm-agent"
		if err := b.Put(base, data, driver.PutOptions{ContentType: "application/octet-stream"}); err != nil {
			return err
		}
		if err := b.Put(base+".sha256", []byte(hx+"\n"), text); err != nil {
			return err
		}
		if err := b.Put(base+".sig", []byte(SignHex(hx, seedB64)+"\n"), text); err != nil {
			return err
		}
		log(fmt.Sprintf("uploaded %s (%d bytes, sha256 %s)", base, len(data), hx))
	}
	if meta != nil {
		raw, err := doc.MarshalIndent(meta, 2)
		if err != nil {
			return err
		}
		if err := b.Put("agent/"+version+"/release.json", append(raw, '\n'), driver.PutOptions{ContentType: "application/json"}); err != nil {
			return err
		}
		log(fmt.Sprintf("uploaded agent/%s/release.json (schema %s, minSchema %s)", version, jsString(meta.Val("schema")), jsString(meta.Val("minSchema"))))
	} else {
		log("no " + filepath.Join(dir, "release.json") + ": schema unknown (legacy 1)")
	}
	if latest {
		if err := b.Put("agent/latest", []byte(version+"\n"), text); err != nil {
			return err
		}
		log("agent/latest = " + version)
	}
	return nil
}

// ResolveAgentNotes: given (nil = undefined; "" = none) wins; undefined keeps the previous notes of an unchanged version.
func ResolveAgentNotes(given *string, agentVersion string, prev *doc.Map) (any, error) {
	if agentVersion == "" {
		return nil, nil
	}
	if given != nil {
		t := doc.JSTrim(strings.ReplaceAll(*given, "\r\n", "\n"))
		if n := doc.JSLen(t); n > AgentNotesMax {
			return nil, fmt.Errorf("agent notes are %d characters; at most %d", n, AgentNotesMax)
		}
		if t == "" {
			return nil, nil
		}
		return t, nil
	}
	a := prev.Map("agent")
	if a.Val("version") == agentVersion && a.Str("notes") != "" {
		return a.Str("notes"), nil
	}
	return nil, nil
}

// DepsPath is depsPath.
func DepsPath(version, arch string) string {
	return "deps/docker/" + version + "/" + arch + "/docker-" + version + ".tgz"
}

func fetchBundle(version, arch, fromDir string) ([]byte, error) {
	if fromDir != "" {
		f := filepath.Join(fromDir, "docker-"+version+"-"+arch+".tgz")
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("missing %s", f)
		}
		return b, nil
	}
	u := "https://download.docker.com/linux/static/stable/" + upstreamArch[arch] + "/docker-" + version + ".tgz"
	r, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %d", u, r.StatusCode)
	}
	return io.ReadAll(r.Body)
}

// UploadDeps is uploadDeps: docker bundles + deps/manifest.json. agentNotes nil = undefined.
func UploadDeps(b *driver.BlobStore, dockerVersion, agentVersion string, agentNotes *string, fromDir string, log func(string)) (*doc.Map, error) {
	var prev *doc.Map
	if raw, err := b.Get("deps/manifest.json"); err != nil {
		return nil, err
	} else if raw != nil {
		prev, _ = doc.ParseMap(raw)
	}
	notes, err := ResolveAgentNotes(agentNotes, agentVersion, prev)
	if err != nil {
		return nil, err
	}
	var meta *doc.Map
	if agentVersion != "" {
		raw, err := b.Get("agent/" + agentVersion + "/release.json")
		if err != nil {
			return nil, err
		}
		if raw != nil {
			if meta, err = doc.ParseMap(raw); err != nil {
				return nil, err
			}
		}
	}
	files := doc.New()
	for _, arch := range []string{"amd64", "arm64"} {
		path := DepsPath(dockerVersion, arch)
		data, err := fetchBundle(dockerVersion, arch, fromDir)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		hx := hex.EncodeToString(sum[:])
		h, err := b.Head(path)
		if err != nil {
			return nil, err
		}
		same := h != nil && h.Meta["sha256"] == hx
		verb := "kept"
		if !same {
			verb = "uploaded"
			if err := b.Put(path, data, driver.PutOptions{ContentType: "application/gzip", Meta: map[string]string{"sha256": hx}}); err != nil {
				return nil, err
			}
		}
		files.Set(arch, doc.Obj("path", path, "sha256", hx, "size", len(data)))
		log(fmt.Sprintf("%s %s (%d bytes, sha256 %s)", verb, path, len(data), hx))
	}
	var agent any
	if agentVersion != "" {
		agent = doc.Obj("version", agentVersion, "notes", notes, "schema", meta.Val("schema"), "minSchema", meta.Val("minSchema"))
	}
	m := doc.Obj("docker", doc.Obj("version", dockerVersion, "files", files), "minDockerApi", MinDockerAPI, "agent", agent, "updatedAt", doc.NowISO())
	raw, err := doc.MarshalIndent(m, 2)
	if err != nil {
		return nil, err
	}
	if err := b.Put("deps/manifest.json", append(raw, '\n'), driver.PutOptions{ContentType: "application/json"}); err != nil {
		return nil, err
	}
	nt := "(none)"
	if s, ok := notes.(string); ok {
		nt = fmt.Sprintf("%d chars", doc.JSLen(s))
		if agentNotes == nil {
			nt += " (kept)"
		}
	}
	av := agentVersion
	if av == "" {
		av = "(none)"
	}
	log(fmt.Sprintf("deps/manifest.json written: docker %s, minDockerApi %s, agent %s, notes %s", dockerVersion, MinDockerAPI, av, nt))
	return m, nil
}

// releaseKey is ensureReleaseKey: { privateKeySeedB64, publicKeyB64 } of <dataDir>/secrets/release-key.json.
func releaseKey(c *CloudConfig) (seed, pub string, created bool, err error) {
	f := releaseKeyFile(c)
	if raw, e := os.ReadFile(f); e == nil {
		m, e := doc.ParseMap(raw)
		if e != nil {
			return "", "", false, e
		}
		return m.Str("privateKeySeedB64"), m.Str("publicKeyB64"), false, nil
	}
	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", false, err
	}
	seed = base64.StdEncoding.EncodeToString(sk.Seed())
	pub = base64.StdEncoding.EncodeToString(pk)
	raw, err := doc.MarshalIndent(doc.Obj("privateKeySeedB64", seed, "publicKeyB64", pub), 2)
	if err != nil {
		return "", "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return "", "", false, err
	}
	if err := os.WriteFile(f, append(raw, '\n'), 0o600); err != nil {
		return "", "", false, err
	}
	return seed, pub, true, os.Chmod(f, 0o600)
}

func releaseKeyFile(c *CloudConfig) string {
	return filepath.Join(c.DataDir, "secrets", "release-key.json")
}
