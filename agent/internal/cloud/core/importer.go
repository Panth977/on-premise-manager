package core

import (
	"errors"
	"strings"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
	"opm/agent/internal/cloud/shared"
)

func init() { cloud.RegisterTrigger("importImage", onImageWritten) }

var runImport = RunImport

// onImageWritten is HANDLERS.importImage of app.ts.
func onImageWritten(e driver.DocEvent) error {
	if e.After == nil || e.After.Map("import").Val("status") != "pending" {
		return nil
	}
	if e.Before != nil && e.Before.Map("import").Val("status") == "pending" {
		return nil
	}
	runImport(e.Params["serviceSlug"], e.Params["version"])
	return nil
}

// HostURL is hostUrl: the registry base URL of a reference host.
func HostURL(host string) string {
	if host == "docker.io" {
		return "https://registry-1.docker.io"
	}
	return "https://" + host
}

// SourceCreds is sourceCreds: the registry's credentials, or anonymous on the reference host when reg is nil.
func SourceCreds(source, reg *doc.Map) (RegistryCreds, error) {
	host := source.Str("host")
	if reg == nil {
		return RegistryCreds{URL: HostURL(host)}, nil
	}
	if h := shared.RegistryHost(reg.Str("url")); h != host {
		return RegistryCreds{}, regErr(`the registry "` + JSString(present(reg, "label")) + `" is for ` + h + ", but the reference is on " + host)
	}
	return RegistryCreds{URL: reg.Str("url"), Username: reg.Str("username"), Password: reg.Str("password")}, nil
}

// SourceRef is sourceRef: the digest when pinned, else the tag.
func SourceRef(source *doc.Map) string {
	return JSString(or(source, "digest", or(source, "tag", "latest")))
}

type importCtx struct {
	creds  RegistryCreds
	client *RegistryClient
	repo   string
	ref    string
}

func importContext(img *doc.Map) (*importCtx, error) {
	src := img.Map("source")
	if !src.Truthy("repository") {
		return nil, errors.New("the image has no source reference (fix it with PATCH reference while the import is failed)")
	}
	var reg *doc.Map
	if id := img.Str("registryId"); id != "" {
		r, err := cloud.Col.Registries().Doc(id).Get()
		if err != nil {
			return nil, err
		}
		if !r.Exists() {
			return nil, errors.New("the registry no longer exists")
		}
		reg = r.Data()
	}
	creds, err := SourceCreds(src, reg)
	if err != nil {
		return nil, err
	}
	return &importCtx{creds, NewRegistryClient(creds, 0), src.Str("repository"), SourceRef(src)}, nil
}

func claimImport(serviceSlug, version string) (*doc.Map, error) {
	ref := cloud.Col.Images(serviceSlug).Doc(version)
	return driver.RunTx(cloud.DB(), func(tx *driver.Tx) (*doc.Map, error) {
		s, err := tx.Get(ref)
		if err != nil || !s.Exists() {
			return nil, err
		}
		img := s.Data()
		imp := img.Map("import")
		if imp.Val("status") != "pending" {
			return nil, nil
		}
		startedAt := doc.NowISO()
		tx.Update(ref, doc.Obj("import.status", "copying", "import.startedAt", startedAt, "import.error", nil, "import.finishedAt", nil))
		return img.Shallow().Set("import", imp.Shallow().Set("status", "copying").Set("startedAt", startedAt)), nil
	})
}

func auditDocOrNil(path string) *doc.Map {
	d, err := cloud.ReadAuditDoc(path)
	if err != nil {
		return nil
	}
	return d
}

func importImage(img *doc.Map, ref *driver.DocRef) (map[string]any, error) {
	ctx, err := importContext(img)
	if err != nil {
		return nil, err
	}
	platform := img.Str("platform")
	client, repo := ctx.client, ctx.repo
	pm, err := WithAccessHint(ctx.creds, func() (*ManifestResult, error) { return client.PlatformManifest(repo, ctx.ref, platform) }, BrowseOpts{})
	if err != nil {
		return nil, err
	}
	if d := img.Str("digest"); d != "" && d != pm.Digest {
		return nil, errors.New("the tag now points to " + pm.Digest + " (registered digest " + d + "); register a new version")
	}
	mHex := cloud.HexOf(pm.Digest)
	if ok, err := Exists(Paths.Blob(mHex)); err != nil {
		return nil, err
	} else if !ok {
		if err := WriteBytes(Paths.Blob(mHex), pm.Bytes, ""); err != nil {
			return nil, err
		}
	}
	cfgDigest := pm.Manifest.Map("config").Str("digest")
	cfgBytes, err := client.BlobBytes(repo, cfgDigest)
	if err != nil {
		return nil, err
	}
	if cloud.Sha256Of(cfgBytes) != cfgDigest {
		return nil, errors.New("config blob sha256 mismatch (" + cfgDigest + ")")
	}
	cfgv, err := doc.Parse(cfgBytes)
	if err != nil {
		return nil, err
	}
	cfg, _ := cfgv.(*doc.Map)
	_, arch, _ := strings.Cut(platform, "/")
	if a := cfg.Val("architecture"); doc.Truthy(a) && a != arch {
		return nil, errors.New("platform " + platform + " not in the image (it is " + JSString(or(cfg, "os", "linux")) + "/" + JSString(a) + ")")
	}
	if ok, err := Exists(Paths.Blob(cloud.HexOf(cfgDigest))); err != nil {
		return nil, err
	} else if !ok {
		if err := WriteBytes(Paths.Blob(cloud.HexOf(cfgDigest)), cfgBytes, ""); err != nil {
			return nil, err
		}
	}
	layers := []any{}
	total := 0.0
	for _, x := range pm.Manifest.Arr("layers") {
		l := x.(*doc.Map)
		layers = append(layers, doc.Obj("digest", present(l, "digest"), "sizeBytes", present(l, "size"), "mediaType", present(l, "mediaType")))
		total += l.Num("size")
	}
	if err := ref.Update(doc.Obj("_progress", doc.Obj("bytesDone", 0.0, "bytesTotal", total))); err != nil {
		return nil, err
	}
	if img.Map("import").Val("mode") == "copy" {
		done := 0.0
		for _, x := range layers {
			l := x.(*doc.Map)
			hex := cloud.HexOf(l.Str("digest"))
			have, err := SizeOf(Paths.Blob(hex))
			if err != nil {
				return nil, err
			}
			if float64(have) != l.Num("sizeBytes") {
				stream, _, err := client.BlobStream(repo, l.Str("digest"))
				if err != nil {
					return nil, err
				}
				_, _, err = StreamToStorage(Paths.Blob(hex), stream, hex, "")
				stream.Close()
				if err != nil {
					return nil, err
				}
			}
			done += l.Num("sizeBytes")
			if err := ref.Update(doc.Obj("_progress", doc.Obj("bytesDone", done, "bytesTotal", total))); err != nil {
				return nil, err
			}
		}
	}
	if err := ref.Update(doc.Obj("digest", pm.Digest, "import.status", "ready", "import.error", nil, "import.layers", layers, "import.sizeBytes", total, "import.configDigest", cfgDigest, "import.manifestMediaType", pm.MediaType, "import.finishedAt", doc.NowISO(), "_configSizeBytes", float64(len(cfgBytes)), "_manifestSizeBytes", float64(len(pm.Bytes)), "updatedAt", doc.NowISO(), "updatedBy", "system")); err != nil {
		return nil, err
	}
	return map[string]any{"digest": pm.Digest, "layers": float64(len(layers)), "total": total}, nil
}

// RunImport is runImport: claims a pending import and copies the image. Failures end in import.status failed.
func RunImport(serviceSlug, version string) {
	img, err := claimImport(serviceSlug, version)
	if err != nil {
		driver.Log.Warn("image import claim failed", "serviceSlug", serviceSlug, "version", version, "error", err)
		return
	}
	if img == nil {
		return
	}
	ref := cloud.Col.Images(serviceSlug).Doc(version)
	target := "services/" + serviceSlug + "/images/" + version
	before := auditDocOrNil(target)
	mode := present(img.Map("import"), "mode")
	r, err := importImage(img, ref)
	if err != nil {
		msg := doc.JSSlice(err.Error(), 1000)
		driver.Log.Warn("image import failed", "serviceSlug", serviceSlug, "version", version, "error", msg)
		_ = ref.Update(doc.Obj("import.status", "failed", "import.error", msg, "import.finishedAt", doc.NowISO(), "updatedAt", doc.NowISO(), "updatedBy", "system"))
		cloud.AuditSystem(cloud.AuditSystemOpts{Job: "importImage", Action: "image.import-failed", TargetType: "image", Target: target, OK: false, ErrorMessage: &msg, HasDiff: true, Before: before, After: auditDocOrNil(target), Data: doc.Obj("serviceSlug", serviceSlug, "version", version, "reference", present(img, "reference"), "mode", mode, "error", msg)})
		return
	}
	driver.Log.Info("image imported", "serviceSlug", serviceSlug, "version", version, "digest", r["digest"], "layers", r["layers"], "total", r["total"])
	cloud.AuditSystem(cloud.AuditSystemOpts{Job: "importImage", Action: "image.import-ready", TargetType: "image", Target: target, OK: true, HasDiff: true, Before: before, After: auditDocOrNil(target), Data: doc.Obj("serviceSlug", serviceSlug, "version", version, "reference", present(img, "reference"), "mode", mode, "digest", r["digest"], "layers", r["layers"], "sizeBytes", r["total"])})
}

// FetchLayer is fetchLayer: gateway mode, stream a missing layer from the registry into Storage (first agent fetch).
func FetchLayer(img *doc.Map, digest string) error {
	hex := cloud.HexOf(digest)
	if ok, err := Exists(Paths.Blob(hex)); ok || err != nil {
		return err
	}
	ctx, err := importContext(img)
	if err != nil {
		return err
	}
	stream, _, err := ctx.client.BlobStream(ctx.repo, digest)
	if err != nil {
		return err
	}
	defer stream.Close()
	_, _, err = StreamToStorage(Paths.Blob(hex), stream, hex, "")
	return err
}
