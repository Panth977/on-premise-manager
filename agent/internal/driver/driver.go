package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/model"
)

// Existing is a running or stopped OPM container of this deployment + site.
type Existing struct {
	ID            string
	Name          string
	ContainerSlug string // label opm.container (legacy containers without it: opm.service)
	Service       string // label opm.service
	Slug          string // = ContainerSlug
	Hash          string
	RevID         string
	RevSeq        int
	State         string
	Running       bool
}

// Action kinds (§15.3).
const (
	ActKeep     = "keep"
	ActRecreate = "recreate"
	ActCreate   = "create"
	ActRemove   = "remove"
	ActStop     = "stop"
)

// Action is one container action of an APPLY.
type Action struct {
	ContainerSlug string
	Slug          string
	Kind          string
	Built         *Built
	Existing      *Existing
}

// Health is the latest readiness of a container (opm_ready + status page).
type Health struct {
	ContainerID string
	Ready       bool
	Health      string // healthy | unhealthy | starting | none
	LastError   string
	LastCheck   time.Time
	LoopPorts   map[string]int // portName → loopback host port
	Restarts    int
}

// Driver manages the OPM containers of one site.
type Driver struct {
	DC              *docker.Client
	Dep, Site       string
	DataDir         string
	Log             *dlog.Logger
	CompleteTimeout time.Duration
	// NoneReadyAfter: a container without a health check is ready after running this long without a restart (10 s;
	// tests shorten it)
	NoneReadyAfter time.Duration
	// Names: the docker naming mode (agent.json containerNames; "" = plain). Managed containers are always FOUND by
	// their labels (opm.deployment + opm.site + opm.container), never by name.
	Names string
	// Adopted (§21 #104): docker ids of hand-made containers a drift commit made part of the config; CreateStart may
	// replace them like a stale container of this site (nil = none).
	Adopted func(id string) bool

	mu     sync.Mutex
	health map[string]*Health
	hist   map[string][]CheckResult // containerSlug → last health checks of the current WaitCompleted (newest last)
}

// New creates a driver.
func New(dc *docker.Client, dep, site, dataDir string, log *dlog.Logger, completeTimeout time.Duration) *Driver {
	if completeTimeout <= 0 {
		completeTimeout = 300 * time.Second
	}
	return &Driver{DC: dc, Dep: dep, Site: site, DataDir: dataDir, Log: log, CompleteTimeout: completeTimeout, health: map[string]*Health{}, hist: map[string][]CheckResult{}}
}

// Existing lists the OPM containers of this deployment + site by containerSlug.
func (d *Driver) Existing(ctx context.Context) (map[string]Existing, error) {
	list, err := d.DC.ContainerList(ctx, true, LDeployment+"="+d.Dep, LSite+"="+d.Site)
	if err != nil {
		return nil, err
	}
	out := map[string]Existing{}
	for _, c := range list {
		sid := c.Labels[LContainer]
		if sid == "" {
			sid = c.Labels[LService] // pre-round-C container (containerSlug = serviceSlug)
		}
		if sid == "" {
			continue
		}
		if len(c.Names) > 0 && IsFailedName(strings.TrimPrefix(c.Names[0], "/")) {
			continue // a kept failed container (§21 #87): not part of any plan
		}
		seq, _ := strconv.Atoi(c.Labels[LRevSeq])
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		e := Existing{ID: c.ID, Name: name, ContainerSlug: sid, Service: c.Labels[LService], Slug: sid, Hash: c.Labels[LSpecHash], RevID: c.Labels[LRev], RevSeq: seq, State: c.State, Running: c.State == "running" || c.State == "restarting"}
		if prev, dup := out[sid]; dup && prev.Running && !e.Running {
			continue
		}
		out[sid] = e
	}
	return out, nil
}

var failedNameRe = regexp.MustCompile(`-failed-\d+$`)

// IsFailedName: a container kept after a failed update (`<name>-failed-<unix>`, §21 #87).
func IsFailedName(name string) bool { return failedNameRe.MatchString(name) }

// FailedContainers lists the kept failed containers of this site (id → name, created unix from the name).
func (d *Driver) FailedContainers(ctx context.Context) (map[string]string, error) {
	list, err := d.DC.ContainerList(ctx, true, LDeployment+"="+d.Dep, LSite+"="+d.Site)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, c := range list {
		if len(c.Names) > 0 && IsFailedName(strings.TrimPrefix(c.Names[0], "/")) {
			out[c.ID] = strings.TrimPrefix(c.Names[0], "/")
		}
	}
	return out, nil
}

// NameFor is the docker name of a containerSlug in this driver's naming mode.
func (d *Driver) NameFor(slug string) string { return ContainerName(d.Names, d.Dep, d.Site, slug) }

// ownedBySite: the labels say the container is managed by this deployment + site.
func (d *Driver) ownedBySite(labels map[string]string) bool {
	return labels[LDeployment] == d.Dep && labels[LSite] == d.Site
}

// NameClashError is the staging / apply error for a docker name held by a container this site does not manage.
func NameClashError(name string, c *docker.ContainerSummary) error {
	id := c.ID
	if len(id) > 12 {
		id = id[:12]
	}
	who := ""
	if dep, site := c.Labels[LDeployment], c.Labels[LSite]; dep != "" {
		who = fmt.Sprintf(", managed by OPM deployment %s site %s", dep, site)
	}
	return fmt.Errorf("Docker container name `%s` is already used by a container not managed by this site (id %s, image %s%s) — remove or rename it, or set containerNames: prefixed in agent.json", name, id, c.Image, who)
}

// CheckNames is the staging check (§9.5 dry run) of the docker names of the enabled builts: a name held by a container
// that is not labelled for this deployment + site fails staging. Containers of this site holding a name (a stale
// container, or the container itself) are fine.
func (d *Driver) CheckNames(ctx context.Context, builts []*Built) error {
	list, err := d.DC.ContainerList(ctx, true)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	var errs []string
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		if c := docker.FindNamed(list, b.Name); c != nil && !d.ownedBySite(c.Labels) && (d.Adopted == nil || !d.Adopted(c.ID)) {
			errs = append(errs, NameClashError(b.Name, c).Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Renamed is one rename of the naming migration.
type Renamed struct {
	ID, From, To string
}

// MigrateNames renames every managed container of this site (found by labels; kept -failed- copies excluded) whose
// docker name is not the name of the current mode, in place (POST /containers/{id}/rename): no recreate, the container
// id, state and spec hash stay. A name held by another container is left alone (skipped; staging reports the clash).
func (d *Driver) MigrateNames(ctx context.Context) (done []Renamed, skipped []string, err error) {
	ex, err := d.Existing(ctx)
	if err != nil {
		return nil, nil, err
	}
	var list []docker.ContainerSummary
	for _, e := range ex {
		want := d.NameFor(e.ContainerSlug)
		if e.Name == want {
			continue
		}
		if list == nil {
			if list, err = d.DC.ContainerList(ctx, true); err != nil {
				return done, skipped, err
			}
		}
		if h := docker.FindNamed(list, want); h != nil && h.ID != e.ID {
			skipped = append(skipped, want)
			d.Log.Warn("driver", "container not renamed: the name is taken", "container", e.ContainerSlug, "from", e.Name, "to", want, "holder", h.ID[:12], "holderImage", h.Image)
			continue
		}
		if err := d.DC.ContainerRename(ctx, e.ID, want); err != nil {
			skipped = append(skipped, want)
			d.Log.Warn("driver", "container rename failed", "container", e.ContainerSlug, "from", e.Name, "to", want, "error", err.Error())
			continue
		}
		done = append(done, Renamed{ID: e.ID, From: e.Name, To: want})
		d.Log.Info("driver", "container renamed", "container", e.ContainerSlug, "id", e.ID[:12], "from", e.Name, "to", want)
	}
	return done, skipped, nil
}

// Keeps reports whether the existing container is kept as is (§15.3): the same spec hash and running, or - for a job -
// the same spec hash and exited (the job has run; it is not re-run until its spec changes). Plan and the merge preview
// (runtime actions) both use it, so the preview never shows "recreate" for a container the apply keeps.
func Keeps(b *Built, ex Existing) bool {
	return ex.Hash == b.Hash && (ex.Running || (b.Kind == "job" && ex.State == "exited")) && !SiteFilesModeStale(b)
}

// SiteFilesModeStale reports whether a file the container mounts is on the host with another mode than the one it
// should have (0400 secret in clear / 0444). The mode is not in the spec hash, and a file is (re)written only when its
// container is created: a change of the mode alone (e.g. a secret reference replaced by a variable with the same value
// → the same bytes, 0400 → 0444) would otherwise keep the container with the old file (nginx: 403). A missing file is
// not stale here (it is written at create).
func SiteFilesModeStale(b *Built) bool {
	for p, sf := range b.SiteFiles {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm() != sf.Mode.Perm() {
			return true
		}
	}
	return false
}

// Plan computes the recreate rule (§15.3) for the target containers (builts of the site, enabled or not).
func Plan(builts []*Built, existing map[string]Existing) []Action {
	var out []Action
	want := map[string]bool{}
	for _, b := range builts {
		if !b.Enabled {
			continue
		}
		want[b.ContainerSlug] = true
		ex, ok := existing[b.ContainerSlug]
		switch {
		case !ok:
			out = append(out, Action{ContainerSlug: b.ContainerSlug, Slug: b.Slug, Kind: ActCreate, Built: b})
		case Keeps(b, ex):
			e := ex
			out = append(out, Action{ContainerSlug: b.ContainerSlug, Slug: b.Slug, Kind: ActKeep, Built: b, Existing: &e})
		default:
			e := ex
			out = append(out, Action{ContainerSlug: b.ContainerSlug, Slug: b.Slug, Kind: ActRecreate, Built: b, Existing: &e})
		}
	}
	for sid, ex := range existing {
		if !want[sid] {
			e := ex
			out = append(out, Action{ContainerSlug: sid, Slug: ex.Slug, Kind: ActRemove, Existing: &e})
		}
	}
	return out
}

// StopRemove stops (SIGTERM, timeout, SIGKILL) and removes a container. Data stays on disk.
func (d *Driver) StopRemove(ctx context.Context, id string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	if err := d.DC.ContainerStop(ctx, id, timeoutSec); err != nil && !errors.Is(err, docker.ErrNotFound) {
		var ae *docker.APIError
		if !(errors.As(err, &ae) && ae.Status == 304) {
			d.Log.Warn("driver", "stop failed; removing with force", "id", id[:12], "error", err.Error())
		}
	}
	return d.DC.ContainerRemove(ctx, id, true)
}

// Stop stops a container without removing it. timeoutSec < 0 = the container's own stop timeout (image
// stopTimeoutSec, set at create); 0 = 10 s.
func (d *Driver) Stop(ctx context.Context, id string, timeoutSec int) error {
	if timeoutSec == 0 {
		timeoutSec = 10
	}
	err := d.DC.ContainerStop(ctx, id, timeoutSec)
	var ae *docker.APIError
	if errors.As(err, &ae) && ae.Status == 304 {
		return nil
	}
	return err
}

// CreateStart prepares the host side, creates and starts the container; returns its id.
func (d *Driver) CreateStart(ctx context.Context, b *Built, revID string, revSeq int) (string, error) {
	if err := b.PrepareHost(d.DataDir, revID); err != nil {
		return "", err
	}
	if err := d.DC.EnsureNetwork(ctx, b.Spec.Network, map[string]string{LDeployment: d.Dep, LSite: d.Site}); err != nil {
		return "", fmt.Errorf("network: %w", err)
	}
	// the docker name must be free, or held by a stale container of THIS site (removed); anything else is never touched
	if old, err := d.DC.ContainerNamed(ctx, b.Name); err != nil {
		return "", fmt.Errorf("docker: %w", err)
	} else if old != nil {
		if !d.ownedBySite(old.Labels) && (d.Adopted == nil || !d.Adopted(old.ID)) {
			return "", NameClashError(b.Name, old)
		}
		_ = d.StopRemove(ctx, old.ID, b.Spec.StopTimeoutSec)
	}
	// the container is created from the readable reference, only after checking that it points to the verified image
	if retagged, err := EnsureReadable(ctx, d.DC, b.Image); err != nil {
		return "", fmt.Errorf("image %s: %w", b.Readable, err)
	} else if retagged {
		d.Log.Info("driver", "image re-tagged", "image", b.Readable, "config", ImageRef(b.Image.ConfigDigest))
	}
	id, err := d.DC.ContainerCreate(ctx, b.Name, b.CreateConfig(revID, revSeq))
	if err != nil {
		return "", fmt.Errorf("create %s: %w", b.Slug, err)
	}
	if err := d.DC.ContainerStart(ctx, id); err != nil {
		return id, fmt.Errorf("start %s: %w", b.Slug, err)
	}
	d.mu.Lock()
	d.health[b.ContainerSlug] = &Health{ContainerID: id, Health: "starting", LoopPorts: map[string]int{}}
	d.mu.Unlock()
	d.Log.Info("driver", "container started", "container", b.Slug, "id", id[:12], "specHash", b.Hash[:12])
	return id, nil
}

// LoopPorts reads the host ports the agent reaches a container on (portName → port), always fresh from docker inspect:
// the 127.0.0.1 publication, else a publication on every address (0.0.0.0 / "" / "::"), reached via 127.0.0.1.
// A container that Docker is restarting has NO port map (NetworkSettings.Ports = {}): the map is then empty.
func (d *Driver) LoopPorts(ctx context.Context, id string, b *Built) (map[string]int, *docker.ContainerJSON, error) {
	cj, err := d.DC.ContainerInspect(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return LoopPortsOf(cj, b), cj, nil
}

// LoopPortsOf maps the inspected publications of b's ports (see LoopPorts).
func LoopPortsOf(cj *docker.ContainerJSON, b *Built) map[string]int {
	out := map[string]int{}
	for _, p := range b.Ports {
		fallback := 0
		for _, pb := range cj.NetworkSettings.Ports[strconv.Itoa(p.ContainerPort)+"/"+p.Protocol] {
			n, _ := strconv.Atoi(pb.HostPort)
			if n <= 0 {
				continue
			}
			if pb.HostIP == "127.0.0.1" {
				out[p.Name] = n
				fallback = 0
				break
			}
			if fallback == 0 && (pb.HostIP == "0.0.0.0" || pb.HostIP == "" || pb.HostIP == "::") {
				fallback = n
			}
		}
		if _, ok := out[p.Name]; !ok && fallback > 0 {
			out[p.Name] = fallback
		}
	}
	return out
}

// PortMapText is the inspected port map for error messages ("8080/tcp=0.0.0.0:6007,127.0.0.1:32768"; "none").
func PortMapText(cj *docker.ContainerJSON) string {
	if cj == nil || len(cj.NetworkSettings.Ports) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(cj.NetworkSettings.Ports))
	for k := range cj.NetworkSettings.Ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		var bs []string
		for _, pb := range cj.NetworkSettings.Ports[k] {
			bs = append(bs, pb.HostIP+":"+pb.HostPort)
		}
		if len(bs) == 0 {
			bs = []string{"unpublished"}
		}
		parts = append(parts, k+"="+strings.Join(bs, ","))
	}
	return strings.Join(parts, " ")
}

// CheckResult is one readiness / health check of a container (update failure diagnostics, §21 #87).
type CheckResult struct {
	At      time.Time
	OK      bool
	Counted bool // false = inside startPeriod (or a not-ready-yet state): it does not count towards retries
	Message string
}

const histMax = 30

func (d *Driver) record(slug string, r CheckResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := append(d.hist[slug], r)
	if len(h) > histMax {
		h = h[len(h)-histMax:]
	}
	d.hist[slug] = h
}

// History returns the health checks of the last WaitCompleted of a container (oldest first).
func (d *Driver) History(slug string) []CheckResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]CheckResult(nil), d.hist[slug]...)
}

// healthPortName: the port an http / tcp check needs ("" for exec / log).
func healthPortName(h *model.HealthCheck) string {
	if h != nil && (h.Type == "http" || h.Type == "tcp") {
		return h.PortName
	}
	return ""
}

// HealthOf returns a copy of the latest readiness.
func (d *Driver) HealthOf(containerSlug string) Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := d.health[containerSlug]
	if h == nil {
		return Health{Health: "none"}
	}
	c := *h
	c.LoopPorts = map[string]int{}
	for k, v := range h.LoopPorts {
		c.LoopPorts[k] = v
	}
	return c
}

// SetHealth stores readiness (used by the monitor).
func (d *Driver) SetHealth(containerSlug string, h Health) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.health[containerSlug] = &h
}

// ForgetHealth drops a removed container.
func (d *Driver) ForgetHealth(containerSlug string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.health, containerSlug)
}

// Ready reports the latest readiness.
func (d *Driver) Ready(containerSlug string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := d.health[containerSlug]
	return h != nil && h.Ready
}

// CheckOnce runs the image health check once. nil = pass.
func (d *Driver) CheckOnce(ctx context.Context, b *Built, id string, loop map[string]int, since time.Time) error {
	h := b.Health
	if h == nil {
		return nil
	}
	timeout := Secs(h.TimeoutSec)
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch h.Type {
	case "http":
		port := loop[h.PortName]
		if port == 0 {
			return fmt.Errorf("port %s has no host publication (container restarting or port not published)", h.PortName)
		}
		req, _ := http.NewRequestWithContext(cctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", port, h.Path), nil)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		want := int(h.ExpectStatus)
		if want == 0 {
			want = 200
		}
		if resp.StatusCode != want {
			return fmt.Errorf("GET %s → %d (want %d)", h.Path, resp.StatusCode, want)
		}
		return nil
	case "tcp":
		port := loop[h.PortName]
		if port == 0 {
			return fmt.Errorf("port %s has no host publication (container restarting or port not published)", h.PortName)
		}
		var dl net.Dialer
		c, err := dl.DialContext(cctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return err
		}
		c.Close()
		return nil
	case "exec":
		code, out, err := d.DC.Exec(cctx, id, h.Command)
		if err != nil {
			return err
		}
		if code != int(h.ExpectExitCode) {
			return fmt.Errorf("exec %s → exit %d (want %d): %s", strings.Join(h.Command, " "), code, int(h.ExpectExitCode), trimOut(out))
		}
		return nil
	case "log":
		re, err := regexp.Compile(h.Pattern)
		if err != nil {
			return err
		}
		lines, err := d.DC.ContainerLogs(cctx, id, 0, strconv.FormatInt(since.Unix(), 10))
		if err != nil {
			return err
		}
		for _, l := range lines {
			if re.MatchString(l.Text) {
				return nil
			}
		}
		return fmt.Errorf("log pattern %q not seen yet", h.Pattern)
	}
	return fmt.Errorf("unknown health check type %s", h.Type)
}

func trimOut(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// CompleteLimit (§15.4): how long a container may take to become completed — the agent limit (300 s), or longer when
// the health check's own start period needs it: startPeriodSec + retries × (intervalSec + timeoutSec). Without this a
// start period above ~300 s (a first dependency install, a Postgres standby clone) could never be used: the container
// failed "not completed within 300 s" while its check was still inside the start period.
func CompleteLimit(base time.Duration, h *model.HealthCheck) time.Duration {
	if h == nil || h.Type == "" || h.Type == "none" {
		return base
	}
	retries := h.Retries
	if retries <= 0 {
		retries = 3
	}
	need := Secs(h.StartPeriodSec) + time.Duration(retries)*(Secs(h.IntervalSec)+Secs(h.TimeoutSec))
	if need > base {
		return need
	}
	return base
}

// ErrUnhealthy marks a fail-fast health failure.
var ErrUnhealthy = errors.New("unhealthy")

// WaitCompleted waits until the container is "completed" (§15.4).
func (d *Driver) WaitCompleted(ctx context.Context, b *Built, id string) error {
	start := time.Now()
	limit := CompleteLimit(d.CompleteTimeout, b.Health)
	deadline := start.Add(limit)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	timeoutErr := func(last string) error {
		if last != "" {
			return fmt.Errorf("%s: not completed within %d s (last: %s)", b.Slug, int(limit.Seconds()), last)
		}
		return fmt.Errorf("%s: not completed within %d s", b.Slug, int(limit.Seconds()))
	}
	if b.Kind == "job" {
		code, err := d.DC.ContainerWait(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return timeoutErr("")
			}
			return err
		}
		if code != 0 {
			return fmt.Errorf("%s: job exited with code %d", b.Slug, code)
		}
		d.SetHealth(b.ContainerSlug, Health{ContainerID: id, Ready: true, Health: "none", LastCheck: time.Now(), LoopPorts: map[string]int{}})
		return nil
	}
	h := b.Health
	if h != nil && (h.Type == "" || h.Type == "none") {
		h = nil // type none: no check; ready after 10 s running without a restart
	}
	interval := time.Second
	if h != nil && h.IntervalSec > 0 {
		interval = Secs(h.IntervalSec)
	}
	retries := 3
	startPeriod := 30 * time.Second
	if h != nil {
		if h.Retries > 0 {
			retries = int(h.Retries)
		}
		startPeriod = Secs(h.StartPeriodSec)
	}
	d.mu.Lock()
	d.hist[b.ContainerSlug] = nil
	d.mu.Unlock()
	lastRestarts := -1
	runningSince := time.Now()
	fails := 0
	last := ""
	for {
		loop, cj, err := d.LoopPorts(ctx, id, b)
		if err != nil {
			if ctx.Err() != nil {
				return timeoutErr(last)
			}
			return fmt.Errorf("%s: inspect: %w", b.Slug, err)
		}
		if lastRestarts >= 0 && cj.RestartCount != lastRestarts {
			runningSince = time.Now()
		}
		lastRestarts = cj.RestartCount
		inStart := time.Since(start) < startPeriod
		hs := &Health{ContainerID: id, LoopPorts: loop, Restarts: cj.RestartCount, LastCheck: time.Now(), Health: "starting"}
		var cerr error
		counted := !inStart
		switch {
		case !cj.State.Running || cj.State.Restarting:
			// Docker reports Running=true + Restarting=true and an EMPTY port map while it waits to restart a crashed
			// container (restart policy backoff): that is a crash loop, not a port problem.
			if !cj.State.Restarting && (b.Spec.Restart == "no" || (b.Spec.Restart == "on-failure" && cj.State.ExitCode == 0)) {
				msg := fmt.Sprintf("exited with code %d%s", cj.State.ExitCode, oomText(cj))
				d.record(b.ContainerSlug, CheckResult{At: time.Now(), Message: msg, Counted: true})
				return fmt.Errorf("%s: %w: %s", b.Slug, ErrUnhealthy, msg)
			}
			cerr = fmt.Errorf("container is restarting: last exit code %d%s, %d restarts", cj.State.ExitCode, oomText(cj), cj.RestartCount)
			runningSince = time.Now()
		case h == nil:
			if time.Since(runningSince) >= d.noneReady() {
				hs.Ready, hs.Health = true, "none"
				d.record(b.ContainerSlug, CheckResult{At: time.Now(), OK: true, Message: "running 10 s without a restart"})
				d.SetHealth(b.ContainerSlug, *hs)
				return nil
			}
			cerr, counted = errors.New("running; ready after 10 s without a restart"), false
		default:
			if pn := healthPortName(h); pn != "" && loop[pn] == 0 {
				cerr = fmt.Errorf("port %s has no host publication yet (docker ports: %s)", pn, PortMapText(cj))
			} else {
				cerr = d.CheckOnce(ctx, b, id, loop, start)
			}
			if cerr == nil {
				hs.Ready, hs.Health = true, "healthy"
				d.record(b.ContainerSlug, CheckResult{At: time.Now(), OK: true, Message: "check passed", Counted: counted})
				d.SetHealth(b.ContainerSlug, *hs)
				d.Log.Debug("health", "container completed", "container", b.Slug, "after", time.Since(start).String())
				return nil
			}
		}
		last = cerr.Error()
		hs.LastError = last
		d.record(b.ContainerSlug, CheckResult{At: time.Now(), Message: last, Counted: counted && h != nil})
		if counted && h != nil {
			fails++
			if fails >= retries {
				hs.Health = "unhealthy"
				d.SetHealth(b.ContainerSlug, *hs)
				return fmt.Errorf("%s: %w: %d checks failed in a row after the %d s start period: %s", b.Slug, ErrUnhealthy, fails, int(startPeriod.Seconds()), last)
			}
		}
		d.SetHealth(b.ContainerSlug, *hs)
		select {
		case <-ctx.Done():
			return timeoutErr(last)
		case <-time.After(interval):
		}
	}
}

// Secs converts image seconds (may be fractional) to a duration.
func Secs(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func (d *Driver) noneReady() time.Duration {
	if d.NoneReadyAfter > 0 {
		return d.NoneReadyAfter
	}
	return 10 * time.Second
}

func oomText(cj *docker.ContainerJSON) string {
	if cj.State.OOMKilled {
		return " (OOM killed)"
	}
	return ""
}

// IsRunning reports whether the container is running now.
func (d *Driver) IsRunning(ctx context.Context, id string) (bool, int, error) {
	cj, err := d.DC.ContainerInspect(ctx, id)
	if err != nil {
		return false, 0, err
	}
	return cj.State.Running, cj.State.ExitCode, nil
}

// PortFree reports whether a host port can be bound (tcp or udp) on all interfaces.
func PortFree(port int, proto string) bool {
	addr := ":" + strconv.Itoa(port)
	if proto == "udp" {
		c, err := net.ListenPacket("udp", addr)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	return true
}
