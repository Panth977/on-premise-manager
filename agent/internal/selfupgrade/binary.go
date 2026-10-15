package selfupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Sha256File returns the hex sha256 of a file.
func Sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CopyFile copies src to dst (mode perm) with fsync; dst is replaced through a temp file + rename.
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".copy-tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

// PrevPath is the known-good copy next to the binary.
func PrevPath(binary string) string { return binary + ".prev" }

// NewPath is the staged new binary next to the binary.
func NewPath(binary string) string { return binary + ".new" }

// Swap installs <binary>.new (already written + fsynced) as <binary>: the current binary is kept as <binary>.prev
// (hard link, else a copy), then rename(new, binary) = atomic.
func Swap(binary string) error {
	newP, prev := NewPath(binary), PrevPath(binary)
	if _, err := os.Stat(newP); err != nil {
		return fmt.Errorf("staged binary %s: %w", newP, err)
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("current binary %s: %w", binary, err)
	}
	tmpPrev := prev + ".tmp"
	_ = os.Remove(tmpPrev)
	if err := os.Link(binary, tmpPrev); err != nil {
		if err := CopyFile(binary, tmpPrev, 0o755); err != nil {
			return fmt.Errorf("keep the current binary as %s: %w", prev, err)
		}
	}
	if err := os.Rename(tmpPrev, prev); err != nil {
		_ = os.Remove(tmpPrev)
		return fmt.Errorf("keep the current binary as %s: %w", prev, err)
	}
	if err := os.Rename(newP, binary); err != nil {
		return fmt.Errorf("install %s: %w", binary, err)
	}
	syncDir(filepath.Dir(binary))
	return nil
}

// Restore copies <binary>.prev over <binary> (atomic rename; .prev is kept).
func Restore(binary string) error {
	prev := PrevPath(binary)
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("%s is missing: cannot roll back", prev)
	}
	return CopyFile(prev, binary, 0o755)
}

// RunVersion runs `<bin> version` and returns its "opm-agent …" stdout line (10 s timeout). stderr is ignored unless
// the command fails (libraries may log there, e.g. Caddy without $HOME under systemd).
func RunVersion(ctx context.Context, bin string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "version")
	cmd.Env = append(os.Environ(), "OPM_SELF_TEST=1")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	line := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "opm-agent ") {
			line = l
			break
		}
	}
	if line == "" {
		line = strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0])
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return line, fmt.Errorf("%s version: %v (%s)", filepath.Base(bin), err, msg)
	}
	return line, nil
}

// ParseVersionLine extracts the version from "opm-agent <version> <os>/<arch> …".
func ParseVersionLine(line string) string {
	f := strings.Fields(line)
	if len(f) >= 2 && f[0] == "opm-agent" {
		return f[1]
	}
	return ""
}

// SelfTest runs `<bin> version` and checks it prints the expected version.
func SelfTest(ctx context.Context, bin, want string) error {
	line, err := RunVersion(ctx, bin)
	if err != nil {
		return err
	}
	if got := ParseVersionLine(line); got != want {
		return fmt.Errorf("self-test: %s version printed %q, expected opm-agent %s", filepath.Base(bin), line, want)
	}
	return nil
}

// ErrNoPrev is returned when a rollback is needed but <binary>.prev is missing.
var ErrNoPrev = errors.New("opm-agent.prev is missing")
