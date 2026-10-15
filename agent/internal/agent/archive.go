package agent

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
)

// archiveDir writes a tar.zst of dir's content (relative "./…" paths, numeric uid/gid, modes, mtimes, symlinks;
// device files skipped) and returns size + sha256 hex (§13.1 item 3).
func archiveDir(dir, dst string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, "", err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		f.Close()
		return 0, "", err
	}
	tw := tar.NewWriter(zw)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		_ = os.MkdirAll(dir, 0o755)
	}
	var werr error
	if st, err := os.Stat(dir); err == nil && st.Mode().IsRegular() {
		// a single-file volume (round E): one regular entry "./file"
		werr = archiveOneFile(tw, dir, st)
	} else {
		werr = archiveTree(tw, dir)
	}
	if werr == nil {
		werr = tw.Close()
	}
	if werr == nil {
		werr = zw.Close()
	} else {
		zw.Close()
	}
	cerr := f.Close()
	if werr != nil {
		return 0, "", werr
	}
	if cerr != nil {
		return 0, "", cerr
	}
	return cw.n, hex.EncodeToString(h.Sum(nil)), nil
}

// SingleFileEntry is the tar entry name of a single-file volume archive.
const SingleFileEntry = "./file"

func archiveOneFile(tw *tar.Writer, p string, st os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(st, "")
	if err != nil {
		return err
	}
	hdr.Name, hdr.Uname, hdr.Gname = SingleFileEntry, "", ""
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		hdr.Uid, hdr.Gid = int(s.Uid), int(s.Gid)
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(p)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.CopyN(tw, src, hdr.Size)
	return err
}

func archiveTree(tw *tar.Writer, dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		name := "./"
		if rel != "." {
			name = "./" + filepath.ToSlash(rel)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) != 0 {
			return nil
		}
		link := ""
		if mode&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			hdr.Uid, hdr.Gid = int(st.Uid), int(st.Gid)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if mode.IsRegular() {
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			return err
		}
		return nil
	})
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// extractArchive clears dir and extracts a tar.zst into it.
func extractArchive(src, dir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	first, err := tr.Next()
	if err != nil && err != io.EOF {
		return err
	}
	if first != nil && first.Name == SingleFileEntry && (first.Typeflag == tar.TypeReg || first.Typeflag == tar.TypeRegA) {
		// a single-file volume: the content goes into the file in place (same inode for bind mounts)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return fmt.Errorf("volume location %s is a directory but the archive holds a single-file volume", dir)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(dir, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(first.Mode).Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		_ = os.Lchown(dir, first.Uid, first.Gid)
		_ = os.Chtimes(dir, first.ModTime, first.ModTime)
		return err
	}
	if st, err := os.Lstat(dir); err == nil && !st.IsDir() {
		if err := os.Remove(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	root, _ := filepath.Abs(dir)
	pending := first
	for {
		hdr, err := pending, error(nil)
		if hdr == nil {
			hdr, err = tr.Next()
		} else {
			pending = nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.Clean("/"+hdr.Name), "/")
		target := filepath.Join(root, name)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes the volume: %s", hdr.Name)
		}
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if target == root {
				_ = os.Chmod(root, mode|0o700)
				continue
			}
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			continue
		case tar.TypeLink:
			lt := filepath.Join(root, strings.TrimPrefix(filepath.Clean("/"+hdr.Linkname), "/"))
			_ = os.Link(lt, target)
			continue
		default:
			continue
		}
		_ = os.Lchown(target, hdr.Uid, hdr.Gid) // works as root; ignored otherwise
		_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
	}
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 1 << 62
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
