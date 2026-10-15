package agent

// §21 #100 server filesystem picker for Transfers › Send › Path (GET /_opm/api/sites/{site}/transfers/fs) and the
// safety policy of sending a server file to the cloud. Mirror of shared/src/transfers.ts hostFsBlockReason (vectors
// shared/testdata/hostfs-vectors.json).
//
// The agent is a host binary (systemd, root on a standard install), not a container: "/" here IS the host's root
// filesystem, read with the agent's own user. A non-root agent sees "no permission" entries where the OS says so.

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"opm/agent/internal/model"
)

var (
	hostFsPseudo  = []string{"/proc", "/sys", "/dev", "/run"}
	hostFsSecrets = map[string]bool{"/etc/shadow": true, "/etc/shadow-": true, "/etc/gshadow": true, "/etc/gshadow-": true, "/etc/sudoers": true, "/etc/security/opasswd": true}
	reSSHHostKey  = regexp.MustCompile(`^/etc/ssh/ssh_host_[^/]*_key$`)
)

// fsClean is TS fsClean: lexical clean of an absolute "/" path; "" when not absolute or it contains NUL.
func fsClean(p string) string {
	if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return ""
	}
	out := []string{}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, seg)
		}
	}
	return "/" + strings.Join(out, "/")
}

func fsWithin(p, dir string) bool {
	if dir == "" {
		return false
	}
	if p == dir {
		return true
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	return strings.HasPrefix(p, dir)
}

// hostFsBlockReason is TS hostFsBlockReason: why a (real) server path cannot be sent; "" = allowed.
func hostFsBlockReason(p string, protected []string) string {
	c := fsClean(p)
	if c == "" {
		return "not an absolute path"
	}
	for _, d := range protected {
		if d == "" {
			continue
		}
		if pc := fsClean(d); pc != "" && pc != "/" && fsWithin(c, pc) {
			return "OPM agent data (site key, database, secrets) — never sent"
		}
	}
	for _, d := range hostFsPseudo {
		if fsWithin(c, d) {
			return d + " is a kernel / runtime filesystem — not a real file"
		}
	}
	if hostFsSecrets[c] {
		return "host password / sudo database — never sent"
	}
	if reSSHHostKey.MatchString(c) {
		return "SSH host private key — never sent"
	}
	return ""
}

// protectedPaths = the agent's own files: its home (site key, db, certs, secrets, blobs …), agent.json, the install token.
func (a *Agent) protectedPaths() []string {
	out := []string{}
	for _, p := range []string{a.dataDir, a.opt.ConfigPath, a.cfg.InstallTokenFile} {
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, abs)
			if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
				out = append(out, real)
			}
		}
	}
	return out
}

// sendBlockReason checks the requested path AND its resolved target (a symlink to /etc/shadow is refused too).
func (a *Agent) sendBlockReason(p string) string {
	prot := a.protectedPaths()
	if r := hostFsBlockReason(p, prot); r != "" {
		return r
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		if r := hostFsBlockReason(real, prot); r != "" {
			return r
		}
	}
	return ""
}

func processUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "uid " + strconv.Itoa(os.Geteuid())
}

// hostFsEntry describes one directory entry (lstat; symlinks are described, never followed for the type).
func (a *Agent) hostFsEntry(dir, name string, prot []string) model.HostFsEntry {
	full := filepath.Join(dir, name)
	e := model.HostFsEntry{Name: name, Path: full, Type: "other", Hidden: strings.HasPrefix(name, ".")}
	fi, err := os.Lstat(full)
	if err != nil {
		e.Readable = false
		return e
	}
	mt := tsOf(fi.ModTime())
	e.Mtime = &mt
	e.Mode = fi.Mode().String()
	blocked := hostFsBlockReason(full, prot)
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Type = "symlink"
		if t, err := os.Readlink(full); err == nil {
			e.LinkTarget = &t
		}
		tt := "missing"
		if st, err := os.Stat(full); err == nil {
			switch {
			case st.IsDir():
				tt = "dir"
				e.Readable = unix.Access(full, unix.R_OK|unix.X_OK) == nil
			case st.Mode().IsRegular():
				tt = "file"
				n := st.Size()
				e.SizeBytes = &n
				e.Readable = unix.Access(full, unix.R_OK) == nil
			default:
				tt = "other"
			}
			if real, err := filepath.EvalSymlinks(full); err == nil && blocked == "" {
				blocked = hostFsBlockReason(real, prot)
			}
		}
		e.TargetType = &tt
	case fi.IsDir():
		e.Type = "dir"
		e.Readable = unix.Access(full, unix.R_OK|unix.X_OK) == nil
	case fi.Mode().IsRegular():
		e.Type = "file"
		n := fi.Size()
		e.SizeBytes = &n
		e.Readable = unix.Access(full, unix.R_OK) == nil
	default:
		e.Type = "other" // socket, fifo, device
		if blocked == "" {
			blocked = "not a regular file (socket, pipe or device)"
		}
	}
	if blocked != "" {
		e.Blocked = &blocked
	}
	return e
}

// handleHostFs lists ONE directory level of the server filesystem (session; read-only; not an activity).
func (a *Agent) handleHostFs(w http.ResponseWriter, r *http.Request) (any, int, *httpError) {
	q := r.URL.Query()
	p := q.Get("path")
	if p == "" {
		p = "/"
	}
	hidden := q.Get("hidden") == "true" || q.Get("hidden") == "1"
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return nil, 0, invalid("path must be absolute", []model.ValidationIssue{{Path: "path", Code: "V_FORMAT", Message: "absolute path", Severity: "error"}})
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot open "+p, map[string]any{"path": p})
		}
		return nil, 0, herr(model.ErrNotFound, "no such file or folder on this server: "+p, map[string]any{"path": p})
	}
	if !st.IsDir() {
		t := "other"
		if st.Mode().IsRegular() {
			t = "file"
		}
		return nil, 0, herr(model.ErrInvalid, p+" is not a folder", map[string]any{"type": t, "path": p, "errors": []model.ValidationIssue{{Path: "path", Code: "V_TYPE", Message: "not a folder", Severity: "error"}}})
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot list "+p, map[string]any{"path": p})
		}
		return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
	}
	// names only (cheap); a huge folder is cut at 50 000 names before sorting
	names, err := f.Readdirnames(50_000)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(err, fs.ErrPermission) {
			return nil, 0, herr(model.ErrForbidden, "the agent (user "+processUser()+") cannot list "+p, map[string]any{"path": p})
		}
		return nil, 0, herr(model.ErrUnavailable, err.Error(), nil)
	}
	res := model.HostFsListResponse{Path: p, Entries: []model.HostFsEntry{}, AgentUser: processUser()}
	if p != "/" {
		par := filepath.Dir(p)
		res.Parent = &par
	}
	prot := a.protectedPaths()
	if b := hostFsBlockReason(p, prot); b != "" {
		res.Blocked = &b
	} else if real, err := filepath.EvalSymlinks(p); err == nil {
		if b := hostFsBlockReason(real, prot); b != "" {
			res.Blocked = &b
		}
	}
	visible := names[:0]
	for _, n := range names {
		if !hidden && strings.HasPrefix(n, ".") {
			res.HiddenCount++
			continue
		}
		visible = append(visible, n)
	}
	entries := make([]model.HostFsEntry, 0, len(visible))
	for _, n := range visible {
		entries = append(entries, a.hostFsEntry(p, n, prot))
	}
	isDir := func(e model.HostFsEntry) bool {
		return e.Type == "dir" || (e.Type == "symlink" && e.TargetType != nil && *e.TargetType == "dir")
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := isDir(entries[i]), isDir(entries[j])
		if di != dj {
			return di
		}
		li, lj := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if li != lj {
			return li < lj
		}
		return entries[i].Name < entries[j].Name
	})
	if len(entries) > model.HostFsMaxEntries {
		entries = entries[:model.HostFsMaxEntries]
		res.Truncated = true
	}
	if len(names) >= 50_000 {
		res.Truncated = true
	}
	res.Entries = entries
	return res, 200, nil
}
