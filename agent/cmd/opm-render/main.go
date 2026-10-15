// Command opm-render is the runtime helper of the §21 #106 compose export (committed into every mirrored branch at
// opm/bin/opm-render-linux-<arch>; static, no libc):
//
//	opm-render init --site <site>      the opm-init service: checks .env, renders the .env-dependent files (the SAME
//	                                    export code as the cloud, with the real values), creates single-file volumes,
//	                                    seeds every empty volume once (marker .opm-seeded), installs itself for probes
//	opm-render probe http --port N --path P [--expect 200] [--timeout 5]   healthcheck (the container's own address)
//	opm-render probe tcp --port N [--timeout 5]
//	opm-render probe exec --expect N -- cmd …
//	opm-render exec --env <file.json> -- cmd …   sets the derived env values, then exec(cmd) (the process stays PID 1)
//	opm-render value <container> <ENV> --site <site>   prints one rendered env value (debug)
//	opm-render environ [pid]           prints /proc/<pid>/environ (debug / tests)
//	opm-render version
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"opm/agent/internal/export"
)

// Version is set at build time (-X main.Version=…).
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	code := 0
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "probe":
		code = cmdProbe(os.Args[2:])
	case "exec":
		err = cmdExec(os.Args[2:])
	case "value":
		err = cmdValue(os.Args[2:])
	case "environ":
		err = cmdEnviron(os.Args[2:])
	case "version":
		fmt.Println("opm-render", Version, "export format", export.Format)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opm-render:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: opm-render init --site <site> | probe http|tcp|exec … | exec --env <file> -- cmd … | value <container> <ENV> --site <site> | environ [pid] | version")
	os.Exit(2)
}

// flags parses --name value pairs until "--"; returns the map and the rest after "--".
func flags(args []string) (map[string]string, []string) {
	m := map[string]string{}
	pos := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return m, args[i+1:]
		}
		if strings.HasPrefix(a, "--") {
			k := strings.TrimPrefix(a, "--")
			if j := strings.IndexByte(k, '='); j >= 0 {
				m[k[:j]] = k[j+1:]
				continue
			}
			if i+1 < len(args) {
				m[k] = args[i+1]
				i++
			}
			continue
		}
		m["_"+strconv.Itoa(pos)] = a
		pos++
	}
	return m, nil
}

func or(m map[string]string, k, d string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return d
}

func loadRequest(path string) (export.Request, error) {
	var req export.Request
	raw, err := os.ReadFile(path)
	if err != nil {
		return req, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, fmt.Errorf("%s: %w", path, err)
	}
	if req.Format != export.Format {
		return req, fmt.Errorf("%s has export format %d; this opm-render understands %d", path, req.Format, export.Format)
	}
	return req, nil
}

func envValues() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && strings.HasPrefix(kv, "OPM_") {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// writeInPlace writes b to p keeping the inode (a running container's single-file bind mount sees the new bytes).
func writeInPlace(p string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if st, err := os.Lstat(p); err == nil && st.IsDir() {
		// Docker created a directory for a bind source that did not exist yet: replace it (it is empty)
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("%s is a directory: %w", p, err)
		}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		_ = os.Chmod(p, 0o600)
		if f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm); err != nil {
			return err
		}
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(p, perm)
}

func cmdInit(args []string) error {
	f, _ := flags(args)
	site := f["site"]
	if site == "" {
		return errors.New("init: --site is required")
	}
	exportPath := or(f, "export", "/opm/export.json")
	siteDir := or(f, "site-dir", "/site")
	rtDir := or(f, "runtime", "/opm-runtime")
	dataDir := or(f, "data", "/opm-data")
	toolsDir := or(f, "tools", "/opm-tools")
	req, err := loadRequest(exportPath)
	if err != nil {
		return err
	}
	vals := envValues()
	if miss, err := export.Missing(req, site, vals); err != nil {
		return err
	} else if len(miss) > 0 {
		fmt.Fprintf(os.Stderr, "opm-init: site %s needs these values in sites/%s/.env (see .env.example):\n", site, site)
		for _, m := range miss {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		return errors.New("missing .env values")
	}
	res, err := export.Export(req, export.Mode{Runtime: true, Site: site, Values: vals})
	if err != nil {
		return err
	}
	for _, file := range res.Files {
		rel := strings.TrimPrefix(file.Path, ".opm-runtime/")
		var b []byte
		if file.Text != nil {
			b = []byte(*file.Text)
		}
		if file.From != "" {
			x, err := os.ReadFile(filepath.Join(siteDir, filepath.FromSlash(file.From)))
			if err != nil {
				return fmt.Errorf("%s: %w", file.From, err)
			}
			b = x
		}
		perm := os.FileMode(file.Perm)
		if perm == 0 {
			perm = 0o444
		}
		if err := writeInPlace(filepath.Join(rtDir, filepath.FromSlash(rel)), b, perm); err != nil {
			return fmt.Errorf("write %s: %w", file.Path, err)
		}
		fmt.Printf("opm-init: rendered %s\n", file.Path)
	}
	plans, err := export.Seeds(req, site, vals)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if err := seed(p, siteDir, dataDir); err != nil {
			return fmt.Errorf("volume %s: %w", p.Volume, err)
		}
	}
	if err := installSelf(toolsDir); err != nil {
		fmt.Fprintln(os.Stderr, "opm-init: probe tool not installed:", err)
	}
	fmt.Printf("opm-init: site %s ready (%d rendered file(s), %d volume(s))\n", site, len(res.Files), len(plans))
	return nil
}

func installSelf(dir string) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".opm-render.tmp")
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o755)
	return os.Rename(tmp, filepath.Join(dir, "opm-render"))
}

func dirEmpty(p string) bool {
	es, err := os.ReadDir(p)
	return err != nil || len(es) == 0
}

func markerJSON(source string, n int) []byte {
	return []byte(fmt.Sprintf(`{"at":%q,"source":%q,"entries":%d}`+"\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), source, n))
}

func seed(p export.SeedPlan, siteDir, dataDir string) error {
	if p.FileKind {
		fp := p.FilePath
		if fp == "" {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			return err
		}
		if st, err := os.Lstat(fp); err == nil && st.IsDir() {
			if dirEmpty(fp) {
				_ = os.Remove(fp) // created by Docker as a missing bind source
			} else {
				return fmt.Errorf("%s is a folder, the volume is a single file", fp)
			}
		}
		marker := filepath.Join(dataDir, ".opm-seeded", p.Volume)
		st, err := os.Stat(fp)
		if err != nil {
			if err := os.WriteFile(fp, nil, 0o644); err != nil {
				return err
			}
			st, _ = os.Stat(fp)
		}
		if p.Source == "" || len(p.Files) == 0 {
			return nil
		}
		if _, err := os.Stat(marker); err == nil || (st != nil && st.Size() > 0) {
			return nil
		}
		sf := p.Files[0]
		b, err := seedBytes(sf, siteDir)
		if err != nil {
			return err
		}
		if err := writeInPlace(fp, b, os.FileMode(sf.Mode)); err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(marker), 0o755)
		_ = os.WriteFile(marker, markerJSON(p.Source, 1), 0o644)
		fmt.Printf("opm-init: volume %s seeded (1 file)\n", p.Volume)
		return nil
	}
	dir := p.DirPath
	if dir == "" || p.Source == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".opm-seeded")); err == nil || !dirEmpty(dir) {
		return nil
	}
	n := 0
	if p.Archive != "" {
		c, err := extract(filepath.Join(siteDir, filepath.FromSlash(p.Archive)), dir)
		if err != nil {
			return err
		}
		n = c
	}
	for _, sf := range p.Files {
		b, err := seedBytes(sf, siteDir)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(sf.Path))
		if !strings.HasPrefix(dst, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("bad content path %s", sf.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, os.FileMode(sf.Mode)); err != nil {
			return err
		}
		_ = os.Chmod(dst, os.FileMode(sf.Mode))
		n++
	}
	for _, d := range p.Dirs {
		_ = os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755)
	}
	if len(p.Skipped) > 0 {
		fmt.Printf("opm-init: volume %s: %d file(s) larger than 100 MB are not in git and were not written\n", p.Volume, len(p.Skipped))
	}
	src := p.Source
	if src == "snapshot" {
		src = "tree"
	}
	if err := os.WriteFile(filepath.Join(dir, ".opm-seeded"), markerJSON(src, n), 0o644); err != nil {
		return err
	}
	fmt.Printf("opm-init: volume %s seeded (%d file(s))\n", p.Volume, n)
	return nil
}

func seedBytes(sf export.SeedFile, siteDir string) ([]byte, error) {
	if sf.GitPath == "" {
		return sf.Data, nil
	}
	return os.ReadFile(filepath.Join(siteDir, filepath.FromSlash(sf.GitPath)))
}

// extract a .tar.gz seed: regular files + folders only, safe relative paths (like the agent).
func extract(src, dir string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(zr)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name := filepath.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return n, fmt.Errorf("unsafe archive entry %q", h.Name)
		}
		dst := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return n, err
			}
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o200)
			if err != nil {
				return n, err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return n, err
			}
			out.Close()
			n++
		default:
			return n, fmt.Errorf("unsupported archive entry %q (type %c)", h.Name, h.Typeflag)
		}
	}
}

// ownAddress is the container's first non-loopback IPv4 address (the agent checks through the published port, i.e. from
// outside the container's loopback).
func ownAddress() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}

func cmdProbe(args []string) int {
	if len(args) == 0 {
		usage()
	}
	kind := args[0]
	f, rest := flags(args[1:])
	timeout, _ := strconv.ParseFloat(or(f, "timeout", "5"), 64)
	d := time.Duration(timeout * float64(time.Second) * 0.9)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	addr := net.JoinHostPort(or(f, "host", ownAddress()), or(f, "port", "0"))
	switch kind {
	case "http":
		want, _ := strconv.Atoi(or(f, "expect", "200"))
		path := or(f, "path", "/")
		r, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+path, nil)
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			fmt.Printf("GET %s → %d (want %d)\n", path, resp.StatusCode, want)
			return 1
		}
		return 0
	case "tcp":
		var dl net.Dialer
		c, err := dl.DialContext(ctx, "tcp", addr)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		c.Close()
		return 0
	case "exec":
		want, _ := strconv.Atoi(or(f, "expect", "0"))
		if len(rest) == 0 {
			return 2
		}
		c := exec.CommandContext(ctx, rest[0], rest[1:]...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		err := c.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			fmt.Println(err)
			return 1
		}
		if code != want {
			fmt.Printf("exit %d (want %d)\n", code, want)
			return 1
		}
		return 0
	}
	usage()
	return 2
}

func cmdExec(args []string) error {
	f, rest := flags(args)
	if len(rest) == 0 {
		return errors.New("exec: no command after --")
	}
	if p := f["env"]; p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_ = os.Setenv(k, m[k])
		}
	}
	bin := rest[0]
	if !strings.Contains(bin, "/") {
		lp, err := exec.LookPath(bin)
		if err != nil {
			return err
		}
		bin = lp
	}
	return syscall.Exec(bin, rest, os.Environ())
}

func cmdValue(args []string) error {
	f, _ := flags(args)
	c, n := f["_0"], f["_1"]
	if c == "" || n == "" || f["site"] == "" {
		return errors.New("value <container> <ENV> --site <site>")
	}
	req, err := loadRequest(or(f, "export", "/opm/export.json"))
	if err != nil {
		return err
	}
	v, err := export.Value(req, f["site"], c, n, envValues())
	if err != nil {
		return err
	}
	fmt.Println(v)
	return nil
}

func cmdEnviron(args []string) error {
	pid := "1"
	if len(args) > 0 {
		pid = args[0]
	}
	b, err := os.ReadFile("/proc/" + pid + "/environ")
	if err != nil {
		return err
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		if kv != "" {
			fmt.Println(kv)
		}
	}
	return nil
}
