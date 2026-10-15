package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RegistryAuth is the X-Registry-Auth body of a pull (§21 #113: a site image from a private registry).
type RegistryAuth struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

// SplitReference splits "host/repo:tag" / "repo@sha256:…" into fromImage + tag (tag may be a digest; "" = latest).
func SplitReference(ref string) (image, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// ImagePull pulls a reference through the daemon (POST /images/create) and waits for the end of the progress stream.
// progress (optional) gets each status line. platform "" = the daemon's.
func (c *Client) ImagePull(ctx context.Context, ref, platform string, auth *RegistryAuth, progress func(string)) error {
	api, err := c.Negotiate(ctx)
	if err != nil {
		return err
	}
	image, tag := SplitReference(ref)
	q := url.Values{"fromImage": {image}, "tag": {tag}}
	if platform != "" {
		q.Set("platform", platform)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url(api, "/images/create", q), nil)
	if err != nil {
		return err
	}
	if auth != nil {
		b, _ := json.Marshal(auth)
		req.Header.Set("X-Registry-Auth", base64.URLEncoding.EncodeToString(b))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%w: %s", ErrNotFound, msgOf(b))
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return &APIError{Status: resp.StatusCode, Message: msgOf(b)}
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Status      string `json:"status"`
			ID          string `json:"id"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("docker pull %s: %s", ref, m.Error)
		}
		if progress != nil && m.Status != "" {
			if m.ID != "" {
				progress(m.ID + ": " + m.Status)
			} else {
				progress(m.Status)
			}
		}
	}
}
