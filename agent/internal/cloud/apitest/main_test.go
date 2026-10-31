package apitest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"opm/agent/internal/cloud"
	"opm/agent/internal/cloud/driver"
	_ "opm/agent/internal/cloud/routes"
)

// The running server and the data dir it owns.
var (
	// Base is the opm-cloud origin.
	Base string
	// DataDir is the server's data dir.
	DataDir string
	// Ports is the driver over DataDir, in this process (adb / aauth / abucket of the TS helpers).
	Ports *driver.Ports
	// DB is Ports.Store.
	DB *driver.Store
	// AuthAdmin is Ports.Auth.
	AuthAdmin *driver.Auth
	// Bucket is the blob store with the calls the tests use.
	Bucket BlobShim
	// CloudBin is the built opm-cloud binary.
	CloudBin string
	// ServerEnv is the environment of the server (for running other opm-cloud subcommands on the same data dir).
	ServerEnv []string
	// ReleaseKey is the throw-away release key of this run (the server embeds the public half in install scripts).
	ReleaseKey struct{ PublicKeyB64, PrivateKeySeedB64 string }
	// TunnelPorts is the free-port range the server hands to port tunnels ("from-to").
	TunnelPorts string
	// ServerLog is the server's stdout + stderr.
	ServerLog string
	// RepoRoot is the repository root.
	RepoRoot string
	// AgentDir is the Go module (agent/).
	AgentDir string
)

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// freePortRange finds n free consecutive ports.
func freePortRange(n int) (int, int) {
next:
	for tries := 0; tries < 200; tries++ {
		from := 20000 + os.Getpid()%20000 + tries*(n+1)
		for i := 0; i < n; i++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(from+i))
			if err != nil {
				continue next
			}
			l.Close()
		}
		return from, from + n - 1
	}
	panic("no free port range")
}

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	_, f, _, _ := runtime.Caller(0)
	AgentDir = filepath.Clean(filepath.Join(filepath.Dir(f), "../../.."))
	RepoRoot = filepath.Dir(AgentDir)
	tmp, err := os.MkdirTemp("", "opm-apitest-")
	if err != nil {
		panic(err)
	}
	keep := os.Getenv("OPM_KEEP_DATA") == "1"
	defer func() {
		if keep {
			fmt.Println("data kept:", tmp)
			return
		}
		os.RemoveAll(tmp)
	}()
	CloudBin = filepath.Join(tmp, "opm-cloud")
	b := exec.Command("go", "build", "-o", CloudBin, "./cmd/opm-cloud")
	b.Dir = AgentDir
	if out, err := b.CombinedOutput(); err != nil {
		fmt.Printf("build opm-cloud: %v\n%s", err, out)
		return 1
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	ReleaseKey.PublicKeyB64 = base64.StdEncoding.EncodeToString(pub)
	ReleaseKey.PrivateKeySeedB64 = base64.StdEncoding.EncodeToString(priv.Seed())
	port := freePort()
	Base = fmt.Sprintf("http://127.0.0.1:%d", port)
	DataDir = filepath.Join(tmp, "data")
	from, to := freePortRange(4)
	TunnelPorts = fmt.Sprintf("%d-%d", from, to)
	env := map[string]string{
		"OPM_DATA_DIR":            DataDir,
		"OPM_TEST_HOOKS":          "1",
		"OPM_LOCAL_STRICT":        "1",
		"OPM_PUBLIC_URL":          Base,
		"OPM_FRONTEND_ORIGINS":    "http://localhost:5173,http://localhost:4173,http://localhost:5002",
		"TUNNEL_FREE_PORTS":       TunnelPorts,
		"OPM_OUTBOX_MIN_AGE_MS":   "1000",
		"OPM_BLOB_REDIRECT":       "0",
		"OPM_RELEASE_PUBLIC_KEY":  ReleaseKey.PublicKeyB64,
		"OPM_RELEASE_PRIVATE_KEY": ReleaseKey.PrivateKeySeedB64,
		"GCLOUD_PROJECT":          "local",
		"OPM_PORT":                strconv.Itoa(port),
		"OPM_HOST":                "127.0.0.1",
	}
	// the driver and the jobs in this process (daily) see the same settings as the server
	for k, v := range env {
		os.Setenv(k, v)
	}
	ServerEnv = os.Environ()
	ServerLog = filepath.Join(tmp, "server.log")
	logf, err := os.Create(ServerLog)
	if err != nil {
		panic(err)
	}
	defer logf.Close()
	srv := exec.Command(CloudBin, "serve")
	srv.Env = ServerEnv
	srv.Stdout, srv.Stderr = logf, logf
	if err := srv.Start(); err != nil {
		panic(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- srv.Wait() }()
	defer func() {
		_ = srv.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(25 * time.Second):
			_ = srv.Process.Kill()
			<-exited
		}
	}()
	for i := 0; ; i++ {
		if r, err := http.Get(Base + "/healthz"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		select {
		case e := <-exited:
			exited <- e
			fmt.Printf("opm-cloud exited: %v\n%s", e, readLog())
			return 1
		default:
		}
		if i > 300 {
			fmt.Printf("opm-cloud did not start\n%s", readLog())
			return 1
		}
		time.Sleep(100 * time.Millisecond)
	}
	strict := true
	Ports, err = driver.Open(driver.Config{DataDir: DataDir, PublicURL: Base, Strict: &strict})
	if err != nil {
		panic(err)
	}
	defer Ports.Close()
	cloud.SetPorts(Ports)
	DB, AuthAdmin, Bucket = Ports.Store, Ports.Auth, BlobShim{Ports.Blobs}
	rc := m.Run()
	if rc != 0 {
		l := readLog()
		if len(l) > 6000 {
			l = l[len(l)-6000:]
		}
		fmt.Printf("--- server log (tail)\n%s\n", l)
	}
	return rc
}

func readLog() string {
	b, _ := os.ReadFile(ServerLog)
	return string(bytes.TrimSpace(b))
}

// OpmCloud runs an opm-cloud subcommand on the server's data dir.
func OpmCloud(t testing.TB, args ...string) string {
	t.Helper()
	c := exec.Command(CloudBin, args...)
	c.Env = ServerEnv
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		t.Fatalf("opm-cloud %s: %v\n%s\n%s", strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}
