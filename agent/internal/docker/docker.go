// Package docker is a minimal Docker Engine API client over HTTP (unix socket or tcp). It covers what the OPM
// driver needs: ping, images (inspect, load, tag), networks, containers (list, inspect, create, start, stop,
// remove, logs, exec, stats).
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Engine API versions (negotiated on the first request, see Negotiate).
const (
	// MinAPIVersion is the floor: Docker 20.10 (user decision, round G). The install script checks the same value
	// (minDockerApi). Every request field the driver sends exists in 1.41; fields newer than 1.41 are gated on the
	// negotiated version (see agent/TEAM.md "Docker floor 20.10").
	MinAPIVersion = "1.41"
	// MaxAPIVersion is the highest version the agent is tested with (Docker 27.x). Every request / response field the
	// driver uses is the same in 1.41 … 1.47 (see agent/TEAM.md, round G docker API); newer daemons are capped to it.
	MaxAPIVersion = "1.47"
)

// ErrAPITooOld is returned when the daemon's highest API version is below MinAPIVersion.
var ErrAPITooOld = errors.New("docker engine API too old")

// Client talks to one Docker daemon.
type Client struct {
	Host string
	http *http.Client
	base string
	mu   sync.Mutex
	api  string // negotiated, e.g. "1.47"; "" until the first successful negotiation
	// calls counts requests (Calls); cache is the event-driven container state cache (cache.go; nil = off)
	calls atomic.Int64
	cache *stateCache
}

// ErrNotFound is returned for 404 answers.
var ErrNotFound = errors.New("not found")

// APIError is a non-2xx daemon answer.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %d %s", e.Status, e.Message) }

// DefaultHost resolves the daemon address: $DOCKER_HOST, then the configured value, then the Colima socket when the
// default socket is missing, then unix:///var/run/docker.sock.
func DefaultHost(configured string) string {
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		return h
	}
	if configured != "" {
		return configured
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return "unix:///var/run/docker.sock"
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".colima", "default", "docker.sock")
		if _, err := os.Stat(p); err == nil {
			return "unix://" + p
		}
	}
	return "unix:///var/run/docker.sock"
}

// New creates a client for host ("unix:///path", "/path" or "tcp://h:p").
func New(host string) *Client {
	c := &Client{Host: host}
	tr := &http.Transport{MaxIdleConns: 20, IdleConnTimeout: 60 * time.Second}
	switch {
	case strings.HasPrefix(host, "unix://") || strings.HasPrefix(host, "/"):
		sock := strings.TrimPrefix(host, "unix://")
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		c.base = "http://docker"
	case strings.HasPrefix(host, "tcp://"):
		c.base = "http://" + strings.TrimPrefix(host, "tcp://")
	default:
		c.base = "http://" + host
	}
	c.http = &http.Client{Transport: tr}
	return c
}

// cmpAPI compares two "major.minor" API versions (-1, 0, 1). Unparsable parts count as 0.
func cmpAPI(a, b string) int {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	for i := 0; i < 2; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// chooseAPI picks the API version for a daemon: min(server max, MaxAPIVersion); a daemon whose max is below
// MinAPIVersion is refused. A daemon whose own minimum is above MaxAPIVersion (a future Docker that dropped 1.47) gets
// its minimum (best effort; untested there).
func chooseAPI(v *Version) (string, error) {
	if v.APIVersion == "" || cmpAPI(v.APIVersion, MinAPIVersion) < 0 {
		have := v.APIVersion
		if have == "" {
			have = "unknown"
		}
		return "", fmt.Errorf("%w: the Docker daemon (Docker %s) speaks Engine API %s; OPM needs Docker 20.10 / API %s or newer; update Docker", ErrAPITooOld, v.Version, have, MinAPIVersion)
	}
	api := v.APIVersion
	if cmpAPI(api, MaxAPIVersion) > 0 {
		api = MaxAPIVersion
	}
	if v.MinAPIVersion != "" && cmpAPI(v.MinAPIVersion, api) > 0 {
		api = v.MinAPIVersion
	}
	return api, nil
}

// Negotiate reads GET /version (unversioned) once and fixes the API version used for every later request. A failure
// (daemon down, too old) is not cached: the next request tries again.
func (c *Client) Negotiate(ctx context.Context) (string, error) {
	c.mu.Lock()
	api := c.api
	c.mu.Unlock()
	if api != "" {
		return api, nil
	}
	v, err := c.Version(ctx)
	if err != nil {
		return "", err
	}
	if api, err = chooseAPI(v); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.api = api
	c.mu.Unlock()
	return api, nil
}

// APIVersion is the negotiated API version ("" before the first successful request).
func (c *Client) APIVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.api
}

func (c *Client) url(api, path string, q url.Values) string {
	u := c.base + path
	if api != "" {
		u = c.base + "/v" + api + path
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, ctype string) (*http.Response, error) {
	api, err := c.Negotiate(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.doAPI(ctx, api, method, path, q, body, ctype)
	if method != "GET" && method != "HEAD" {
		c.afterWrite(path) // success or not: the daemon may have changed something
	}
	return resp, err
}

// afterWrite drops the cache entries a write may have changed (the matching event follows; reads right after the
// write must not see the old state).
func (c *Client) afterWrite(path string) {
	if c.stateCache() == nil {
		return
	}
	switch {
	case strings.HasSuffix(path, "/exec"), strings.HasSuffix(path, "/resize"), strings.HasSuffix(path, "/attach"), strings.HasSuffix(path, "/archive"):
		// exec health probes run every few seconds: they change nothing an inspect shows
	case strings.HasPrefix(path, "/containers/"):
		id := strings.SplitN(strings.TrimPrefix(path, "/containers/"), "/", 2)[0]
		if id == "create" || id == "prune" {
			id = ""
		}
		c.invalidate(id)
	case strings.HasPrefix(path, "/images/"), strings.HasPrefix(path, "/build"):
		c.invalidateImages()
	case strings.HasPrefix(path, "/networks/"):
		if sc := c.stateCache(); sc != nil {
			sc.reset(sc.isLive()) // connect / disconnect: the container is in the body; drop every inspect
		}
	}
}

// DefaultTimeout is the deadline a request gets when the caller's context has none (§21 #112: a hung dockerd never
// freezes a loop). 0 = no default (streams and long operations: events without until, wait, follow logs, image
// load / pull, stats streams). Callers with their own deadline keep it.
func DefaultTimeout(method, path string, q url.Values) time.Duration {
	switch {
	case path == "/events":
		if q.Get("until") != "" {
			return 2 * time.Minute
		}
		return 0
	case strings.HasSuffix(path, "/wait"), strings.HasPrefix(path, "/images/load"), strings.HasPrefix(path, "/images/create"), strings.HasPrefix(path, "/build"):
		return 0
	case strings.HasSuffix(path, "/logs"):
		if f := q.Get("follow"); f == "1" || f == "true" {
			return 0
		}
		return time.Minute
	case strings.HasSuffix(path, "/stats"):
		if s := q.Get("stream"); s == "0" || s == "false" {
			return 10 * time.Second
		}
		return 0
	case strings.HasSuffix(path, "/stop"), strings.HasSuffix(path, "/restart"):
		if t, err := strconv.Atoi(q.Get("t")); err == nil && t >= 0 {
			return time.Duration(t)*time.Second + time.Minute
		}
		return 10 * time.Minute // the container's own StopTimeout (unknown here)
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/start"):
		return 10 * time.Minute
	case path == "/system/df":
		return 2 * time.Minute
	case method == "GET" || method == "HEAD":
		return 15 * time.Second
	default:
		return 2 * time.Minute
	}
}

// cancelBody cancels the request context when the body is closed (a default deadline must outlive doAPI).
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// doAPI sends one request; api "" = unversioned path (GET /version).
func (c *Client) doAPI(ctx context.Context, api, method, path string, q url.Values, body io.Reader, ctype string) (*http.Response, error) {
	var cancel context.CancelFunc
	if _, has := ctx.Deadline(); !has {
		if d := DefaultTimeout(method, path, q); d > 0 {
			ctx, cancel = context.WithTimeout(ctx, d)
		}
	}
	fail := func(err error) (*http.Response, error) {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	c.calls.Add(1)
	req, err := http.NewRequestWithContext(ctx, method, c.url(api, path, q), body)
	if err != nil {
		return fail(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode == 404 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return fail(fmt.Errorf("%w: %s", ErrNotFound, msgOf(b)))
	}
	if resp.StatusCode >= 300 && resp.StatusCode != 304 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		return fail(&APIError{Status: resp.StatusCode, Message: msgOf(b)})
	}
	if cancel != nil {
		resp.Body = cancelBody{resp.Body, cancel}
	}
	return resp, nil
}

// Calls is the number of requests sent to the daemon so far (self-metrics; tests count Docker API load).
func (c *Client) Calls() int64 { return c.calls.Load() }

func msgOf(b []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return m.Message
	}
	return strings.TrimSpace(string(b))
}

func (c *Client) jsonDo(ctx context.Context, method, path string, q url.Values, in, out any) error {
	var body io.Reader
	ct := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ct = bytes.NewReader(b), "application/json"
	}
	resp, err := c.do(ctx, method, path, q, body, ct)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Ping checks the daemon.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, "GET", "/_ping", nil, nil, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Version is GET /version (subset).
type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
}

// Version returns the daemon version (unversioned GET /version, no negotiation needed).
func (c *Client) Version(ctx context.Context) (*Version, error) {
	resp, err := c.doAPI(ctx, "", "GET", "/version", nil, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v Version
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("docker /version: %w", err)
	}
	return &v, nil
}

// ───────────────────────── images

// Image is GET /images/{name}/json (subset).
type Image struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	RootFS   struct {
		Type   string   `json:"Type"`
		Layers []string `json:"Layers"`
	} `json:"RootFS"`
	Architecture string `json:"Architecture"`
	Os           string `json:"Os"`
	Size         int64  `json:"Size"`
}

// ImageInspect returns the image or ErrNotFound.
func (c *Client) ImageInspect(ctx context.Context, ref string) (*Image, error) {
	sc := c.stateCache()
	var gen uint64
	if sc != nil {
		img, hit, g, live := sc.imgGet(ref)
		if hit {
			cp := *img
			return &cp, nil
		}
		if live {
			gen = g
		} else {
			sc = nil
		}
	}
	var im Image
	if err := c.jsonDo(ctx, "GET", "/images/"+ref+"/json", nil, nil, &im); err != nil {
		return nil, err
	}
	if sc != nil {
		cp := im
		sc.imgPut(ref, gen, &cp)
	}
	return &im, nil
}

// ImageLoad streams a docker-archive tar to POST /images/load and returns the loaded image refs.
func (c *Client) ImageLoad(ctx context.Context, archive io.Reader) ([]string, error) {
	resp, err := c.do(ctx, "POST", "/images/load", url.Values{"quiet": {"1"}}, archive, "application/x-tar")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var loaded []string
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return loaded, err
		}
		if m.Error != "" {
			return loaded, fmt.Errorf("docker load: %s", m.Error)
		}
		s := strings.TrimSpace(m.Stream)
		for _, p := range []string{"Loaded image: ", "Loaded image ID: "} {
			if strings.HasPrefix(s, p) {
				loaded = append(loaded, strings.TrimPrefix(s, p))
			}
		}
	}
	return loaded, nil
}

// ImageTag tags an image.
func (c *Client) ImageTag(ctx context.Context, ref, repo, tag string) error {
	return c.jsonDo(ctx, "POST", "/images/"+ref+"/tag", url.Values{"repo": {repo}, "tag": {tag}}, nil, nil)
}

// ImageSummary is an item of GET /images/json.
type ImageSummary struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
}

// ImageList lists images; reference filters by a reference pattern (e.g. "opm/cfg"), "" = all.
func (c *Client) ImageList(ctx context.Context, reference string) ([]ImageSummary, error) {
	q := url.Values{}
	if reference != "" {
		f, _ := json.Marshal(map[string][]string{"reference": {reference}})
		q.Set("filters", string(f))
	}
	var out []ImageSummary
	return out, c.jsonDo(ctx, "GET", "/images/json", q, nil, &out)
}

// ImageRemove removes an image reference (no prune of parents; force false).
func (c *Client) ImageRemove(ctx context.Context, ref string) error {
	return c.jsonDo(ctx, "DELETE", "/images/"+ref, url.Values{"noprune": {"1"}}, nil, nil)
}

// ───────────────────────── networks

// Network is a network summary.
type Network struct {
	ID         string                       `json:"Id"`
	Name       string                       `json:"Name"`
	Labels     map[string]string            `json:"Labels"`
	Containers map[string]map[string]string `json:"Containers"`
}

// NetworkInspect returns a network or ErrNotFound.
func (c *Client) NetworkInspect(ctx context.Context, name string) (*Network, error) {
	var n Network
	if err := c.jsonDo(ctx, "GET", "/networks/"+name, nil, nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// EnsureNetwork creates a bridge network if missing.
func (c *Client) EnsureNetwork(ctx context.Context, name string, labels map[string]string) error {
	if _, err := c.NetworkInspect(ctx, name); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	body := map[string]any{"Name": name, "Driver": "bridge", "Labels": labels}
	if api, _ := c.Negotiate(ctx); cmpAPI(api, "1.44") < 0 {
		body["CheckDuplicate"] = true // deprecated in 1.44 (names are always unique there)
	}
	err := c.jsonDo(ctx, "POST", "/networks/create", nil, body, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 409 {
		return nil
	}
	return err
}

// EndpointSettings is one network endpoint of a container (inspect subset).
type EndpointSettings struct {
	Aliases   []string `json:"Aliases"`
	DNSNames  []string `json:"DNSNames"`
	IPAddress string   `json:"IPAddress"`
	NetworkID string   `json:"NetworkID"`
}

// NetworkConnect attaches a container to a network with aliases (POST /networks/{id}/connect).
func (c *Client) NetworkConnect(ctx context.Context, network, container string, aliases []string) error {
	body := map[string]any{"Container": container, "EndpointConfig": map[string]any{"Aliases": aliases}}
	return c.jsonDo(ctx, "POST", "/networks/"+network+"/connect", nil, body, nil)
}

// NetworkDisconnect detaches a container from a network (POST /networks/{id}/disconnect).
func (c *Client) NetworkDisconnect(ctx context.Context, network, container string, force bool) error {
	return c.jsonDo(ctx, "POST", "/networks/"+network+"/disconnect", nil, map[string]any{"Container": container, "Force": force}, nil)
}

// NetworkRemove deletes a network.
func (c *Client) NetworkRemove(ctx context.Context, name string) error {
	return c.jsonDo(ctx, "DELETE", "/networks/"+name, nil, nil, nil)
}

// ───────────────────────── containers

// PortBinding is a host binding.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// Mount is a HostConfig mount.
type Mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
}

// RestartPolicy of a container.
type RestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount,omitempty"`
}

// HostConfig (subset).
type HostConfig struct {
	Mounts        []Mount                  `json:"Mounts,omitempty"`
	PortBindings  map[string][]PortBinding `json:"PortBindings,omitempty"`
	RestartPolicy RestartPolicy            `json:"RestartPolicy"`
	NanoCpus      int64                    `json:"NanoCpus,omitempty"`
	Memory        int64                    `json:"Memory,omitempty"`
	ExtraHosts    []string                 `json:"ExtraHosts,omitempty"`
	NetworkMode   string                   `json:"NetworkMode,omitempty"`
}

// ContainerConfig is the POST /containers/create body.
type ContainerConfig struct {
	Image            string              `json:"Image"`
	Entrypoint       []string            `json:"Entrypoint,omitempty"`
	Cmd              []string            `json:"Cmd,omitempty"`
	Env              []string            `json:"Env,omitempty"`
	WorkingDir       string              `json:"WorkingDir,omitempty"`
	User             string              `json:"User,omitempty"`
	Labels           map[string]string   `json:"Labels,omitempty"`
	ExposedPorts     map[string]struct{} `json:"ExposedPorts,omitempty"`
	StopTimeout      *int                `json:"StopTimeout,omitempty"`
	HostConfig       HostConfig          `json:"HostConfig"`
	NetworkingConfig *NetworkingConfig   `json:"NetworkingConfig,omitempty"`
}

// NetworkingConfig attaches the container to a network with aliases.
type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointConfig `json:"EndpointsConfig"`
}

// EndpointConfig of one network.
type EndpointConfig struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// ContainerSummary is an item of GET /containers/json.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	// NetworkSettings.Networks names the networks of the container (list filter before any inspect, §21 #112)
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

// ContainerList lists containers (all=true includes stopped) matching label filters ("k=v" or "k").
func (c *Client) ContainerList(ctx context.Context, all bool, labels ...string) ([]ContainerSummary, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	if len(labels) > 0 {
		f, _ := json.Marshal(map[string][]string{"label": labels})
		q.Set("filters", string(f))
	}
	sc := c.stateCache()
	key := listKey(all, labels)
	var gen uint64
	if sc != nil {
		list, hit, g, live := sc.listGet(key)
		if hit {
			return list, nil
		}
		if live {
			gen = g
		} else {
			sc = nil
		}
	}
	var out []ContainerSummary
	if err := c.jsonDo(ctx, "GET", "/containers/json", q, nil, &out); err != nil {
		return out, err
	}
	if sc != nil {
		sc.listPut(key, gen, out)
	}
	return out, nil
}

// ContainerNamed returns the container whose docker name is exactly name (nil = none). It never uses
// GET /containers/{name}/json: Docker resolves that path by name OR by id prefix, so a name such as "cafe" could match
// an unrelated container id.
func (c *Client) ContainerNamed(ctx context.Context, name string) (*ContainerSummary, error) {
	list, err := c.ContainerList(ctx, true)
	if err != nil {
		return nil, err
	}
	return FindNamed(list, name), nil
}

// FindNamed returns the container of list whose docker name is exactly name (nil = none).
func FindNamed(list []ContainerSummary, name string) *ContainerSummary {
	for i := range list {
		for _, n := range list[i].Names {
			if strings.TrimPrefix(n, "/") == name {
				return &list[i]
			}
		}
	}
	return nil
}

// ContainerState is ContainerJSON.State.
type ContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

// ContainerJSON is GET /containers/{id}/json (subset).
type ContainerJSON struct {
	ID           string         `json:"Id"`
	Name         string         `json:"Name"`
	Image        string         `json:"Image"`
	RestartCount int            `json:"RestartCount"`
	State        ContainerState `json:"State"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
		Image  string            `json:"Image"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]PortBinding `json:"Ports"`
		// Networks: endpoint per network (§21 #93 alias self-check). Aliases = the user aliases (API < 1.45 also lists the
		// short id); DNSNames (API ≥ 1.44) = every name the embedded DNS answers for (name, aliases, short id).
		Networks map[string]EndpointSettings `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

// ContainerInspect returns a container or ErrNotFound.
func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerJSON, error) {
	sc := c.stateCache()
	var gen uint64
	if sc != nil {
		v, hit, g, live := sc.inspGet(id)
		if hit && v.cj != nil {
			return copyJSON(v.cj), nil
		}
		if live {
			gen = g
		} else {
			sc = nil
		}
	}
	var cj ContainerJSON
	if err := c.jsonDo(ctx, "GET", "/containers/"+id+"/json", nil, nil, &cj); err != nil {
		return nil, err
	}
	if sc != nil {
		sc.inspPut(id, gen, copyJSON(&cj), nil)
	}
	return &cj, nil
}

// ContainerCreate creates a container and returns its id.
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg *ContainerConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.jsonDo(ctx, "POST", "/containers/create", url.Values{"name": {name}}, cfg, &out)
	return out.ID, err
}

// ContainerStart starts a container (already running = ok).
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.jsonDo(ctx, "POST", "/containers/"+id+"/start", nil, nil, nil)
}

// ContainerStop stops a container with a timeout in seconds (SIGTERM, then SIGKILL).
// timeoutSec < 0 = the container's own StopTimeout (set at create from the image stopTimeoutSec).
func (c *Client) ContainerStop(ctx context.Context, id string, timeoutSec int) error {
	q := url.Values{}
	if timeoutSec >= 0 {
		q.Set("t", strconv.Itoa(timeoutSec))
	}
	return c.jsonDo(ctx, "POST", "/containers/"+id+"/stop", q, nil, nil)
}

// ContainerRename renames a container (POST /containers/{id}/rename?name=).
func (c *Client) ContainerRename(ctx context.Context, id, name string) error {
	return c.jsonDo(ctx, "POST", "/containers/"+id+"/rename", url.Values{"name": {name}}, nil, nil)
}

// ContainerRemove removes a container (force kills it first).
func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	err := c.jsonDo(ctx, "DELETE", "/containers/"+id, q, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ContainerWait waits for a stop condition ("not-running").
func (c *Client) ContainerWait(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
	}
	err := c.jsonDo(ctx, "POST", "/containers/"+id+"/wait", url.Values{"condition": {"not-running"}}, nil, &out)
	return out.StatusCode, err
}

// LogLine is one demultiplexed log line.
type LogLine struct {
	Stream string // stdout | stderr
	Ts     string
	Text   string
}

// ContainerLogs reads logs (timestamps on). tail <= 0 = all. since = unix seconds string or "".
func (c *Client) ContainerLogs(ctx context.Context, id string, tail int, since string) ([]LogLine, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	} else {
		q.Set("tail", "all")
	}
	if since != "" {
		q.Set("since", since)
	}
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/logs", q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	return parseLogs(raw), nil
}

func parseLogs(raw []byte) []LogLine {
	var out []LogLine
	add := func(stream string, chunk []byte) {
		for _, ln := range strings.Split(strings.TrimRight(string(chunk), "\n"), "\n") {
			if ln == "" {
				continue
			}
			ts, text, ok := strings.Cut(ln, " ")
			if !ok {
				ts, text = "", ln
			}
			out = append(out, LogLine{Stream: stream, Ts: ts, Text: strings.TrimRight(text, "\r")})
		}
	}
	// multiplexed stream: 8-byte header [stream, 0, 0, 0, size(4)]
	if len(raw) >= 8 && (raw[0] == 1 || raw[0] == 2) && raw[1] == 0 && raw[2] == 0 && raw[3] == 0 {
		for len(raw) >= 8 {
			st := "stdout"
			if raw[0] == 2 {
				st = "stderr"
			}
			n := int(binary.BigEndian.Uint32(raw[4:8]))
			raw = raw[8:]
			if n > len(raw) {
				n = len(raw)
			}
			add(st, raw[:n])
			raw = raw[n:]
		}
		return out
	}
	add("stdout", raw)
	return out
}

// Exec runs cmd in a running container and returns the exit code and combined output.
func (c *Client) Exec(ctx context.Context, id string, cmd []string) (int, string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	if err := c.jsonDo(ctx, "POST", "/containers/"+id+"/exec", nil, map[string]any{"Cmd": cmd, "AttachStdout": true, "AttachStderr": true}, &created); err != nil {
		return -1, "", err
	}
	resp, err := c.do(ctx, "POST", "/exec/"+created.ID+"/start", nil, bytes.NewReader([]byte(`{"Detach":false,"Tty":false}`)), "application/json")
	if err != nil {
		return -1, "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var sb strings.Builder
	for _, l := range parseLogs(raw) {
		sb.WriteString(l.Ts)
		if l.Text != "" {
			sb.WriteString(" " + l.Text)
		}
		sb.WriteString("\n")
	}
	var ins struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	for i := 0; i < 50; i++ {
		if err := c.jsonDo(ctx, "GET", "/exec/"+created.ID+"/json", nil, nil, &ins); err != nil {
			return -1, sb.String(), err
		}
		if !ins.Running {
			break
		}
		select {
		case <-ctx.Done():
			return -1, sb.String(), ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ins.ExitCode, sb.String(), nil
}

// Stats is a one-shot CPU / memory sample.
type Stats struct {
	CPUPct float64
	MemMb  float64
}

// ContainerStats samples stats once (stream=false).
func (c *Client) ContainerStats(ctx context.Context, id string) (*Stats, error) {
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/stats", url.Values{"stream": {"0"}}, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(bufio.NewReader(resp.Body)).Decode(&s); err != nil {
		return nil, err
	}
	out := &Stats{}
	cd := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sd := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if sd > 0 && cd > 0 {
		n := s.CPUStats.OnlineCPUs
		if n == 0 {
			n = 1
		}
		out.CPUPct = cd / sd * float64(n) * 100
	}
	mem := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < mem {
		mem -= v
	}
	out.MemMb = float64(mem) / (1024 * 1024)
	return out, nil
}

// Info is the part of GET /info the agent reads (§21 #119 storage view).
type Info struct {
	DockerRootDir string `json:"DockerRootDir"`
	Images        int    `json:"Images"`
	Containers    int    `json:"Containers"`
}

// Info returns the daemon's data root and counts.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var v Info
	if err := c.jsonDo(ctx, "GET", "/info", nil, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
