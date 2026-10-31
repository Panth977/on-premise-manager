package shared

import (
	"math"
	"regexp"
	"slices"
	"strings"

	"opm/agent/internal/cloud/doc"
)

// HOST_FS_PSEUDO is HOST_FS_PSEUDO.
var HOST_FS_PSEUDO = []string{"/proc", "/sys", "/dev", "/run"}

// HOST_FS_SECRETS is HOST_FS_SECRETS.
var HOST_FS_SECRETS = []string{"/etc/shadow", "/etc/shadow-", "/etc/gshadow", "/etc/gshadow-", "/etc/sudoers", "/etc/security/opasswd"}

var jReSSHKey = regexp.MustCompile(`^/etc/ssh/ssh_host_[^/]*_key$`)

// FsClean is fsClean (nil = null).
func FsClean(p string) *string {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") {
		return nil
	}
	var out []string
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
	s := "/" + strings.Join(out, "/")
	return &s
}

// FsParent is fsParent (nil = null).
func FsParent(p string) *string {
	c := FsClean(p)
	if c == nil || *c == "/" {
		return nil
	}
	s := "/"
	if i := strings.LastIndexByte(*c, '/'); i > 0 {
		s = (*c)[:i]
	}
	return &s
}

// FsCrumbs is fsCrumbs: [{ name, path }].
func FsCrumbs(p string) []any {
	c := "/"
	if x := FsClean(p); x != nil {
		c = *x
	}
	out := []any{doc.Obj("name", "/", "path", "/")}
	acc := ""
	for _, seg := range strings.Split(c, "/") {
		if seg != "" {
			acc += "/" + seg
			out = append(out, doc.Obj("name", seg, "path", acc))
		}
	}
	return out
}

func jWithin(p, dir string) bool {
	if dir == "" {
		return false
	}
	if p == dir {
		return true
	}
	if strings.HasSuffix(dir, "/") {
		return strings.HasPrefix(p, dir)
	}
	return strings.HasPrefix(p, dir+"/")
}

// HostFsBlockReason is hostFsBlockReason (nil = null = allowed).
func HostFsBlockReason(p string, protectedPaths []string) *string {
	reason := func(s string) *string { return &s }
	cp := FsClean(p)
	if cp == nil {
		return reason("not an absolute path")
	}
	c := *cp
	for _, d := range protectedPaths {
		if pc := FsClean(d); d != "" && pc != nil && *pc != "/" && jWithin(c, *pc) {
			return reason("OPM agent data (site key, database, secrets) — never sent")
		}
	}
	for _, d := range HOST_FS_PSEUDO {
		if jWithin(c, d) {
			return reason(d + " is a kernel / runtime filesystem — not a real file")
		}
	}
	if slices.Contains(HOST_FS_SECRETS, c) {
		return reason("host password / sudo database — never sent")
	}
	if jReSSHKey.MatchString(c) {
		return reason("SSH host private key — never sent")
	}
	return nil
}

func jPct(done, size float64) float64 {
	if size > 0 {
		return math.Max(0, math.Min(1, done/size))
	}
	return 0
}

// TransferState is transferState (side "agent" | "cloud"): { key, label, tone, progress, hint }.
func TransferState(t *doc.Map, side string) *doc.Map {
	st := func(key, label, tone string, progress, hint any) *doc.Map {
		return doc.Obj("key", key, "label", label, "tone", tone, "progress", progress, "hint", hint)
	}
	d := t.Map("delivery")
	var retry any
	if d != nil && d.Num("attempts") > 0 {
		s := "retry " + jStr(d.Val("attempts"))
		if d.Truthy("lastError") {
			s += " — " + jStr(d.Val("lastError"))
		}
		retry = s
	}
	status := t.Str("status")
	size := t.Num("sizeBytes")
	phase := ""
	if d != nil {
		phase = d.Str("phase")
	}
	if status == "expired" {
		return st("expired", "deleted", "", nil, nil)
	}
	if t.Str("direction") == "to-cloud" {
		if status == "downloaded" {
			var hint any
			if side == "agent" {
				hint = "the operations team downloaded it"
			}
			return st("downloaded", "stored · downloaded", "accent", nil, hint)
		}
		if status == "ready" {
			label := "stored"
			if side == "agent" {
				label = "stored in the cloud"
			}
			return st("stored", label, "ok", nil, nil)
		}
		if side == "cloud" {
			return st("uploading", "arriving from the site", "info", nil, "the site uploads it in the background")
		}
		if phase == "uploading" {
			return st("uploading", "uploading", "info", jPct(d.Num("bytesDone"), size), retry)
		}
		var progress any
		if d != nil && d.Num("bytesDone") > 0 {
			progress = jPct(d.Num("bytesDone"), size)
		}
		return st("queued", "queued", "warn", progress, jCoal(retry, "stored on the site; uploads as soon as the cloud is reachable"))
	}
	switch status {
	case "uploading":
		return st("browser-uploading", "uploading from the browser", "info", nil, nil)
	case "saved-on-server":
		return st("saved-on-server", "saved on server", "accent", nil, t.Val("savedPath"))
	case "downloaded":
		var hint any
		if side == "cloud" {
			hint = "downloaded in the agent GUI"
		}
		return st("downloaded", "downloaded", "accent", nil, hint)
	}
	if side == "cloud" {
		if t.Truthy("fetchedAt") {
			return st("on-site", "on the site", "ok", nil, nil)
		}
		return st("pending", "stored · waiting for the site", "warn", nil, "the agent fetches it the next time it is online")
	}
	if phase == "downloading" {
		return st("downloading", "downloading", "info", jPct(d.Num("bytesDone"), size), retry)
	}
	if phase == "pending" {
		return st("pending", "pending", "warn", nil, jCoal(retry, "in the cloud; fetched as soon as the cloud is reachable"))
	}
	return st("on-site", "on this site", "ok", nil, nil)
}
