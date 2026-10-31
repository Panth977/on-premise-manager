package apitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Port of backend/test/08-scripts.test.ts: the scripts are the opm-cloud subcommands upload-agent and create-admin.
func TestScripts(t *testing.T) {
	t.Run("scripts: upload-agent writes agent/<v>/<os>-<arch>/opm-agent + .sha256 + .sig + latest; create-admin is idempotent", func(t *testing.T) {
		dir := t.TempDir()
		NoErr(t, os.WriteFile(filepath.Join(dir, "opm-agent-linux-amd64"), []byte("linux-amd64-bin"), 0o666))
		NoErr(t, os.Mkdir(filepath.Join(dir, "darwin-arm64"), 0o777))
		NoErr(t, os.WriteFile(filepath.Join(dir, "darwin-arm64", "opm-agent"), []byte("darwin-arm64-bin"), 0o666))
		out := OpmCloud(t, "upload-agent", "--dir", dir, "--version", "9.8.7-test", "--latest")
		Match(t, out, `agent/latest = 9\.8\.7-test`)
		a := ParseJ([]byte(OpmCloud(t, "create-admin", "--email", "script-admin@opm.test", "--password", "pw-123456", "--name", "Script Admin")))
		b := ParseJ([]byte(OpmCloud(t, "create-admin", "--email", "script-admin@opm.test", "--password", "pw-654321", "--name", "Script Admin")))
		Eq(t, a.At("uid"), b.At("uid"))
		True(t, a.At("uid").Truthy())
		bin := Bucket.Download(t, "agent/9.8.7-test/linux-amd64/opm-agent")
		sha := Bucket.Download(t, "agent/9.8.7-test/linux-amd64/opm-agent.sha256")
		sig := Bucket.Download(t, "agent/9.8.7-test/darwin-arm64/opm-agent.sig")
		latest := Bucket.Download(t, "agent/latest")
		acc := Doc(t, "accounts/"+a.At("uid").Str())
		user, err := AuthAdmin.GetUserByEmail("script-admin@opm.test")
		NoErr(t, err)
		Eq(t, string(bin), "linux-amd64-bin")
		Eq(t, strings.TrimSpace(string(sha)), Sha256Hex("linux-amd64-bin"))
		Match(t, strings.TrimSpace(string(sig)), `^[A-Za-z0-9_-]{86}$`)
		Eq(t, strings.TrimSpace(string(latest)), "9.8.7-test")
		Eq(t, acc.At("createdBy"), "bootstrap")
		Eq(t, user.UID, a.At("uid"))
	})
}
