package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"opm/agent/internal/movehome"
	"opm/agent/internal/selfupgrade"
)

// cmdMoveHome (hidden, §21 #119): started by the agent (systemd-run) after it prepared a move of its home; stops the
// agent service, moves the folder, points agent.json + the systemd units at it, starts the agent again.
func cmdMoveHome(args []string) error {
	fs := flag.NewFlagSet("move-home", flag.ContinueOnError)
	cfg := fs.String("config", "/etc/opm/agent.json", "agent.json")
	from := fs.String("from", "", "the current agent home")
	to := fs.String("to", "", "the new agent home")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return fmt.Errorf("usage: opm-agent move-home --config agent.json --from DIR --to DIR")
	}
	systemctl := func(a ...string) error {
		out, err := exec.Command("systemctl", a...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(a, " "), err, bytes.TrimSpace(out))
		}
		return nil
	}
	bin, _ := os.Executable()
	return movehome.Run(movehome.Options{
		ConfigPath: *cfg, From: *from, To: *to,
		// the SOCKET too: a socket-activated agent is started again by any connection (peers ping it every few
		// seconds) and would write into the home while it is copied — §21 #119 the copy then never matches
		Stop: func() error {
			if selfupgrade.SocketInstalled("") {
				_ = systemctl("stop", selfupgrade.SocketFile)
			}
			return systemctl("stop", selfupgrade.ServiceFile)
		},
		Start: func() error {
			if selfupgrade.SocketInstalled("") {
				_ = systemctl("start", selfupgrade.SocketFile)
			}
			return systemctl("start", selfupgrade.ServiceFile)
		},
		Units: func(dir string) error {
			_, err := selfupgrade.InstallUnits(selfupgrade.UnitOptions{ConfigPath: *cfg, Binary: bin, DataDir: dir, Socket: selfupgrade.SocketInstalled(""), ScriptPath: selfupgrade.DefaultGuardScript})
			return err
		},
		Log: func(f string, a ...any) { fmt.Printf("opm-agent move-home: "+f+"\n", a...) },
	})
}
