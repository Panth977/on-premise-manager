//go:build !linux && !darwin

package tools

import "errors"

const ptySupported = false

type hostProc struct{}

func startHostShell(userName, shell string, cols, rows int, extraEnv []string) (*hostProc, error) {
	return nil, errors.New("the host shell is not supported on this OS")
}
func (p *hostProc) Read(b []byte) (int, error)  { return 0, errors.New("unsupported") }
func (p *hostProc) Write(b []byte) (int, error) { return 0, errors.New("unsupported") }
func (p *hostProc) Resize(cols, rows int)       {}
func (p *hostProc) Kill()                       {}
func (p *hostProc) Wait() int                   { return -1 }
