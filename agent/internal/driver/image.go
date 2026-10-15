package driver

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// BlobPath returns the local path of a blob by hex (without "sha256:").
type BlobPath func(hex string) string

func hexOf(d string) string { return strings.TrimPrefix(d, "sha256:") }

func shortHex(d string) string {
	h := hexOf(d)
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ImagePresent reports whether opm/cfg:<configHex> exists and matches the config's rootfs.
func ImagePresent(ctx context.Context, dc *docker.Client, img model.ImageSpec, blob BlobPath) (bool, error) {
	im, err := dc.ImageInspect(ctx, ImageRef(img.ConfigDigest))
	if errors.Is(err, docker.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if im.ID == img.ConfigDigest {
		return true, nil
	}
	want, err := diffIDs(blob(hexOf(img.ConfigDigest)))
	if err != nil {
		return true, nil // no local config blob to compare (image adopted from a previous run)
	}
	return equalStrings(want, im.RootFS.Layers), nil
}

func diffIDs(cfgPath string) ([]string, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var c struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return c.RootFS.DiffIDs, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LoadImage assembles a docker-archive from the config + layer blobs and POSTs it to /images/load (§9.5 step 3).
// RepoTags: the readable <serviceSlug>:<version> and opm/cfg:<configHex>. Verifies the loaded rootfs against the config,
// then makes sure the readable reference points to the verified image (EnsureReadable).
func LoadImage(ctx context.Context, dc *docker.Client, img model.ImageSpec, blob BlobPath) error {
	cfgHex := hexOf(img.ConfigDigest)
	readable, _, _ := ReadableRef(img)
	files := []string{cfgHex}
	layers := []string{}
	for _, l := range img.Layers {
		layers = append(layers, "blobs/sha256/"+hexOf(l.Digest))
		files = append(files, hexOf(l.Digest))
	}
	for _, h := range files {
		if _, err := os.Stat(blob(h)); err != nil {
			return fmt.Errorf("blob %s missing: %w", h[:12], err)
		}
	}
	manifest, _ := json.Marshal([]map[string]any{{
		"Config":   "blobs/sha256/" + cfgHex,
		"RepoTags": []string{readable, ImageRef(img.ConfigDigest)},
		"Layers":   layers,
	}})
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := func() error {
			if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manifest)), Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			if _, err := tw.Write(manifest); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0o755, Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/", Typeflag: tar.TypeDir, Mode: 0o755, Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, h := range files {
				if seen[h] {
					continue
				}
				seen[h] = true
				f, err := os.Open(blob(h))
				if err != nil {
					return err
				}
				st, _ := f.Stat()
				if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + h, Mode: 0o444, Size: st.Size(), Format: tar.FormatUSTAR}); err != nil {
					f.Close()
					return err
				}
				_, err = io.Copy(tw, f)
				f.Close()
				if err != nil {
					return err
				}
			}
			return tw.Close()
		}()
		pw.CloseWithError(err)
	}()
	if _, err := dc.ImageLoad(ctx, pr); err != nil {
		pr.CloseWithError(err)
		return err
	}
	ok, err := ImagePresent(ctx, dc, img, blob)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("loaded image %s:%s does not match config %s", img.ServiceSlug, img.Version, cfgHex[:12])
	}
	_, err = EnsureReadable(ctx, dc, img)
	return err
}

// EnsureReadable makes the readable reference <serviceSlug>:<version> point to the verified image opm/cfg:<configHex>
// (same image id). When it is missing or points to another image (e.g. overwritten by a manual docker tag / pull), it is
// re-tagged. Returns true when a tag was (re)written. opm/cfg:<configHex> must exist (ImagePresent / LoadImage).
func EnsureReadable(ctx context.Context, dc *docker.Client, img model.ImageSpec) (bool, error) {
	cfg, err := dc.ImageInspect(ctx, ImageRef(img.ConfigDigest))
	if err != nil && errors.Is(err, docker.ErrNotFound) && img.Digest == "" {
		// §21 #104 a local image (drift, no blobs): ConfigDigest is the docker image id — re-tag the integrity tag
		// (another agent on the same host may have pruned it while no container used the image)
		terr := dc.ImageTag(ctx, img.ConfigDigest, "opm/cfg", hexOf(img.ConfigDigest))
		if terr != nil && img.Reference != nil && *img.Reference != "" {
			// §21 #113 a site image this host does not have (another site, or removed from Docker): pull its reference
			if perr := dc.ImagePull(ctx, *img.Reference, img.Platform, nil, nil); perr != nil {
				return false, fmt.Errorf("site image %s: %w", *img.Reference, perr)
			}
			if got, ierr := dc.ImageInspect(ctx, *img.Reference); ierr == nil && got.ID != img.ConfigDigest {
				return false, fmt.Errorf("site image %s now is %s, not %s: change the container's image (the tag moved)", *img.Reference, shortHex(got.ID), shortHex(img.ConfigDigest))
			}
			terr = dc.ImageTag(ctx, img.ConfigDigest, "opm/cfg", hexOf(img.ConfigDigest))
		}
		if terr == nil {
			cfg, err = dc.ImageInspect(ctx, ImageRef(img.ConfigDigest))
		}
	}
	if err != nil {
		return false, fmt.Errorf("image %s: %w", ImageRef(img.ConfigDigest), err)
	}
	ref, repo, tag := ReadableRef(img)
	cur, err := dc.ImageInspect(ctx, ref)
	if err == nil && cur.ID == cfg.ID {
		return false, nil
	}
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		return false, err
	}
	if err := dc.ImageTag(ctx, ImageRef(img.ConfigDigest), repo, tag); err != nil {
		return false, fmt.Errorf("tag %s: %w", ref, err)
	}
	cur, err = dc.ImageInspect(ctx, ref)
	if err != nil {
		return true, err
	}
	if cur.ID != cfg.ID {
		return true, fmt.Errorf("tag %s does not point to %s after re-tagging", ref, ImageRef(img.ConfigDigest))
	}
	return true, nil
}
