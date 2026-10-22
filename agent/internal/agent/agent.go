// Package agent is the OPM site agent: cloud link (signed requests, poll, heartbeat, relay, tunnel), the mesh (peer
// API + pings), staging + smart distribution, the update coordinator, snapshots, transfers, activities, the local
// GUI API and the edge. Normative: SPEC-final.md §1–§16.
package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"opm/agent/internal/dlog"
	"opm/agent/internal/docker"
	"opm/agent/internal/driver"
	"opm/agent/internal/edge"
	"opm/agent/internal/model"
	"opm/agent/internal/sign"
	"opm/agent/internal/store"
)

// Version is the agent version (set by the build with -ldflags "-X opm/agent/internal/agent.Version=…").
var Version = "0.1.0-dev"

// Timings are the loop periods (§9.1). Tests shorten them.
type Timings struct {
	PollWait        int // seconds
	Heartbeat       time.Duration
	Ping            time.Duration
	PingTimeout     time.Duration
	ViewExchange    time.Duration
	Actors          time.Duration
	Outbox          time.Duration
	Pipe            time.Duration
	Sampler         time.Duration
	Rollup          time.Duration
	TunnelExpiry    time.Duration
	TunnelIdle      time.Duration
	Reachable       time.Duration // answered within → reachable
	Unreachable     time.Duration // site.unreachable after
	Internet        time.Duration // last direct call within → internet
	StepPoll        time.Duration
	Monitor         time.Duration
	EnrollRetry     time.Duration
	FetchPoll       time.Duration
	StageRetryMin   time.Duration
	StageRetryMax   time.Duration
	HaveCache       time.Duration
	AbandonAfter    time.Duration
	OldPortGrace    time.Duration
	EdgeRetry       time.Duration // retry of an edge port that could not be bound (§21 #86)
	Transfers       time.Duration // §21 #100 transfers loop period = the first retry delay (backoff ×2, max TransfersMax)
	TransfersMax    time.Duration
	ScheduledAtHour int
}

// DefaultTimings are the production values.
func DefaultTimings() Timings {
	return Timings{PollWait: 25, Heartbeat: 30 * time.Second, Ping: 10 * time.Second, PingTimeout: 5 * time.Second,
		ViewExchange: 10 * time.Second, Actors: 60 * time.Second, Outbox: 5 * time.Second, Pipe: 10 * time.Second,
		Sampler: 30 * time.Second, Rollup: time.Hour, TunnelExpiry: 5 * time.Second, TunnelIdle: 30 * time.Second,
		Reachable: 30 * time.Second, Unreachable: 90 * time.Second, Internet: 90 * time.Second, StepPoll: 2 * time.Second,
		Monitor: 5 * time.Second, EnrollRetry: 10 * time.Second, FetchPoll: 5 * time.Second,
		StageRetryMin: 5 * time.Second, StageRetryMax: 5 * time.Minute, HaveCache: 10 * time.Second,
		AbandonAfter: 10 * time.Minute, OldPortGrace: 10 * time.Minute, EdgeRetry: 30 * time.Second, ScheduledAtHour: 2,
		Transfers: 15 * time.Second, TransfersMax: 5 * time.Minute}
}

// Options configure an agent.
type Options struct {
	Config          model.AgentConfigFile
	ConfigPath      string
	TokenFile       string
	Timings         Timings
	ApproveWait     time.Duration
	CompleteTimeout time.Duration
	LogLevel        string
	LogEcho         io.Writer
	HTTPClient      *http.Client // cloud client (tests); default: proxy aware
	DisableSchedule bool
	// §21 #90 self-management hooks (tests inject fakes; zero values = production behaviour)
	Version       string        // the running version (default: the Version build var)
	BinaryPath    string        // the opm-agent binary (default: this executable, symlinks resolved)
	Supervisor    Supervisor    // restart (default: systemd when run by systemd, else re-exec)
	SpawnWatcher  WatchSpawner  // starts `opm-agent.prev upgrade-watch` (default: systemd-run / setsid)
	UpgradeHealth time.Duration // health window after the restart (default AGENT_UPGRADE_HEALTH_SEC; env OPM_UPGRADE_HEALTH_SEC)
	GuardScript   string        // boot guard script path (default /usr/local/lib/opm/upgrade-guard.sh)
	GuardDropIn   string        // boot guard drop-in dir (default /etc/systemd/system/opm-agent.service.d)
}

// OptionsFromEnv fills env-driven options (§19.3).
func OptionsFromEnv(o *Options) {
	if v, err := strconv.Atoi(os.Getenv("OPM_APPROVE_WAIT_SEC")); err == nil && v >= 1 {
		o.ApproveWait = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(os.Getenv("OPM_COMPLETE_TIMEOUT_SEC")); err == nil && v >= 1 {
		o.CompleteTimeout = time.Duration(v) * time.Second
	}
	if v := os.Getenv("OPM_LOG_LEVEL"); v != "" {
		o.LogLevel = v
	}
	if v, err := strconv.Atoi(os.Getenv("OPM_UPGRADE_HEALTH_SEC")); err == nil && v >= 5 {
		o.UpgradeHealth = time.Duration(v) * time.Second
	}
}

// Agent is one site agent.
type Agent struct {
	// modeHealDone: the one re-apply of the head at start when a mounted file has a stale mode (initMonitor)
	modeHealDone    bool
	opt             Options
	version         string     // the running version (Options.Version, else Version)
	self            *selfState // §21 #90 self-management (self.go)
	cfg             model.AgentConfigFile
	dep             string
	site            string
	dataDir         string
	legacyMigrating atomic.Bool // §21 #105 J13 migration loop running
	xfer            xferTracker // §21 #100 store-and-forward delivery state (transfer.go)
	db              *store.DB
	key             ed25519.PrivateKey
	pub             ed25519.PublicKey
	log             *dlog.Logger
	dc              *docker.Client
	drv             *driver.Driver
	tm              Timings
	instance        string
	http            *http.Client
	started         time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	actMu sync.Mutex
	// cloud link
	lastDirectOk time.Time
	directFailAt time.Time
	lastCloudOk  time.Time
	cloudRoute   string
	clockOffset  int64
	// §21 #95 intent waits (hold-to-fast-forward), intentwait.go
	iwMu sync.Mutex
	iw   map[string]*intentWait
	// §21 #96 volume manager, volops.go
	volMu   sync.Mutex
	volOps  map[string]*model.VolumeOp
	volBusy map[string]string
	volRate map[string][]time.Time
	vs      *vsState     // §21 #101 volume content sync (vcsync.go)
	remote  *remoteState // §21 #118 remote GUI through the cloud (remotegui.go)
	// mesh
	peerState map[string]*peerRuntime
	haveCache map[string]*haveEntry
	// listeners
	listeners   map[string]*listener // purpose → listener
	bindErrs    map[string]model.BindError
	edgeDefault bool       // the built-in default edge runs (§21 #86)
	edgeMu      sync.Mutex // serializes edge reloads (APPLY, upstream refresh, 30 s bind retry)
	edgeSocks   map[int]*edgeSock
	// edge
	edgeRunning  bool
	edgeHTTP     *int
	edgeHTTPS    *int
	edgeErr      *string
	edgeRevSeq   *int
	edgeReloaded *string
	certErrs     map[string]string
	// staging
	stagers map[string]context.CancelFunc
	details map[string]*model.StagingDetail
	// update
	updateMu   sync.Mutex
	siteStates map[string]*model.UpdateSiteState
	runs       map[string]*model.UpdateRun
	coordLast  map[string]time.Time                  // updateId → last contact by the coordinator
	updCtl     map[string]map[int]context.CancelFunc // §21 #110 updateId → cancel of its running loops (terminate)
	updCtlN    int
	// distribution
	fetching map[string]*fetchJob
	// snapshots in progress
	snapBusy map[string]bool
	// tunnel
	tun *tunnel
	// misc
	actorRate map[string][]time.Time
	revealAt  map[string]time.Time
	revealN   map[string]revealWindow // sessionId → reveals in the current fixed minute (SPEC #84 e)
	kick      map[string]chan struct{}
	enrolled  bool
	deleted   bool
	monitorFn map[string]*driver.Built
	clock     func() time.Time                                // injectable (tests); default time.Now
	sleepFn   func(ctx context.Context, d time.Duration) bool // injectable (tests); default sleepCtx
	apiOnce   sync.Once
	apiH      http.Handler
	siteOnce  sync.Once
	// §21 #104 revisions that are branch objects (merge results, cloud heads): no proposal reports
	branchRevs map[string]bool
	siteH      http.Handler
	// §21 #112 the decoded head config for read-only hot paths (headConfigRO), keyed by the head revision id
	roMu  sync.Mutex
	roRev string
	roCfg *model.Config
	roCat *model.RevisionCatalog
	// §21 #112 loop liveness (watchdog.go): name → last start / finish of each periodic loop
	beatMu sync.Mutex
	beats  map[string]*loopBeat
	// §21 #112 health of the store (a failed write marks it; status + self-metrics show it)
	storeErr atomic.Pointer[storeFailure]
	// §21 #112 unix time of the last 401 from the cloud (0 = none); procCPU = the previous CPU sample (self-metrics)
	cloudKeyRejected atomic.Int64
	procMu           sync.Mutex
	procPrev         procSample
	hbPrev           procSample // the heartbeat's own CPU window
	dockerPrev       procCalls
}

// New prepares an agent (opens the store, loads or creates the site key). Call Run to start it.
func New(opt Options) (*Agent, error) {
	c := opt.Config
	if c.APIBase == "" || c.DeploymentID == "" || c.SiteSlug == "" {
		return nil, errors.New("agent.json needs apiBase, deploymentId and siteSlug")
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/opm"
	}
	if c.AgentPort == 0 {
		c.AgentPort = 7080
	}
	if !c.GuiAliasPort.Set {
		c.GuiAliasPort = model.Some(8080)
	}
	if c.InstallTokenFile == "" {
		c.InstallTokenFile = "/etc/opm/install-token"
	}
	if opt.TokenFile != "" {
		c.InstallTokenFile = opt.TokenFile
	}
	if c.AdvertiseHost == "" {
		c.AdvertiseHost = detectIP()
	}
	if !driver.ValidNames(c.ContainerNames) {
		return nil, fmt.Errorf("agent.json containerNames must be \"plain\" or \"prefixed\" (got %q)", c.ContainerNames)
	}
	if c.ContainerNames == "" {
		c.ContainerNames = driver.NamesPlain
	}
	if opt.Timings.Ping == 0 {
		opt.Timings = DefaultTimings()
	}
	if opt.Timings.Transfers <= 0 {
		opt.Timings.Transfers = 15 * time.Second
	}
	if opt.Timings.TransfersMax <= 0 {
		opt.Timings.TransfersMax = 5 * time.Minute
	}
	if opt.Timings.EdgeRetry <= 0 {
		opt.Timings.EdgeRetry = 30 * time.Second
	}
	if opt.ApproveWait <= 0 {
		opt.ApproveWait = 60 * time.Second
	}
	if opt.CompleteTimeout <= 0 {
		opt.CompleteTimeout = 300 * time.Second
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.DataDir = abs
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	for _, d := range []string{"volumes", "files", "secrets", "blobs/sha256", "blobs/files", "notfound", "snapshots", "certs", "edge", "transfers", "tunnel", "logs", "tmp"} {
		perm := os.FileMode(0o700)
		if d == "volumes" || d == "files" {
			perm = 0o755
		}
		if err := os.MkdirAll(filepath.Join(abs, d), perm); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(abs, 0o700)
	// §21 #112 a broken opm.db is replaced by the newest daily backup instead of a restart loop (edge down)
	db, restoredFrom, err := store.OpenOrRecover(filepath.Join(abs, "opm.db"))
	if err != nil {
		return nil, err
	}
	if restoredFrom != "" {
		fmt.Fprintf(os.Stderr, "opm-agent: opm.db was broken; restored the daily backup %s (the broken file is kept next to it)\n", restoredFrom)
	}
	key, _, err := sign.LoadOrCreateKey(filepath.Join(abs, "site.key"))
	if err != nil {
		db.Close()
		return nil, err
	}
	a := &Agent{opt: opt, cfg: c, dep: c.DeploymentID, site: c.SiteSlug, dataDir: abs, db: db, key: key,
		pub: key.Public().(ed25519.PublicKey), tm: opt.Timings, instance: c.DeploymentID + "/" + c.SiteSlug,
		started: time.Now(), cloudRoute: "none",
		peerState: map[string]*peerRuntime{}, haveCache: map[string]*haveEntry{}, listeners: map[string]*listener{}, edgeSocks: map[int]*edgeSock{},
		bindErrs: map[string]model.BindError{}, certErrs: map[string]string{}, stagers: map[string]context.CancelFunc{},
		details: map[string]*model.StagingDetail{}, siteStates: map[string]*model.UpdateSiteState{}, runs: map[string]*model.UpdateRun{},
		coordLast: map[string]time.Time{}, updCtl: map[string]map[int]context.CancelFunc{}, fetching: map[string]*fetchJob{}, snapBusy: map[string]bool{},
		actorRate: map[string][]time.Time{}, revealAt: map[string]time.Time{}, revealN: map[string]revealWindow{}, kick: map[string]chan struct{}{},
		monitorFn: map[string]*driver.Built{}, clock: time.Now, sleepFn: sleepCtx, iw: map[string]*intentWait{},
		volOps: map[string]*model.VolumeOp{}, volBusy: map[string]string{}, volRate: map[string][]time.Time{}}
	for _, k := range []string{"pipe", "outbox", "poll", "heartbeat", "ping", "distrib", "transfers", "pending", "drift"} {
		a.kick[k] = make(chan struct{}, 1)
	}
	a.log = dlog.New(filepath.Join(abs, "logs"), opt.LogLevel, opt.LogEcho)
	db.OnError = a.noteStoreError // §21 #112
	a.dc = docker.New(docker.DefaultHost(c.DockerHost))
	a.drv = driver.New(a.dc, a.dep, a.site, abs, a.log, opt.CompleteTimeout)
	a.drv.Names = c.ContainerNames
	a.drv.Adopted = a.isAdopted // §21 #104: hand-made containers a drift commit made part of the config
	a.http = opt.HTTPClient
	if a.http == nil {
		a.http = newCloudHTTPClient(c)
	}
	a.tun = newTunnel()
	a.version = opt.Version
	if a.version == "" {
		a.version = Version
	}
	a.self = newSelfState(a)                      // §21 #90
	_ = a.db.SetKV("lastAgentVersion", a.version) // §21 #91: labels the DB backup of the next schema migration
	var off int64
	if ok, _ := a.db.GetKV("clockOffsetSec", &off); ok {
		a.clockOffset = off
	}
	var del bool
	if ok, _ := a.db.GetKV("deleted", &del); ok {
		a.deleted = del
	}
	var enr map[string]any
	if ok, _ := a.db.GetKV("enrolled", &enr); ok {
		a.enrolled = true
	}
	return a, nil
}

// Site returns the site slug.
func (a *Agent) Site() string { return a.site }

// DataDir returns the data directory.
func (a *Agent) DataDir() string { return a.dataDir }

// DB exposes the store (tests, CLI).
func (a *Agent) DB() *store.DB { return a.db }

// Log exposes the debug logger.
func (a *Agent) Log() *dlog.Logger { return a.log }

func (a *Agent) now() time.Time { return time.Now() }

// nowTs is an ISO timestamp (UTC, milliseconds).
func nowTs() string { return tsOf(time.Now()) }

func tsOf(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseTs(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (a *Agent) signTs() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Now().Unix() + a.clockOffset
}

func (a *Agent) kickLoop(name string) {
	if ch, ok := a.kick[name]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *Agent) goLoop(name string, fn func(ctx context.Context)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 8192)
				n := runtime.Stack(buf, false)
				a.log.Error("gui", "loop panic", "loop", name, "panic", fmt.Sprint(r), "stack", string(buf[:n]))
			}
		}()
		fn(ctx(a))
	}()
}

func ctx(a *Agent) context.Context { return a.ctx }

// every runs fn at the period (and when kicked) until the agent stops.
func (a *Agent) every(name string, period time.Duration, fn func(ctx context.Context)) {
	a.goLoop(name, func(c context.Context) {
		t := time.NewTimer(0)
		defer t.Stop()
		kick := a.kick[name]
		for {
			select {
			case <-c.Done():
				return
			case <-t.C:
			case <-kick:
			}
			a.beatStart(name, period) // §21 #112 liveness
			func() {
				panicked := ""
				defer func() {
					if r := recover(); r != nil {
						buf := make([]byte, 8192)
						n := runtime.Stack(buf, false)
						panicked = fmt.Sprint(r)
						a.log.Error("gui", "loop panic", "loop", name, "panic", panicked, "stack", string(buf[:n]))
					}
					a.beatEnd(name, panicked)
				}()
				fn(c)
			}()
			t.Reset(period)
		}
	})
}

func sleepCtx(c context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c.Done():
		return false
	case <-t.C:
		return true
	}
}

// Run starts every loop and listener and blocks until ctx is cancelled; then it shuts down gracefully.
func (a *Agent) Run(parent context.Context) error {
	a.ctx, a.cancel = context.WithCancel(parent)
	// §21 #112 one shared view of Docker (lists + inspects cached until an event says they changed)
	a.dc.StartCache(a.ctx, func(msg string, kv ...any) { a.log.Warn("docker", msg, kv...) })
	edge.Register(a.instance, a)
	a.activity(nil, "system", "agent.started", "agent started", map[string]any{"agentVersion": a.version, "headRevId": a.headRevIDOrNil()})
	a.log.Info("gui", "agent starting", "version", a.version, "site", a.site, "dataDir", a.dataDir)
	go func() { // log the docker engine + negotiated API version (a refusal shows up again at staging)
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		if v, err := a.dc.Version(ctx); err != nil {
			a.log.Warn("docker", "docker daemon not reachable", "host", a.dc.Host, "error", err.Error())
		} else if api, err := a.dc.Negotiate(ctx); err != nil {
			a.log.Error("docker", "docker engine refused", "dockerVersion", v.Version, "apiVersion", v.APIVersion, "error", err.Error())
		} else {
			a.log.Info("docker", "docker engine", "dockerVersion", v.Version, "serverApi", v.APIVersion, "api", api)
			a.migrateContainerNames(ctx, "start") // §21 #88: rename managed containers in place to the naming mode
			a.checkAliases(ctx, "start")          // §21 #93: every managed container answers on its slug
		}
	}()
	a.finishHomeMove() // §21 #119 a moved agent home: paths in opm.db, the containers re-applied (movehome.go)
	a.moveFileBlobs()
	a.moveLegacyVolumes(nil)
	a.loadVolumeOps() // §21 #96: an action interrupted by a stop → failed, its containers started again
	a.bindInitial()
	a.recoverOnStart()
	a.resumeRecovery() // §21 #110 terminate with rollback + restartAgent
	a.migrateCommits() // §21 #104: the revision chain → branch commits (idempotent)
	a.every("enroll", a.tm.EnrollRetry, a.enrollLoop)
	a.goLoop("poll", a.pollLoop)
	a.every("heartbeat", a.tm.Heartbeat, a.heartbeatOnce)
	a.every("ping", a.tm.Ping, a.pingAll)
	a.every("view", a.tm.ViewExchange, a.exchangeViews)
	a.every("actors", a.tm.Actors, a.mergeActors)
	a.every("outbox", a.tm.Outbox, a.outboxOnce)
	a.every("pipe", a.tm.Pipe, a.pipeOnce)
	a.every("sampler", a.tm.Sampler, a.sampleOnce)
	a.every("rollup", a.tm.Rollup, a.rollupOnce)
	a.every("tunnel", a.tm.TunnelExpiry, a.tunnelExpiry)
	a.every("monitor", a.tm.Monitor, a.monitorOnce)
	a.every("transfers", a.tm.Transfers, a.transfersOnce)
	a.every("pending", time.Minute, a.pushPendingParts)
	a.every("housekeeping", time.Hour, a.housekeeping)
	a.every("watchdog", time.Minute, a.watchdogOnce)            // §21 #110
	a.every("volume-backup", 5*time.Minute, a.volumeBackupLoop) // §21 #112 per-volume automatic backups
	a.every("db-maint", 6*time.Hour, a.dbMaintenance)           // §21 #112 opm.db backup + prune + checkpoint
	a.every("edge-retry", a.tm.EdgeRetry, a.edgeRetryOnce)
	a.startTools()                             // §21 #89 operator toolbox: metrics sampler + transcript cleanup (tools_hook.go)
	a.startDevTools()                          // §21 #97 dev tools: tables + pooled-client sweeper (devtools_hook.go)
	a.startVolumeSync()                        // §21 #101 persist-to-cloud volumes: scanner + uploader (vcsync.go)
	a.self.startupDone.Store(true)             // §21 #91 GET /_opm/api/health ok from here
	a.goLoop("upgrade-boot", a.upgradeOnStart) // §21 #90: finish / report an agent upgrade (selfboot.go)
	if !a.opt.DisableSchedule {
		a.goLoop("schedule", a.scheduleLoop)
	}
	a.resumeStaging()
	a.goLoop("drift", a.driftLoop)  // §21 #104 drift detection
	a.watchdogFeeder()              // §21 #112 systemd watchdog (only under WatchdogSec)
	a.startLegacyWorkingMigration() // §21 #105 J13: 0.14.0 saved edits → auto-commit or drop (once)
	<-a.ctx.Done()
	a.shutdown()
	return nil
}

// Stop cancels Run.
func (a *Agent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *Agent) shutdown() {
	a.log.Info("gui", "agent stopping")
	a.mu.Lock()
	for _, c := range a.stagers {
		c()
	}
	a.mu.Unlock()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.mu.Lock()
	ls := make([]*listener, 0, len(a.listeners))
	for _, l := range a.listeners {
		ls = append(ls, l)
	}
	a.mu.Unlock()
	for _, l := range ls {
		_ = l.srv.Shutdown(sctx)
	}
	_ = edge.Stop(a.instance)
	a.mu.Lock()
	a.edgeRunning = false
	a.mu.Unlock()
	a.releaseEdgeSockets()
	edge.Unregister(a.instance)
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	a.log.Close()
	a.db.Close()
}
