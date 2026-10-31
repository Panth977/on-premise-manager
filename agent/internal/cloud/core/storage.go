package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

// Paths is the paths object of storage.ts.
var Paths pathsT

type pathsT struct{}

func (pathsT) Blob(hex string) string          { return "blobs/sha256/" + hex }
func (pathsT) File(hex string) string          { return "files/" + hex }
func (pathsT) Transfer(dep, tid string) string { return "transfers/" + dep + "/" + tid }

// SharedFile is the cloud copy of a shared file (a file migrated from a transfer keeps its transfers/… object).
func (pathsT) SharedFile(dep, fileID string) string { return "shared-files/" + dep + "/" + fileID }
func (pathsT) UploadPart(uid string, n int) string {
	return "uploads/" + uid + "/part-" + strconv.Itoa(n)
}
func (pathsT) Logo(dep string) string         { return "logos/" + dep }
func (pathsT) Bundle(dep, name string) string { return "activity-bundles/" + dep + "/" + name }
func (pathsT) AgentBinary(v, os, arch string) string {
	return "agent/" + v + "/" + os + "-" + arch + "/opm-agent"
}

// AgentLatest is paths.agentLatest.
const AgentLatest = "agent/latest"

// Exists is exists.
func Exists(path string) (bool, error) {
	m, err := cloud.Blobs().Head(path)
	return m != nil, err
}

// SizeOf is sizeOf: -1 = null (no such object).
func SizeOf(path string) (int64, error) {
	m, err := cloud.Blobs().Head(path)
	if err != nil || m == nil {
		return -1, err
	}
	return m.Size, nil
}

// ReadText is readText: ok=false = null.
func ReadText(path string) (string, bool, error) {
	b, err := cloud.Blobs().Get(path)
	if err != nil || b == nil {
		return "", false, err
	}
	return Utf8(b), true, nil
}

// WriteBytes is writeBytes; contentType "" = application/octet-stream.
func WriteBytes(path string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return cloud.Blobs().Put(path, data, driver.PutOptions{ContentType: contentType, CacheControl: "private, max-age=0"})
}

// Remove is remove.
func Remove(path string) error { return cloud.Blobs().Delete(path) }

// RemovePrefix is removePrefix.
func RemovePrefix(prefix string) error { return cloud.Blobs().DeletePrefix(prefix) }

// StreamToStorage is streamToStorage: stores src while hashing, deletes the object on a hash mismatch. expectHex ""
// = no check, contentType "" = application/octet-stream.
func StreamToStorage(path string, src io.Reader, expectHex, contentType string) (hexOut string, size int64, err error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w, err := cloud.Blobs().WriteStream(path, driver.PutOptions{ContentType: contentType})
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(w, h), src)
	if err != nil {
		w.Abort()
		return "", 0, err
	}
	if err = w.Close(); err != nil {
		return "", 0, err
	}
	hexOut = hex.EncodeToString(h.Sum(nil))
	if expectHex != "" && hexOut != expectHex {
		if err := Remove(path); err != nil {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("sha256 mismatch for %s: got %s, expected %s", path, hexOut, expectHex)
	}
	return hexOut, size, nil
}

// HashObject is hashObject: sha256 hex + size of a stored object.
func HashObject(path string) (string, int64, error) {
	r, err := cloud.Blobs().Stream(path, nil)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	size, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// ServeOpts of ServeObject.
type ServeOpts struct {
	Range      string
	Headers    map[string]string
	NoRedirect bool
	FileName   string
	// ContentType "" = application/octet-stream
	ContentType string
	// StreamMax: bodies up to this size always stream (nil = no such limit)
	StreamMax *int64
}

// NoRedirectFor is noRedirectFor: a relayed or browser-sent (Origin header) agent request must stream, never 302.
func NoRedirectFor(headers map[string]string, relayedBy string) bool {
	return relayedBy != "" || headers["origin"] != ""
}

// ServeMode is serveMode: "redirect" | "stream".
func ServeMode(size int64, redirect, noRedirect bool, streamMax *int64) string {
	if !redirect || noRedirect {
		return "stream"
	}
	if streamMax != nil && size <= *streamMax {
		return "stream"
	}
	return "redirect"
}

var (
	reRange     = regexp.MustCompile(`^bytes=([0-9]*)-([0-9]*)$`)
	fileNameBad = strings.NewReplacer(`"`, "_", `\`, "_", "\r", "_", "\n", "_")
)

// ServeObject is serveObject: 200 stream (Range → 206) or 302 to a 5-minute signed URL (OPM_BLOB_REDIRECT=1).
func ServeObject(path string, o ServeOpts) (*cloud.Reply, error) {
	size, err := SizeOf(path)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, cloud.E.NotFound("the content is not stored")
	}
	headers := map[string]string{}
	for k, v := range o.Headers {
		headers[k] = v
	}
	headers["Accept-Ranges"] = "bytes"
	if o.FileName != "" {
		headers["Content-Disposition"] = `attachment; filename="` + fileNameBad.Replace(o.FileName) + `"; filename*=UTF-8''` + doc.EncodeURIComponent(o.FileName)
	}
	if ServeMode(size, cloud.Env.BlobRedirect(), o.NoRedirect, o.StreamMax) == "redirect" {
		url, err := cloud.Blobs().SignedReadURL(path, 5*time.Minute, headers["Content-Disposition"])
		if err != nil {
			return nil, err
		}
		headers["Location"] = url
		headers["Cache-Control"] = "no-store"
		return &cloud.Reply{Status: 302, Headers: headers}, nil
	}
	headers["Content-Type"] = "application/octet-stream"
	if o.ContentType != "" {
		headers["Content-Type"] = o.ContentType
	}
	start, end := 0.0, float64(size-1)
	status := 200
	if o.Range != "" {
		m := reRange.FindStringSubmatch(doc.JSTrim(o.Range))
		ok := m != nil && !(m[1] == "" && m[2] == "")
		if ok {
			if m[1] == "" {
				n, _ := strconv.ParseFloat(m[2], 64)
				start = float64(size) - n
				if start < 0 {
					start = 0
				}
			} else {
				start, _ = strconv.ParseFloat(m[1], 64)
				if m[2] != "" {
					if e, _ := strconv.ParseFloat(m[2], 64); e < end {
						end = e
					}
				}
			}
			ok = start <= end && start < float64(size)
		}
		if !ok {
			headers["Content-Range"] = "bytes */" + strconv.FormatInt(size, 10)
			headers["Content-Type"] = "text/plain"
			return &cloud.Reply{Status: 416, Headers: headers, Body: "range not satisfiable"}, nil
		}
		status = 206
		headers["Content-Range"] = fmt.Sprintf("bytes %d-%d/%d", int64(start), int64(end), size)
	}
	if size == 0 {
		headers["Content-Length"] = "0"
		return &cloud.Reply{Status: 200, Headers: headers, Body: []byte{}}, nil
	}
	headers["Content-Length"] = strconv.FormatInt(int64(end)-int64(start)+1, 10)
	r, err := cloud.Blobs().Stream(path, &[2]int64{int64(start), int64(end)})
	if err != nil {
		return nil, err
	}
	return &cloud.Reply{Status: status, Headers: headers, Body: r}, nil
}
