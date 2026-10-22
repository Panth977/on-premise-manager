// Command opm-agent is the On-Prem Manager site agent (SPEC-final §4.2, §19).
//
//	opm-agent run      --config /etc/opm/agent.json [--data-dir DIR] [--token-file FILE]
//	(agentHome = --data-dir > $OPM_DATA_DIR > agent.json dataDir > /var/lib/opm)
//	opm-agent enroll   --config /etc/opm/agent.json --token-file /etc/opm/install-token
//	opm-agent install  --api URL --deployment ID --site SLUG --token opi_… [--data-dir DIR] [--agent-port N] [--gui-alias-port N] [--advertise-host H] [--docker-host H] [--config PATH] [--no-systemd]
//	opm-agent version
//	opm-agent keygen   [--data-dir DIR]
//	opm-agent verify-release --pubkey B64 --sha256 HEX --sig B64URL
//	opm-agent snapshot list   --config PATH
//	opm-agent snapshot export --config PATH --id SNAPSHOT --out FILE
//	opm-agent snapshot import --config PATH --file FILE.opmsnap
//	opm-agent status   --config PATH
//	opm-agent upgrade  [--config PATH] [--check] [--force] [--no-restart] [--pubkey B64] [--target PATH]   (enrolled site, §21 #86 / #90)
//	opm-agent upgrade-guard --install [--data-dir DIR] [--binary PATH] [--no-reload] [--print]   (hidden; §21 #90 c boot guard)
//	opm-agent systemd-units --install [--config PATH] [--socket|--no-socket] [--no-reload] [--binary PATH] [--data-dir DIR]   (hidden; §21 #91)
//	opm-agent upgrade-watch --data-dir DIR [--binary PATH] [--id UPGRADE]                        (hidden; §21 #90 c watcher)
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"opm/agent/internal/agent"
	"opm/agent/internal/model"
	"opm/agent/internal/selfupgrade"
	"opm/agent/internal/sign"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: opm-agent <command> [flags]
commands: run, enroll, install, version, keygen, verify-release, snapshot (list|export|import), status, upgrade, repair`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "enroll":
		err = cmdEnroll(args)
	case "install":
		err = cmdInstall(args)
	case "version", "--version", "-v":
		err = cmdVersion(args)
	case "keygen":
		err = cmdKeygen(args)
	case "verify-release":
		err = cmdVerifyRelease(args)
	case "snapshot":
		err = cmdSnapshot(args)
	case "repair":
		err = cmdRepair(os.Args[2:])
	case "status":
		err = cmdStatus(args)
	case "upgrade":
		err = cmdUpgrade(args)
	case "upgrade-guard": // hidden (install script + the agent itself)
		err = cmdUpgradeGuard(args)
	case "systemd-units": // hidden (install script; §21 #91 one source of truth for the units)
		err = cmdSystemdUnits(args)
	case "upgrade-watch": // hidden (started by the agent before an upgrade restart)
		err = cmdUpgradeWatch(args)
	case "move-home": // hidden (started by the agent: §21 #119 move the agent home while the agent is stopped)
		err = cmdMoveHome(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opm-agent:", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (model.AgentConfigFile, error) {
	var c model.AgentConfigFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func newAgent(fs *flag.FlagSet, args []string) (*agent.Agent, error) {
	opt, err := agentOptions(fs, args)
	if err != nil {
		return nil, err
	}
	return agent.New(opt)
}

func agentOptions(fs *flag.FlagSet, args []string) (agent.Options, error) {
	cfgPath := fs.String("config", "/etc/opm/agent.json", "agent.json path")
	dataDir := fs.String("data-dir", "", "data directory (overrides agent.json dataDir)")
	tokenFile := fs.String("token-file", "", "install token file (overrides agent.json installTokenFile)")
	if err := fs.Parse(args); err != nil {
		return agent.Options{}, err
	}
	if abs, err := filepath.Abs(*cfgPath); err == nil {
		*cfgPath = abs
	}
	c, err := loadConfig(*cfgPath)
	if err != nil {
		return agent.Options{}, err
	}
	c.DataDir = agent.ResolveDataDir(*dataDir, c.DataDir)
	opt := agent.Options{Config: c, ConfigPath: *cfgPath, TokenFile: *tokenFile, LogEcho: os.Stderr}
	agent.OptionsFromEnv(&opt)
	return opt, nil
}

func cmdRun(args []string) error {
	a, err := newAgent(flag.NewFlagSet("run", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	if agent.TestBrokenRun != "" { // smoke-test build only (agent/test/upgrade-smoke): a release that never comes up
		fmt.Fprintln(os.Stderr, "opm-agent: test build "+agent.TestBrokenRun+": exiting at start")
		os.Exit(3)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	memLimit := agent.ApplyMemoryLimit() // §21 #112 GOMEMLIMIT, else min(25 % RAM, 1 GiB)
	fmt.Fprintf(os.Stderr, "opm-agent %s: site %s, data %s, memory limit %d MiB\n", agent.Version, a.Site(), a.DataDir(), memLimit>>20)
	err = a.Run(ctx)
	stop()
	if err == nil && a.ReExecRequested() { // no supervisor (§21 #90 b): execve the same path + argv
		bin := a.BinaryPath()
		fmt.Fprintf(os.Stderr, "opm-agent: restarting (exec %s)\n", bin)
		return syscall.Exec(bin, os.Args, os.Environ())
	}
	return err
}

func cmdEnroll(args []string) error {
	a, err := newAgent(flag.NewFlagSet("enroll", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := a.Enroll(ctx); err != nil {
		return err
	}
	fmt.Println("enrolled")
	return nil
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	api := fs.String("api", "", "API base URL")
	dep := fs.String("deployment", "", "deployment id")
	site := fs.String("site", "", "site slug")
	token := fs.String("token", "", "install token (opi_…)")
	cfgPath := fs.String("config", "/etc/opm/agent.json", "agent.json path to write")
	tokenPath := fs.String("token-file", "/etc/opm/install-token", "install token file to write")
	dataDir := fs.String("data-dir", "", "data directory")
	agentPort := fs.Int("agent-port", 0, "agent port before the first head (default 7080)")
	alias := fs.Int("gui-alias-port", -1, "GUI alias port before the first head (default 8080; 0 = off)")
	adv := fs.String("advertise-host", "", "mesh address announced to peers")
	dockerHost := fs.String("docker-host", "", "Docker socket")
	noSystemd := fs.Bool("no-systemd", false, "do not install / start the systemd unit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *api == "" || *dep == "" || *site == "" || *token == "" {
		return errors.New("install needs --api, --deployment, --site and --token")
	}
	c := model.AgentConfigFile{APIBase: *api, DeploymentID: *dep, SiteSlug: *site, DataDir: *dataDir, AgentPort: *agentPort, AdvertiseHost: *adv, DockerHost: *dockerHost}
	if *tokenPath != "/etc/opm/install-token" {
		c.InstallTokenFile = *tokenPath
	}
	if *alias == 0 {
		c.GuiAliasPort = model.Null[int]()
	} else if *alias > 0 {
		c.GuiAliasPort = model.Some(*alias)
	}
	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(*cfgPath, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*tokenPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*tokenPath, []byte(*token), 0o600); err != nil {
		return err
	}
	fmt.Println("wrote", *cfgPath, "and", *tokenPath)
	if *noSystemd || runtime.GOOS != "linux" {
		fmt.Printf("start the agent with: opm-agent run --config %s\n", *cfgPath)
		return nil
	}
	r, err := selfupgrade.InstallUnits(selfupgrade.UnitOptions{ConfigPath: *cfgPath, DataDir: agent.ResolveDataDir("", *dataDir), Socket: os.Getenv("OPM_SOCKET_ACTIVATION") != "0", NoReload: false})
	if err != nil {
		return err
	}
	for _, n := range r.Notes {
		fmt.Println("note:", n)
	}
	for _, c := range [][]string{{"systemctl", "enable", "--now", "opm-agent"}} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %v: %s", c, err, out)
		}
	}
	fmt.Println("Installed. Open http://<this server>:8080/_opm/ (or :7080) and log in with the deployment access key.")
	return nil
}

// cmdVersion: the version line (the upgrade self-test reads field 2); --json = AgentBuildMeta (§21 #91);
// --release-json = the same without os/arch (make dist → dist/release.json → agent/<v>/release.json).
func cmdVersion(args []string) error {
	asJSON, release := false, false
	for _, a := range args {
		switch strings.TrimLeft(a, "-") {
		case "json":
			asJSON = true
		case "release-json":
			release = true
		}
	}
	if !asJSON && !release {
		fmt.Println(agent.VersionLine(runtime.GOOS, runtime.GOARCH, runtime.Version()))
		return nil
	}
	m := agent.BuildMeta()
	if release {
		m.OS, m.Arch = "", ""
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(b))
	return nil
}

func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "data directory (default OPM_DATA_DIR, else /var/lib/opm)")
	_ = fs.Parse(args)
	k, created, err := sign.LoadOrCreateKey(filepath.Join(agent.ResolveDataDir(*dataDir, ""), "site.key"))
	if err != nil {
		return err
	}
	pub := k.Public().(ed25519.PublicKey)
	fmt.Printf("publicKey   %s\nfingerprint %s\ncreated     %v\n", sign.PublicKeyB64(pub), sign.Fingerprint(pub), created)
	return nil
}

func cmdVerifyRelease(args []string) error {
	fs := flag.NewFlagSet("verify-release", flag.ExitOnError)
	pub := fs.String("pubkey", "", "release public key (base64)")
	sha := fs.String("sha256", "", "sha256 hex of the binary")
	sig := fs.String("sig", "", "signature (base64url)")
	_ = fs.Parse(args)
	if err := sign.VerifyRelease(*pub, *sha, *sig); err != nil {
		return err
	}
	fmt.Println("release signature ok")
	return nil
}

func cmdStatus(args []string) error {
	a, err := newAgent(flag.NewFlagSet("status", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a.StatusSummary(), "", "  ")
	fmt.Println(string(b))
	return nil
}

// cmdUpgrade: `opm-agent upgrade` = the same engine as the GUI (download → verify → swap with opm-agent.prev → pending +
// watcher → systemctl restart; the boot guard is (re)installed first). Without systemd (or --no-restart) the binary is
// swapped and the operator restarts the agent; the new agent finishes the upgrade when it starts.
func cmdUpgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	check := fs.Bool("check", false, "only check the pinned release (verify-release parity)")
	force := fs.Bool("force", false, "reinstall even when the binary already matches")
	noRestart := fs.Bool("no-restart", false, "do not restart opm-agent.service (restart it yourself)")
	pub := fs.String("pubkey", "", "release public key (base64; default agent.json releasePublicKey, else the cloud's)")
	target := fs.String("target", "", "binary to replace (default: this executable)")
	opt, err := agentOptions(fs, args)
	if err != nil {
		return err
	}
	if *target != "" {
		opt.BinaryPath = *target
	}
	restart := !*noRestart
	if restart && selfupgrade.SystemdAvailable() {
		opt.Supervisor = agent.SystemdSupervisor()
	} else {
		opt.Supervisor = agent.NoRestartSupervisor{}
		restart = false
	}
	a, err := agent.New(opt)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	last := ""
	res, err := a.SelfUpgrade(ctx, agent.UpgradeOptions{PublicKey: *pub, Force: *force, CheckOnly: *check, NoRestart: !restart,
		Progress: func(p model.AgentUpgradeProgress) {
			line := p.Phase
			if p.Phase == model.UpgradeDownload && p.Progress != nil {
				line = fmt.Sprintf("download %3.0f%%", *p.Progress*100)
			}
			if line != last {
				last = line
				fmt.Println("  " + line)
			}
		}})
	if res != nil && res.Verification != nil {
		v := res.Verification
		fmt.Printf("release %s: signature %v (key %s, %s)\n", v.Version, v.SignatureOk, v.PublicKeySource, v.PublicKeyFingerprint)
	}
	if err != nil {
		return err
	}
	switch {
	case res.UpToDate:
		fmt.Printf("opm-agent %s is the pinned release already (%s)\n", res.To, res.Target)
		return nil
	case res.CheckOnly:
		fmt.Printf("installed %s, pinned release %s (sha256 %s) — run without --check to upgrade\n", res.From, res.To, res.Sha256)
		return nil
	}
	fmt.Printf("upgraded %s: %s → %s (sha256 %s; the previous binary is %s.prev)\n", res.Target, res.From, res.To, res.Sha256, res.Target)
	if !restart {
		fmt.Println("not restarted: restart the agent to run the new version (sudo systemctl restart opm-agent, or start `opm-agent run` again)")
		return nil
	}
	fmt.Println("opm-agent.service is restarting; the new agent must report healthy within the health window, else opm-agent.prev is restored")
	return nil
}

// cmdUpgradeGuard installs the boot guard (script + systemd drop-in). Exit 0 with a note when not root / no systemd.
func cmdUpgradeGuard(args []string) error {
	fs := flag.NewFlagSet("upgrade-guard", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	install := fs.Bool("install", false, "write "+selfupgrade.DefaultGuardScript+" + "+selfupgrade.DefaultDropInDir+"/"+selfupgrade.DropInName)
	dataDir := fs.String("data-dir", "", "agent data dir (default /var/lib/opm)")
	binary := fs.String("binary", "", "agent binary (default /usr/local/bin/opm-agent)")
	noReload := fs.Bool("no-reload", false, "skip systemctl daemon-reload")
	printScript := fs.Bool("print", false, "print the guard script")
	fs.Usage = func() {
		fmt.Println("usage: opm-agent upgrade-guard --install [--data-dir DIR] [--binary PATH] [--no-reload] | --print")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // --help exits 0 (the install script probes it)
		}
		return err
	}
	if *printScript {
		_, err := os.Stdout.Write(selfupgrade.GuardScript)
		return err
	}
	if !*install {
		fs.Usage()
		return nil
	}
	dd := *dataDir
	if dd == "" {
		dd = agent.ResolveDataDir("", "")
	}
	res, err := selfupgrade.InstallGuard(selfupgrade.GuardOptions{DataDir: dd, Binary: *binary, NoReload: *noReload})
	if err != nil {
		return err
	}
	switch {
	case res.Skipped != "":
		fmt.Println("upgrade guard not installed:", res.Skipped)
	case res.Changed:
		fmt.Printf("upgrade guard installed: %s + %s\n", res.ScriptPath, res.DropInPath)
	default:
		fmt.Println("upgrade guard up to date:", res.DropInPath)
	}
	return nil
}

// cmdUpgradeWatch is the known-good watcher (run as opm-agent.prev outside the service, §21 #90 c).
// cmdSystemdUnits writes opm-agent.service + opm-agent.socket (or removes it) + the guard drop-in (§19.2, §21 #90 c, #91 f).
func cmdSystemdUnits(args []string) error {
	fs := flag.NewFlagSet("systemd-units", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	install := fs.Bool("install", false, "write "+selfupgrade.DefaultUnitDir+"/opm-agent.service (+ opm-agent.socket) + the upgrade guard drop-in")
	cfgPath := fs.String("config", "/etc/opm/agent.json", "agent.json path (ExecStart + socket ports)")
	binary := fs.String("binary", selfupgrade.DefaultBinary, "agent binary")
	dataDir := fs.String("data-dir", "", "agent data dir (default: agent.json dataDir, else /var/lib/opm)")
	socket := fs.Bool("socket", false, "install opm-agent.socket (default unless OPM_SOCKET_ACTIVATION=0)")
	noSocket := fs.Bool("no-socket", false, "no socket activation (removes an existing opm-agent.socket)")
	noReload := fs.Bool("no-reload", false, "skip systemctl daemon-reload")
	unitDir := fs.String("unit-dir", "", "") // tests
	printUnits := fs.Bool("print", false, "print the units instead of writing them")
	fs.Usage = func() {
		fmt.Println("usage: opm-agent systemd-units --install [--config PATH] [--socket|--no-socket] [--no-reload] [--binary PATH] [--data-dir DIR] | --print")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	useSocket := !*noSocket && (*socket || os.Getenv("OPM_SOCKET_ACTIVATION") != "0")
	dd := *dataDir
	if dd == "" {
		if c, err := loadConfig(*cfgPath); err == nil {
			dd = agent.ResolveDataDir("", c.DataDir)
		} else {
			dd = agent.ResolveDataDir("", "")
		}
	}
	if *printUnits {
		fmt.Print(selfupgrade.ServiceUnit(*binary, *cfgPath, useSocket))
		if useSocket {
			c, _ := loadConfig(*cfgPath)
			fmt.Print("\n# opm-agent.socket\n" + selfupgrade.SocketUnit(selfupgrade.SocketPorts(c)))
		}
		fmt.Print("\n# opm-agent.service.d/upgrade-guard.conf\n" + selfupgrade.DropIn(selfupgrade.GuardOptions{DataDir: dd, Binary: *binary}))
		return nil
	}
	if !*install {
		fs.Usage()
		return nil
	}
	r, err := selfupgrade.InstallUnits(selfupgrade.UnitOptions{ConfigPath: *cfgPath, Binary: *binary, DataDir: dd, Socket: useSocket, NoReload: *noReload, UnitDir: *unitDir, Force: *unitDir != ""})
	if err != nil {
		return err
	}
	if r.Skipped != "" {
		fmt.Println("systemd units not installed:", r.Skipped)
		return nil
	}
	for _, n := range r.Notes {
		fmt.Println("note:", n)
	}
	for _, w := range r.Written {
		fmt.Println("wrote", w)
	}
	for _, w := range r.Removed {
		fmt.Println("removed", w)
	}
	if r.Socket {
		fmt.Printf("socket activation: opm-agent.socket holds ports %v\n", r.SocketPorts)
	} else {
		fmt.Println("socket activation: off")
	}
	if len(r.Written) == 0 && len(r.Removed) == 0 {
		fmt.Println("systemd units up to date")
	}
	return nil
}

func cmdUpgradeWatch(args []string) error {
	fs := flag.NewFlagSet("upgrade-watch", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "agent data dir")
	binary := fs.String("binary", "", "agent binary (default pending.json binary)")
	id := fs.String("id", "", "upgradeId (default pending.json)")
	_ = fs.Parse(args)
	if *dataDir == "" {
		return errors.New("upgrade-watch needs --data-dir")
	}
	paths := selfupgrade.For(*dataDir)
	_ = os.MkdirAll(paths.Dir, 0o700)
	var out io.Writer = os.Stderr
	if f, err := os.OpenFile(paths.WatchLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		out = io.MultiWriter(os.Stderr, f)
	}
	var ctl selfupgrade.Controller
	if p := paths.ReadPending(); p != nil {
		ctl = selfupgrade.ControllerFor(p, filepath.Join(*dataDir, "logs", "agent-restart.log"))
	}
	res, err := selfupgrade.Watch(context.Background(), selfupgrade.WatchOptions{DataDir: *dataDir, Binary: *binary, ID: *id, Ctl: ctl, Log: out})
	fmt.Fprintf(out, "%s upgrade-watch: result %s\n", selfupgrade.Ts(time.Now()), res)
	return err
}

func cmdSnapshot(args []string) error {
	if len(args) < 1 {
		return errors.New("snapshot list|export|import")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("snapshot "+sub, flag.ExitOnError)
	id := fs.String("id", "", "snapshot id")
	out := fs.String("out", "", "output file")
	file := fs.String("file", "", ".opmsnap file")
	incl := fs.Bool("include-images", false, "include image blobs")
	a, err := newAgent(fs, rest)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		b, _ := json.MarshalIndent(a.LocalSnapshots(), "", "  ")
		fmt.Println(string(b))
		return nil
	case "export":
		if *id == "" || *out == "" {
			return errors.New("export needs --id and --out")
		}
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := a.ExportSnapshot(f, *id, *incl); err != nil {
			return err
		}
		fmt.Println("wrote", *out)
		return nil
	case "import":
		if *file == "" {
			return errors.New("import needs --file")
		}
		if err := a.ImportSnapshotFile(*file, "cli"); err != nil {
			return err
		}
		fmt.Println("imported this site's part of", *file)
		return nil
	}
	return errors.New("snapshot list|export|import")
}

// cmdRepair (§21 #110): stop every update this site is stuck in (nothing rolled back), optionally roll this site back
// to its last backup, then start the agent again. The install script runs it (with --no-service) on every reinstall /
// upgrade while the agent is stopped.
func cmdRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ExitOnError)
	rollback := fs.Bool("rollback", false, "also roll this site back to its last backup (the pre-update snapshot of the stuck update, else the newest snapshot at the head config)")
	snap := fs.String("snapshot", "", "with --rollback: this snapshot (taken at the head config) instead of the last one")
	check := fs.Bool("check", false, "only report: change nothing (exit code 3 when an update is stuck or the database is damaged)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	noService := fs.Bool("no-service", false, "do not stop / start opm-agent.service (the caller already stopped it)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: opm-agent repair [--check] [--rollback [--snapshot ID]] [--json] [--no-service] [--config agent.json]")
		fs.PrintDefaults()
	}
	opt, err := agentOptions(fs, args)
	if err != nil {
		return err
	}
	systemd := !*noService && !*check && runtime.GOOS == "linux" && !selfupgrade.UnderSystemd() && hasSystemd()
	wasActive := false
	if systemd {
		if exec.Command("systemctl", "is-active", "--quiet", "opm-agent").Run() == nil {
			wasActive = true
			fmt.Println("stopping opm-agent.service (app containers keep running) …")
			if out, err := exec.Command("systemctl", "stop", "opm-agent").CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl stop opm-agent: %v: %s", err, out)
			}
		}
	}
	a, err := agent.New(opt)
	if err != nil {
		return err
	}
	rep, rerr := a.Repair(agent.RepairOptions{Rollback: *rollback || *snap != "", SnapshotID: *snap, CheckOnly: *check})
	a.Close()
	if wasActive {
		fmt.Println("starting opm-agent.service …")
		if out, err := exec.Command("systemctl", "start", "opm-agent").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "systemctl start opm-agent: %v: %s\n", err, out)
		}
	}
	if rep != nil {
		if *asJSON {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
		} else {
			printRepair(rep, *check)
		}
	}
	if rerr != nil {
		return rerr
	}
	if *check && rep != nil && (len(rep.Stuck) > 0 || !rep.DatabaseOk) {
		os.Exit(3)
	}
	return nil
}

func hasSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

func printRepair(r *agent.RepairReport, check bool) {
	if !r.DatabaseOk {
		fmt.Println("database: DAMAGED —", r.DatabaseProblem)
	} else {
		fmt.Println("database: ok")
	}
	switch {
	case len(r.Stuck) == 0:
		fmt.Println("updates:  none stuck")
	case check:
		fmt.Println("updates:  stuck:", strings.Join(r.Stuck, ", "))
	default:
		fmt.Println("updates:  stopped:", strings.Join(r.Terminated, ", "), "(nothing rolled back; the other sites stop them too)")
	}
	if r.Backup != "" {
		verb := "last backup"
		if r.RolledBack {
			verb = "rolled back to the backup"
		}
		fmt.Printf("backup:   %s %s (%s)\n", verb, r.Backup, r.BackupAt)
	} else if r.RolledBack {
		fmt.Println("backup:   none at the head config: the head config was re-applied (volumes kept)")
	}
	for _, n := range r.Notes {
		fmt.Println("note:    ", n)
	}
	if !check && !r.RolledBack && r.DatabaseOk {
		fmt.Println("To also roll this site back to its last backup: opm-agent repair --rollback")
	}
}
