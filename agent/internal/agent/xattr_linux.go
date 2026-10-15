//go:build linux

package agent

import (
	"strings"

	"golang.org/x/sys/unix"
)

// copyXattrs copies the extended attributes of src to dst (symlinks themselves; §21 #96 "preserve"). It returns a
// warning ("" = ok / nothing to copy).
func copyXattrs(src, dst string) string {
	sz, err := unix.Llistxattr(src, nil)
	if err != nil || sz <= 0 {
		return ""
	}
	buf := make([]byte, sz)
	sz, err = unix.Llistxattr(src, buf)
	if err != nil {
		return ""
	}
	for _, name := range strings.Split(strings.TrimRight(string(buf[:sz]), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		vs, err := unix.Lgetxattr(src, name, nil)
		if err != nil || vs < 0 {
			continue
		}
		v := make([]byte, vs)
		if vs, err = unix.Lgetxattr(src, name, v); err != nil {
			continue
		}
		if err := unix.Lsetxattr(dst, name, v[:vs], 0); err != nil {
			return "extended attributes not kept: " + err.Error()
		}
	}
	return ""
}
