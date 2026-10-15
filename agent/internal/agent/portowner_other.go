//go:build !linux

package agent

// portOwner: only Linux exposes the socket owners in /proc (§21 #86).
func portOwner(int) *string { return nil }
